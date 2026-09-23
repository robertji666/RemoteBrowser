package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/errdefs"
	"github.com/go-chi/chi/v5"
	"github.com/robertji666/RemoteBrowser/internal/auth"
	"github.com/robertji666/RemoteBrowser/internal/config"
	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/files"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/session"
	"github.com/robertji666/RemoteBrowser/internal/store"
	"github.com/robertji666/RemoteBrowser/internal/view"
)

type absentRuntime struct{}

func (absentRuntime) EnsureNetwork(context.Context, string) (string, error) { return "test", nil }
func (absentRuntime) CreateSessionContainer(context.Context, docker.CreateSessionOptions) (string, error) {
	return "", errors.New("unexpected create")
}
func (absentRuntime) InspectContainer(context.Context, string) (*docker.ContainerInfo, error) {
	return nil, errdefs.NotFound(errors.New("absent"))
}
func (absentRuntime) StartContainer(context.Context, string) error            { return nil }
func (absentRuntime) StopContainer(context.Context, string, int) error        { return nil }
func (absentRuntime) RemoveContainer(context.Context, string, bool) error     { return nil }
func (absentRuntime) ListManagedContainers(context.Context) ([]string, error) { return nil, nil }

type httpFixture struct {
	h                                *Handler
	router                           http.Handler
	st                               *store.Store
	admin, alice, bob                *model.User
	adminToken, aliceToken, bobToken string
	csrf                             *http.Cookie
}

