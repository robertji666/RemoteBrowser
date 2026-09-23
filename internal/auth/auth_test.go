package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/config"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
)

func testAuth(t *testing.T) (*Auth, *model.User) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.Migrate(); err != nil {
		t.Fatal(err)
	}
	a := New(&config.Config{CookieSecret: strings.Repeat("secret", 8), AdminPassword: "admin-test-password", DefaultInstanceQuota: 5}, st)
	if err = a.InitDefaultUser(context.Background()); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	return a, u
}
func testUser(t *testing.T, a *Auth, admin *model.User, email string) *model.User {
	t.Helper()
	u, err := a.CreateUser(context.Background(), admin.ID, email, "Tester", "temporary-password", 2, true)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func login(t *testing.T, a *Auth, name, password string) string {
	t.Helper()
	token, err := a.Login(context.Background(), name, password)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestBootstrapPreservesExistingPassword(t *testing.T) {
	a, admin := testAuth(t)
	a.cfg.AdminPassword = "changed-env-password"
	if err := a.InitDefaultUser(context.Background()); err != nil {
		t.Fatal(err)
	}
	login(t, a, "admin", "admin-test-password")
	if _, err := a.Login(context.Background(), "admin", "changed-env-password"); err == nil {
		t.Fatal("environment changed existing password")
	}
	if err := a.ResetAdminPassword(context.Background(), "explicit-reset-password"); err != nil {
		t.Fatal(err)
	}
	updated, _ := a.store.GetUserByID(context.Background(), admin.ID)
	if updated.ID != admin.ID || updated.AuthVersion <= admin.AuthVersion {
		t.Fatal("reset must preserve ID and revoke credentials")
	}
	login(t, a, "admin", "explicit-reset-password")
}
func TestNormalizedLoginAndFirstPasswordChange(t *testing.T) {
	a, admin := testAuth(t)
	u := testUser(t, a, admin, "  Alice@Example.TEST  ")
	if u.Email != "alice@example.test" || !u.MustChangePassword {
		t.Fatalf("unexpected user %#v", u)
	}
	token := login(t, a, " Alice@EXAMPLE.test ", "temporary-password")
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	rec := httptest.NewRecorder()
	a.Middleware(a.RequirePasswordChanged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("initial password granted desktop access") }))).ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/account/password" {
		t.Fatalf("status=%d location=%s", rec.Code, rec.Header().Get("Location"))
	}
	if err := a.ChangePassword(context.Background(), u.ID, "wrong-password", "new-user-password"); err == nil {
		t.Fatal("accepted wrong current password")
	}
	if err := a.ChangePassword(context.Background(), u.ID, "temporary-password", "new-user-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateToken(context.Background(), token); err == nil {
		t.Fatal("old login survived password change")
	}
	newToken := login(t, a, u.Email, "new-user-password")
	current, err := a.ValidateToken(context.Background(), newToken)
	if err != nil || current.MustChangePassword {
		t.Fatalf("changed user=%v err=%v", current, err)
	}
}
func TestResetDisableDeleteRevokeOnlyTargetUser(t *testing.T) {
	a, admin := testAuth(t)
	alice := testUser(t, a, admin, "alice@example.test")
	bob := testUser(t, a, admin, "bob@example.test")
	oldAlice := login(t, a, alice.Email, "temporary-password")
	bobToken := login(t, a, bob.Email, "temporary-password")
	if _, err := a.ResetUserPassword(context.Background(), admin.ID, alice.ID, "replacement-password", true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateToken(context.Background(), oldAlice); err == nil {
		t.Fatal("reset did not revoke target")
	}
	if _, err := a.ValidateToken(context.Background(), bobToken); err != nil {
		t.Fatal("reset revoked unrelated user")
	}
	newAlice := login(t, a, alice.Email, "replacement-password")
	if err := a.SetUserStatus(context.Background(), admin.ID, alice.ID, model.UserDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateToken(context.Background(), newAlice); err == nil {
		t.Fatal("disabled token valid")
	}
	if _, err := a.Login(context.Background(), alice.Email, "replacement-password"); err == nil {
		t.Fatal("disabled login accepted")
	}
	if err := a.SetUserStatus(context.Background(), admin.ID, alice.ID, model.UserActive); err != nil {
		t.Fatal(err)
	}
	activeToken := login(t, a, alice.Email, "replacement-password")
	if _, err := a.ValidateToken(context.Background(), newAlice); err == nil {
		t.Fatal("enabling resurrected old login")
	}
	if err := a.store.MarkUserDeleting(context.Background(), alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateToken(context.Background(), activeToken); err == nil {
		t.Fatal("deleting account remained authenticated")
	}
	if _, err := a.ValidateToken(context.Background(), bobToken); err != nil {
		t.Fatal("deletion revoked unrelated user")
	}
}
func TestLogoutRevokesOneLoginAndTokensRejectTampering(t *testing.T) {
	a, _ := testAuth(t)
	one := login(t, a, "admin", "admin-test-password")
	two := login(t, a, "admin", "admin-test-password")
	if one == two {
		t.Fatal("logins reused nonce")
	}
	req := httptest.NewRequest("POST", "http://localhost/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: one})
	if err := a.Logout(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ValidateToken(context.Background(), one); err == nil {
		t.Fatal("logged-out token accepted")
	}
	if _, err := a.ValidateToken(context.Background(), two); err != nil {
		t.Fatal("logout affected separate login")
	}
	for _, candidate := range []string{strings.Replace(two, "v2:1:", "v2:2:", 1), "v1:1:9999999999:signature", "1:9999999999", two + "0"} {
		if _, err := a.ValidateToken(context.Background(), candidate); err == nil {
			t.Fatal("accepted tampered token")
		}
	}
	expired := a.signSession(1, 1, time.Now().Add(-time.Minute), strings.Repeat("a", 64))
	if _, err := a.ValidateToken(context.Background(), expired); err == nil {
		t.Fatal("accepted expired token")
	}
}
func TestAdminProtectionAndPasswordValidation(t *testing.T) {
	a, admin := testAuth(t)
	u := testUser(t, a, admin, "user@example.test")
	if _, err := a.ResetUserPassword(context.Background(), u.ID, admin.ID, "illegal-password", true); err == nil {
		t.Fatal("ordinary user reset administrator")
	}
	if _, err := a.ResetUserPassword(context.Background(), admin.ID, admin.ID, "illegal-password", true); err == nil {
		t.Fatal("admin management resets protected admin")
	}
	for _, password := range []string{"short", strings.Repeat("a", 73), strings.Repeat("汉", 25)} {
		if ValidatePassword(password) == nil {
			t.Errorf("accepted invalid password %q", password)
		}
	}
	if err := ValidatePassword("这是十二个中文字符组成的密码"); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"invalid", "Name <user@example.test>", "foo@example.test\nBcc: attacker@example.test"} {
		if _, err := NormalizeEmail(email); err == nil {
			t.Errorf("accepted malformed email %q", email)
		}
	}
}

func TestManualCreationRequiresPasswordWithoutCreatingAccount(t *testing.T) {
	a, admin := testAuth(t)
	if _, err := a.CreateUser(context.Background(), admin.ID, "missing-password@example.test", "", "", 1, true); err == nil {
		t.Fatal("manual account created without a password")
	}
	u, err := a.store.GetUserByEmail(context.Background(), "missing-password@example.test")
	if err != nil || u != nil {
		t.Fatalf("invalid request retained an account: %+v %v", u, err)
	}
}
func TestMailFailureKeepsSingleUserAndResetDoesNotAffectOthers(t *testing.T) {
	a, admin := testAuth(t)
	existing := testUser(t, a, admin, "existing@example.test")
	existingToken := login(t, a, existing.Email, "temporary-password")
	a.cfg.SMTP = config.SMTPConfig{Host: "localhost", Port: 25, From: "bad-from", Encryption: "plain"}
	a.cfg.PublicURL = "http://localhost"
	u, err := a.CreateUser(context.Background(), admin.ID, "mail@example.test", "", "", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if u.DeliveryStatus != "failed" || u.DeliveryError == "" {
		t.Fatalf("delivery=%s error=%s", u.DeliveryStatus, u.DeliveryError)
	}
	if _, err = a.CreateUser(context.Background(), admin.ID, "MAIL@example.test", "", "", 1, false); err == nil {
		t.Fatal("duplicate mail failure created user twice")
	}
	before := u.AuthVersion
	u, err = a.ResendWelcome(context.Background(), admin.ID, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.AuthVersion <= before || u.DeliveryStatus != "failed" {
		t.Fatal("resend must rotate password and record failure")
	}
	u, err = a.ResetUserPassword(context.Background(), admin.ID, u.ID, "manual-fallback-password", true)
	if err != nil {
		t.Fatal(err)
	}
	if u.DeliveryStatus != "manual" {
		t.Fatal("manual fallback not marked")
	}
	login(t, a, u.Email, "manual-fallback-password")
	if _, err = a.ValidateToken(context.Background(), existingToken); err != nil {
		t.Fatal("mail failure affected existing user's login")
	}
}
func TestLoginLimiter(t *testing.T) {
	a, _ := testAuth(t)
	for i := 0; i < 10; i++ {
		a.allow("login:missing@example.test", 10, time.Minute)
	}
	if _, err := a.Login(context.Background(), "missing@example.test", "anything"); err != ErrRateLimited {
		t.Fatalf("limiter error=%v", err)
	}
}
func TestSetCookieSecure(t *testing.T) {
	a, _ := testAuth(t)
	req := httptest.NewRequest("GET", "https://example.test/", nil)
	rec := httptest.NewRecorder()
	a.SetCookie(rec, req, "test")
	if !rec.Result().Cookies()[0].Secure || !rec.Result().Cookies()[0].HttpOnly {
		t.Fatal("unsafe session cookie")
	}
}

func TestMissingLoginRedirectsPagesButNeverAPIOrSocket(t *testing.T) {
	a, _ := testAuth(t)
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("unauthenticated request reached handler") }))
	for _, tt := range []struct {
		path string
		want int
	}{{"/", 303}, {"/admin/users", 303}, {"/sessions/test/view", 303}, {"/account/password", 303}, {"/api/account", 401}, {"/sessions/test/audio", 401}, {"/sessions/test/webrtc/offer", 401}} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost"+tt.path, nil))
			if w.Code != tt.want {
				t.Fatalf("code=%d", w.Code)
			}
			if tt.want == 303 && w.Header().Get("Location") != "/login?expired=1" {
				t.Fatal("redirected to non-page login endpoint")
			}
		})
	}
}
func TestCSRFAcrossFormsJSONAndWebSockets(t *testing.T) {
	a, _ := testAuth(t)
	h := a.CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, CSRFToken(r)) }))
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest("GET", "http://localhost/login", nil))
	cookie := get.Result().Cookies()[0]
	token := get.Body.String()
	tests := []struct {
		name, method, origin, body, header, upgrade string
		want                                        int
	}{
		{"form", "POST", "http://localhost", "csrf_token=" + token, "", "", 200},
		{"json", "POST", "http://localhost", "", token, "", 200},
		{"missing", "POST", "http://localhost", "", "", "", 403},
		{"cross-site", "POST", "https://evil.example", "", token, "", 403},
		{"ws", "GET", "http://localhost", "", "", "websocket", 200},
		{"ws-cross", "GET", "http://evil.example", "", "", "websocket", 403},
		{"ws-missing-origin", "GET", "", "", "", "websocket", 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "http://localhost/api/action", strings.NewReader(tt.body))
			r.AddCookie(cookie)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("X-CSRF-Token", tt.header)
			r.Header.Set("Upgrade", tt.upgrade)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
