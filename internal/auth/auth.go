package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/config"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const cookieName = "rb_session"

type contextKey int

const ctxUserKey contextKey = 1

var ErrCredentials = errors.New("邮箱或密码错误，或账号不可用")
var ErrRateLimited = errors.New("请求过于频繁，请稍后再试")

type Auth struct {
	cfg       *config.Config
	store     *store.Store
	limitMu   sync.Mutex
	attempts  map[string]rateWindow
	dummyHash []byte
}
type rateWindow struct {
	Start time.Time
	Count int
}

func New(cfg *config.Config, st *store.Store) *Auth {
	hash, _ := bcrypt.GenerateFromPassword([]byte("invalid-account-password"), bcrypt.DefaultCost)
	return &Auth{cfg: cfg, store: st, attempts: make(map[string]rateWindow), dummyHash: hash}
}
func (a *Auth) Store() *store.Store    { return a.store }
func (a *Auth) Config() *config.Config { return a.cfg }

func (a *Auth) InitDefaultUser(ctx context.Context) error {
	u, err := a.store.GetUserByUsername(ctx, "admin")
	if err != nil {
		return err
	}
	if u != nil {
		log.Print("existing administrator preserved; RB_ADMIN_PASSWORD is only used for first initialization; use explicit reset command to change it")
		return nil
	}
	if err := ValidatePassword(a.cfg.AdminPassword); err != nil {
		return fmt.Errorf("first deployment requires RB_ADMIN_PASSWORD: %w", err)
	}
	email := ""
	if a.cfg.AdminEmail != "" {
		email, err = NormalizeEmail(a.cfg.AdminEmail)
		if err != nil {
			return fmt.Errorf("RB_ADMIN_EMAIL: %w", err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(a.cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return a.store.CreateUser(ctx, &model.User{Username: "admin", Email: email, Role: model.RoleAdmin, Status: model.UserActive, InstanceQuota: a.cfg.DefaultInstanceQuota, PasswordHash: string(hash), AuthVersion: 1, CreatedAt: time.Now()})
}

// Login uses normalized identifiers and an independent per-account limiter.
// LoginHTTPMiddleware adds a source-address limiter before password work.
func (a *Auth) Login(ctx context.Context, username, password string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 254 || len(password) > 72 {
		return "", ErrCredentials
	}
	if !a.allow("login:"+username, 10, time.Minute) {
		return "", ErrRateLimited
	}
	u, err := a.store.GetUserByLogin(ctx, username)
	if err != nil {
		return "", err
	}
	if u == nil {
		_ = bcrypt.CompareHashAndPassword(a.dummyHash, []byte(password))
		return "", ErrCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil || u.Status != model.UserActive {
		return "", ErrCredentials
	}
	expires := time.Now().Add(7 * 24 * time.Hour)
	nonce, err := randomHex(32)
	if err != nil {
		return "", err
	}
	token := a.signSession(u.ID, u.AuthVersion, expires, nonce)
	if err := a.store.CreateLoginSession(ctx, tokenHash(token), u.ID, u.AuthVersion, expires); err != nil {
		return "", err
	}
	return token, nil
}

func (a *Auth) ValidateToken(ctx context.Context, token string) (*model.User, error) {
	id, version, expires, err := a.decodeSession(token)
	if err != nil || !expires.After(time.Now()) {
		return nil, ErrCredentials
	}
	u, err := a.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil || u.Status != model.UserActive || u.AuthVersion != version {
		return nil, ErrCredentials
	}
	valid, err := a.store.LoginSessionValid(ctx, tokenHash(token), id, version)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, ErrCredentials
	}
	return u, nil
}
func (a *Auth) AuthenticateRequest(r *http.Request) (*model.User, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return nil, ErrCredentials
	}
	return a.ValidateToken(r.Context(), cookie.Value)
}
func (a *Auth) ValidateRequest(r *http.Request) (*model.User, error) { return a.AuthenticateRequest(r) }
func (a *Auth) Logout(ctx context.Context, r *http.Request) error {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	return a.store.RevokeLoginSession(ctx, tokenHash(cookie.Value))
}

func (a *Auth) SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 7, Expires: time.Now().Add(7 * 24 * time.Hour)})
}
func (a *Auth) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := a.AuthenticateRequest(r)
		if err != nil {
			if isPageRequest(r) {
				a.ClearCookie(w, r)
				http.Redirect(w, r, "/login?expired=1", http.StatusSeeOther)
				return
			}
			http.Error(w, "登录已过期或账号不可用，请重新登录", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey, u)))
	})
}

func isPageRequest(r *http.Request) bool {
	if r.Method != http.MethodGet || r.Header.Get("Upgrade") != "" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		return false
	}
	p := r.URL.Path
	return p == "/" || strings.HasPrefix(p, "/admin/") || p == "/account/password" || (strings.HasPrefix(p, "/sessions/") && strings.HasSuffix(p, "/view"))
}
func (a *Auth) RequirePasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r.Context())
		if u == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if u.MustChangePassword {
			if isPageRequest(r) {
				http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			} else {
				http.Error(w, "请先修改初始密码", http.StatusForbidden)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
func UserFromContext(ctx context.Context) *model.User {
	v, _ := ctx.Value(ctxUserKey).(*model.User)
	return v
}
func UserIDFromContext(ctx context.Context) int64 {
	if u := UserFromContext(ctx); u != nil {
		return u.ID
	}
	return 0
}

func (a *Auth) signSession(id, version int64, expires time.Time, nonce string) string {
	payload := fmt.Sprintf("v2:%d:%d:%d:%s", id, version, expires.Unix(), nonce)
	return payload + ":" + a.mac(payload)
}
func (a *Auth) decodeSession(token string) (int64, int64, time.Time, error) {
	p := strings.Split(token, ":")
	if len(p) != 6 || p[0] != "v2" || len(p[4]) != 64 || !SecureCompare(p[5], a.mac(strings.Join(p[:5], ":"))) {
		return 0, 0, time.Time{}, ErrCredentials
	}
	if _, err := hex.DecodeString(p[4]); err != nil {
		return 0, 0, time.Time{}, ErrCredentials
	}
	id, e1 := strconv.ParseInt(p[1], 10, 64)
	version, e2 := strconv.ParseInt(p[2], 10, 64)
	ts, e3 := strconv.ParseInt(p[3], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || id < 1 || version < 1 {
		return 0, 0, time.Time{}, ErrCredentials
	}
	return id, version, time.Unix(ts, 0), nil
}
func (a *Auth) mac(payload string) string {
	m := hmac.New(sha256.New, []byte(a.cfg.CookieSecret))
	_, _ = m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}
func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func requestIsHTTPS(r *http.Request) bool {
	return r != nil && (r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}
func SecureCompare(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (a *Auth) allow(key string, max int, window time.Duration) bool {
	a.limitMu.Lock()
	defer a.limitMu.Unlock()
	now := time.Now()
	v, exists := a.attempts[key]
	if !exists && len(a.attempts) >= 10000 {
		for k, item := range a.attempts {
			if now.Sub(item.Start) >= time.Hour {
				delete(a.attempts, k)
			}
		}
		if len(a.attempts) >= 10000 {
			return false
		}
	}
	if now.Sub(v.Start) >= window {
		v = rateWindow{Start: now}
	}
	v.Count++
	a.attempts[key] = v
	return v.Count <= max
}
func (a *Auth) LoginHTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		// Forwarded address headers are intentionally not trusted for limiting.
		if !a.allow("ip:"+ip, 60, time.Minute) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, ErrRateLimited.Error(), http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