func setupHTTP(t *testing.T) *httpFixture {
	t.Helper()
	t.Chdir("../..")
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.Migrate(); err != nil {
		t.Fatal(err)
	}
	a := auth.New(&config.Config{AdminPassword: "admin-test-password", CookieSecret: strings.Repeat("s", 32), DefaultInstanceQuota: 3}, st)
	if err = a.InitDefaultUser(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin, _ := st.GetUserByUsername(context.Background(), "admin")
	newUser := func(email string) *model.User {
		u, err := a.CreateUser(context.Background(), admin.ID, email, "Test User", "temporary-password", 3, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = a.ChangePassword(context.Background(), u.ID, "temporary-password", "permanent-password"); err != nil {
			t.Fatal(err)
		}
		u, _ = st.GetUserByID(context.Background(), u.ID)
		return u
	}
	alice, bob := newUser("alice@example.com"), newUser("bob@example.com")
	ss := session.NewService(st, absentRuntime{}, 3, 0, dir, dir, 0, 0, 0, "test", "", "", "", "", "", "", "", "", "", "", "", "", false)
	v, _ := view.New()
	h := NewHandler(a, ss, nil, nil, files.NewService(st), v, nil, 1024)
	r := chi.NewRouter()
	h.Register(r)
	f := &httpFixture{h: h, router: r, st: st, admin: admin, alice: alice, bob: bob}
	for _, pair := range []struct {
		user, password string
		dest           *string
	}{{"admin", "admin-test-password", &f.adminToken}, {alice.Email, "permanent-password", &f.aliceToken}, {bob.Email, "permanent-password", &f.bobToken}} {
		*pair.dest, err = a.Login(context.Background(), pair.user, pair.password)
		if err != nil {
			t.Fatal(err)
		}
	}
	w := f.request("GET", "/login", "", nil, false)
	for _, c := range w.Result().Cookies() {
		if c.Name == "rb_csrf" {
			f.csrf = c
		}
	}
	if f.csrf == nil {
		t.Fatal("no CSRF cookie")
	}
	return f
}

func (f *httpFixture) request(method, path, token string, form url.Values, json bool) *httptest.ResponseRecorder {
	var body string
	if form != nil {
		body = form.Encode()
	}
	r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if json {
		r.Header.Set("Accept", "application/json")
	}
	if token != "" {
		w := httptest.NewRecorder()
		f.h.auth.SetCookie(w, r, token)
		for _, c := range w.Result().Cookies() {
			r.AddCookie(c)
		}
	}
	if f.csrf != nil {
		r.AddCookie(f.csrf)
		r.Header.Set("X-CSRF-Token", f.csrf.Value)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func TestAccountHTTPUserManagementAndTemplates(t *testing.T) {
	f := setupHTTP(t)
	for _, path := range []string{"/", "/admin/users", "/admin/users?status=active&q=alice&page=1", fmt.Sprintf("/admin/users/%d", f.alice.ID), fmt.Sprintf("/admin/users/%d", f.admin.ID), "/admin/resources", "/account/password"} {
		w := f.request("GET", path, f.adminToken, nil, false)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<!DOCTYPE html>") || strings.Contains(w.Body.String(), "页面暂时无法显示") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := f.request("GET", "/admin/users", f.aliceToken, nil, false)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = f.request("GET", "/api/admin/users", f.adminToken, nil, true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "PasswordHash") || strings.Contains(w.Body.String(), "$2a$") {
		t.Fatal("unsafe user list", w.Body.String())
	}
	w = f.request("POST", "/api/admin/users", f.adminToken, url.Values{"email": {"Carol@Example.com "}, "password": {"temporary-password"}, "quota": {"0"}, "manual": {"true"}}, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	u, err := f.st.GetUserByEmail(context.Background(), "carol@example.com")
	if err != nil || u == nil || u.InstanceQuota != 0 {
		t.Fatal(u, err)
	}
	initial, err := f.h.auth.Login(context.Background(), u.Email, "temporary-password")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.request("GET", "/", initial, nil, false); got.Code != 303 || got.Header().Get("Location") != "/account/password" {
		t.Fatalf("first login %d %v", got.Code, got.Header())
	}
	if got := f.request("GET", "/api/sessions", initial, nil, true); got.Code != 403 {
		t.Fatal(got.Code)
	}
	w = f.request("POST", "/api/account/password", initial, url.Values{"current_password": {"temporary-password"}, "new_password": {"carol-permanent-pass"}, "confirm_password": {"carol-permanent-pass"}}, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := f.request("GET", "/api/account", initial, nil, true); got.Code != 401 {
		t.Fatal("old token survived password change", got.Code)
	}
	resetPath := fmt.Sprintf("/api/admin/users/%d/password", u.ID)
	w = f.request("POST", resetPath, f.adminToken, url.Values{"password": {"reset-temporary-pass"}, "confirm_password": {"reset-temporary-pass"}, "manual": {"true"}}, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = f.h.auth.Login(context.Background(), u.Email, "carol-permanent-pass"); err == nil {
		t.Fatal("reset kept old password")
	}
	if got := f.request("GET", "/api/account", f.bobToken, nil, true); got.Code != 200 {
		t.Fatal("reset affected another user")
	}
	deletePath := fmt.Sprintf("/api/admin/users/%d/delete", u.ID)
	w = f.request("POST", deletePath, f.adminToken, url.Values{"confirm_email": {u.Email}}, true)
	if w.Code != 400 {
		t.Fatal("missing confirmation accepted")
	}
	if u, _ = f.st.GetUserByID(context.Background(), u.ID); u == nil || u.Status != "active" {
		t.Fatal("failed confirmation mutated user")
	}
	w = f.request("POST", deletePath, f.adminToken, url.Values{"confirm_email": {u.Email}, "confirm_delete": {"yes"}}, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if u, _ = f.st.GetUserByID(context.Background(), u.ID); u != nil {
		t.Fatal("user not removed")
	}
}

func TestEveryInstanceSurfaceRejectsOtherUsersAndAdmin(t *testing.T) {
	f := setupHTTP(t)
	s := &model.Session{ID: "sess_http_test", Name: "<script>alert(1)</script>", UserID: f.alice.ID, Status: model.SessionStopped, DesiredState: "stopped", ProfileDir: filepath.Join(t.TempDir(), "profile"), DownloadsDir: filepath.Join(t.TempDir(), "downloads")}
	if err := f.st.CreateSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	paths := []struct{ method, path string }{{"GET", "/api/sessions/%s"}, {"POST", "/api/sessions/%s/start"}, {"POST", "/api/sessions/%s/stop"}, {"DELETE", "/api/sessions/%s"}, {"GET", "/api/sessions/%s/files"}, {"POST", "/api/sessions/%s/files/upload"}, {"GET", "/api/sessions/%s/files/secret.txt"}, {"GET", "/api/sessions/%s/clipboard/pull"}, {"POST", "/api/sessions/%s/clipboard/push"}, {"GET", "/sessions/%s/view"}, {"GET", "/sessions/%s/novnc/websockify"}, {"GET", "/sessions/%s/audio"}, {"GET", "/sessions/%s/input"}, {"GET", "/sessions/%s/input/state"}, {"POST", "/sessions/%s/webrtc/offer"}}
	for _, token := range []string{f.bobToken, f.adminToken} {
		for _, p := range paths {
			w := f.request(p.method, fmt.Sprintf(p.path, s.ID), token, nil, true)
			if w.Code != 404 {
				t.Fatalf("%s %s returned %d", p.method, p.path, w.Code)
			}
		}
	}
	if got := f.request("GET", "/api/sessions", f.bobToken, nil, true); strings.Contains(got.Body.String(), s.ID) {
		t.Fatal("list leaked instance")
	}
	w := f.request("GET", "/api/sessions/"+s.ID, f.aliceToken, nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "http://example.com/api/sessions", nil)
	r.Header.Set("HX-Request", "true")
	r.AddCookie(f.csrf)
	cookieWriter := httptest.NewRecorder()
	f.h.auth.SetCookie(cookieWriter, r, f.aliceToken)
	for _, c := range cookieWriter.Result().Cookies() {
		r.AddCookie(c)
	}
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), s.Name) || !strings.Contains(w.Body.String(), "&lt;script&gt;") {
		t.Fatal("partial failed or XSS", w.Body.String())
	}
	if err := f.h.auth.SetUserStatus(context.Background(), f.admin.ID, f.alice.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if got := f.request("GET", "/api/sessions/"+s.ID, f.aliceToken, nil, true); got.Code != 401 {
		t.Fatal("disabled token accepted")
	}
	if sess, _ := f.st.GetSession(context.Background(), s.ID); sess == nil {
		t.Fatal("disable deleted instance")
	}
}

func TestHTTPLoginAndCSRF(t *testing.T) {
	f := setupHTTP(t)
	saved := f.csrf
	f.csrf = nil
	w := f.request("POST", "/api/login", "", url.Values{"username": {"admin"}, "password": {"admin-test-password"}}, false)
	if w.Code != 403 {
		t.Fatal("login without csrf accepted")
	}
	f.csrf = saved
	w = f.request("POST", "/api/login", "", url.Values{"username": {"admin"}, "password": {"wrong"}}, false)
	if w.Code != 401 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatal(w.Code, w.Header())
	}
	w = f.request("POST", "/api/login", "", url.Values{"username": {"admin"}, "password": {"admin-test-password"}}, false)
	if w.Code != 303 || len(w.Result().Cookies()) == 0 {
		t.Fatal(w.Code)
	}
	w = f.request("POST", "/api/logout", f.aliceToken, nil, false)
	if w.Code != 303 {
		t.Fatal(w.Code)
	}
	if got := f.request("GET", "/api/account", f.aliceToken, nil, true); got.Code != 401 {
		t.Fatal("logout token reusable")
	}
}
