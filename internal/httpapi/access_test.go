package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/auth"
	"github.com/robertji666/RemoteBrowser/internal/config"
	"github.com/robertji666/RemoteBrowser/internal/files"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type accessFixture struct {
	h           *Handler
	st          *store.Store
	admin, user *model.User
	token       string
}

func setupAccess(t *testing.T) *accessFixture {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.Migrate(); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("access-test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	admin := &model.User{Username: "admin", Role: model.RoleAdmin, Status: model.UserActive, AuthVersion: 1, PasswordHash: string(hash), InstanceQuota: 2}
	user := &model.User{Username: "access@example.test", Email: "access@example.test", Role: model.RoleUser, Status: model.UserActive, AuthVersion: 1, PasswordHash: string(hash), InstanceQuota: 2}
	for _, u := range []*model.User{admin, user} {
		if err = st.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	a := auth.New(&config.Config{CookieSecret: strings.Repeat("s", 32)}, st)
	token, err := a.Login(context.Background(), user.Email, "access-test-password")
	if err != nil {
		t.Fatal(err)
	}
	return &accessFixture{h: &Handler{auth: a}, st: st, admin: admin, user: user, token: token}
}
func (f *accessFixture) request(method, url string) *http.Request {
	r, _ := http.NewRequest(method, url, nil)
	r.AddCookie(&http.Cookie{Name: "rb_session", Value: f.token})
	return r
}
func (f *accessFixture) revoke(t *testing.T, kind string) {
	t.Helper()
	var err error
	switch kind {
	case "reset":
		_, err = f.h.auth.ResetUserPassword(context.Background(), f.admin.ID, f.user.ID, "replacement-access-password", true)
	case "disable":
		err = f.h.auth.SetUserStatus(context.Background(), f.admin.ID, f.user.ID, model.UserDisabled)
	case "logout":
		err = f.h.auth.Logout(context.Background(), f.request("POST", "http://localhost/api/logout"))
	case "delete":
		err = f.st.MarkUserDeleting(context.Background(), f.user.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestHijackedConnectionIsClosedAfterCredentialRevocation(t *testing.T) {
	for _, kind := range []string{"reset", "disable", "logout", "delete"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAccess(t)
			sess := &model.Session{ID: "sess_access_test", UserID: f.user.ID, Status: model.SessionRunning, DesiredState: "running"}
			if err := f.st.CreateSession(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			closed := make(chan struct{})
			stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer c.Close()
				defer close(closed)
				fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				_ = rw.Flush()
				_, _ = io.Copy(io.Discard, c)
			})
			srv := httptest.NewServer(f.h.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.h.guardAccess(stream, w, r) })))
			defer srv.Close()
			c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			fmt.Fprintf(c, "GET /sessions/test/input HTTP/1.1\r\nHost: localhost\r\nCookie: rb_session=%s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", f.token)
			reader := bufio.NewReader(c)
			line, err := reader.ReadString('\n')
			if err != nil || !strings.Contains(line, "101") {
				t.Fatalf("upgrade=%q %v", line, err)
			}
			for {
				line, err = reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if line == "\r\n" {
					break
				}
			}
			started := time.Now()
			f.revoke(t, kind)
			_ = c.SetReadDeadline(time.Now().Add(9 * time.Second))
			_, err = reader.ReadByte()
			if err == nil {
				t.Fatal("revoked socket sent data")
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				t.Fatal("socket remained open past revocation deadline")
			}
			if time.Since(started) >= 10*time.Second {
				t.Fatal("revocation exceeded 10 seconds")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("server retained hijacked connection")
			}
			current, err := f.st.GetSession(context.Background(), sess.ID)
			if err != nil || current == nil || current.Status != model.SessionRunning {
				t.Fatal("access revocation mutated persistent instance", current, err)
			}
		})
	}
}

func TestDownloadStopsWhenLoginIsRevoked(t *testing.T) {
	f := setupAccess(t)
	upstreamCancelled := make(chan struct{})
	upstreamStarted := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("first bytes\n"))
		w.(http.Flusher).Flush()
		close(upstreamStarted)
		<-r.Context().Done()
		close(upstreamCancelled)
	}))
	defer upstream.Close()
	fileService := files.NewService(f.st)
	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = fileService.ProxyDownload(r.Context(), upstream.URL, "large.bin", w)
	})
	srv := httptest.NewServer(f.h.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.h.guardAccess(stream, w, r) })))
	defer srv.Close()
	responseDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(f.request("GET", srv.URL+"/api/sessions/test/files/large.bin"))
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		responseDone <- err
	}()
	// The guard is installed before the first upstream byte; revocation must
	// cancel both the download response and the upstream request.
	select {
	case <-upstreamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("download never reached upstream")
	}
	f.revoke(t, "logout")
	select {
	case <-upstreamCancelled:
	case <-time.After(9 * time.Second):
		t.Fatal("upstream download retained revoked access")
	}
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("download response remained open")
	}
}

func TestStalledRequestBodyIsInterruptedOnRevocation(t *testing.T) {
	f := setupAccess(t)
	started, finished := make(chan struct{}), make(chan struct{})
	reader := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		_, _ = io.Copy(io.Discard, r.Body)
		close(finished)
	})
	srv := httptest.NewServer(f.h.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.h.guardAccess(reader, w, r) })))
	defer srv.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST /guard-body HTTP/1.1\r\nHost: localhost\r\nCookie: rb_session=%s\r\nContent-Length: 100000\r\n\r\nx", f.token)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("body read did not start")
	}
	f.revoke(t, "logout")
	select {
	case <-finished:
	case <-time.After(9 * time.Second):
		t.Fatal("stalled request retained its in-flight write lock after revocation")
	}
}

func TestMediaLeaseRenewsAndDeletesOnRevocation(t *testing.T) {
	f := setupAccess(t)
	lease := strings.Repeat("a", 32)
	renewed := make(chan struct{}, 4)
	deleted := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["lease"] != lease {
			http.Error(w, "bad lease", 400)
			return
		}
		if r.Method == http.MethodDelete {
			deleted <- struct{}{}
		} else {
			renewed <- struct{}{}
		}
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := f.request("POST", "http://localhost/offer").WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); f.h.maintainMediaLease(req, upstream.URL, lease) }()
	select {
	case <-renewed:
	case <-time.After(4 * time.Second):
		t.Fatal("valid lease was not renewed")
	}
	f.revoke(t, "disable")
	select {
	case <-deleted:
	case <-time.After(4 * time.Second):
		t.Fatal("revoked media lease not deleted")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lease renewal continued after revocation")
	}
}
func TestMediaLeaseStopsOnPeerMissingAndContextCancel(t *testing.T) {
	for _, kind := range []string{"missing", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAccess(t)
			var posts, deletes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
				} else {
					posts.Add(1)
				}
				http.NotFound(w, r)
			}))
			defer upstream.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				f.h.maintainMediaLease(f.request("POST", "http://localhost/offer").WithContext(ctx), upstream.URL, strings.Repeat("b", 32))
			}()
			if kind == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("lease goroutine leaked")
			}
			if deletes.Load() != 1 {
				t.Fatal("lease cleanup not attempted")
			}
			if kind == "missing" && posts.Load() != 1 {
				t.Fatal("missing lease should end after one failed renewal")
			}
		})
	}
}
