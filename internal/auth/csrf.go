package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

const csrfCookieName = "rb_csrf"
const csrfContextKey contextKey = 2

func CSRFToken(r *http.Request) string { v, _ := r.Context().Value(csrfContextKey).(string); return v }

// Signed double-submit tokens cover unauthenticated login forms too. WebSocket
// handshakes cannot carry form tokens, so they require a same-origin Origin.
func (a *Auth) CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie(csrfCookieName); err == nil {
			p := strings.Split(c.Value, ".")
			if len(p) == 2 && len(p[0]) == 64 && SecureCompare(p[1], a.mac("csrf:"+p[0])) {
				token = c.Value
			}
		}
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		websocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if unsafe || websocket {
			if !a.sameOrigin(r, websocket) {
				http.Error(w, "跨站请求已拒绝", http.StatusForbidden)
				return
			}
			if unsafe {
				supplied := r.Header.Get("X-CSRF-Token")
				if supplied == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
					r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
					if err := r.ParseForm(); err != nil {
						http.Error(w, "表单请求无效", http.StatusBadRequest)
						return
					}
					supplied = r.PostForm.Get("csrf_token")
				}
				if token == "" || !SecureCompare(token, supplied) {
					http.Error(w, "页面验证已失效，请刷新后重试", http.StatusForbidden)
					return
				}
			}
		}
		if token == "" {
			nonce, err := randomHex(32)
			if err != nil {
				http.Error(w, "无法创建页面验证", 500)
				return
			}
			token = nonce + "." + a.mac("csrf:"+nonce)
			http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode})
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), csrfContextKey, token)))
	})
}
func (a *Auth) sameOrigin(r *http.Request, required bool) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return !required
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	if strings.EqualFold(u.Host, r.Host) && u.Scheme == scheme {
		return true
	}
	if a.cfg.PublicURL != "" {
		p, err := url.Parse(a.cfg.PublicURL)
		return err == nil && p.Host == u.Host && p.Scheme == u.Scheme
	}
	return false
}
