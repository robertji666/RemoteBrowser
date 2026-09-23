package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/proxy"
	"github.com/robertji666/RemoteBrowser/internal/session"
)

// These tests use actual local HTTP/TCP connections and the real handlers,
// authentication, connection accounting and SQLite. The Docker inspect API
// and audio/WebRTC senders are simulated; they do not exercise media codecs.
func setupCountedProxy(t *testing.T, upstream string) (*accessFixture, *model.Session, *httptest.Server) {
	t.Helper()
	return setupCountedProxyWithInspect(t, upstream, nil)
}

func setupCountedProxyWithInspect(t *testing.T, upstream string, beforeInspect func(*http.Request)) (*accessFixture, *model.Session, *httptest.Server) {
	t.Helper()
	f := setupAccess(t)
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/rb-counted/json") {
			http.NotFound(w, r)
			return
		}
		if beforeInspect != nil {
			beforeInspect(r)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Id":"counted","Name":"rb-counted","State":{"Running":true,"Status":"running"},"Config":{"Labels":{}},"HostConfig":{"RestartPolicy":{"Name":"unless-stopped"}},"NetworkSettings":{"Ports":{"6080/tcp":[{"HostIp":"127.0.0.1","HostPort":%q}],"6081/tcp":[{"HostIp":"127.0.0.1","HostPort":%q}],"6082/tcp":[{"HostIp":"127.0.0.1","HostPort":%q}],"6084/tcp":[{"HostIp":"127.0.0.1","HostPort":%q}]}}}`, u.Port(), u.Port(), u.Port(), u.Port())
	}))
	t.Cleanup(engine.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(engine.URL, "http://"))
	t.Setenv("DOCKER_API_VERSION", "1.47")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	f.h.docker, err = docker.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.h.docker.Close() })
	dir := t.TempDir()
	f.h.session = session.NewService(f.st, absentRuntime{}, 3, 0, dir, dir, 0, 0, 0, "count-test", "", "", "", "", "", "", "", "", "", "", "", "", false)
	f.h.proxy = proxy.New(f.h.session, f.h.docker)
	sess := &model.Session{ID: "sess_counted", UserID: f.user.ID, ContainerName: "rb-counted", Status: model.SessionRunning, DesiredState: "running"}
	if err := f.st.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(f.h.auth.Middleware)
	r.Get("/sessions/{id}/audio", f.h.owned(f.h.ProxyAudio))
	r.Get("/sessions/{id}/input", f.h.owned(f.h.ProxyInput))
	r.Get("/sessions/{id}/novnc/*", f.h.owned(f.h.ProxyNoVNC))
	r.Post("/sessions/{id}/webrtc/offer", f.h.owned(f.h.ProxyWebRTC))
	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		_ = f.h.auth.Logout(context.Background(), f.request("POST", srv.URL+"/api/logout"))
		deadline := time.Now().Add(4 * time.Second)
		for f.h.session.ConnectedCount(sess.ID) != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = f.h.session.Shutdown(ctx)
	})
	return f, sess, srv
}

func TestCancelledTransportOpenDoesNotDecrementAnotherConnection(t *testing.T) {
	for _, path := range []string{"audio", "input", "novnc/websockify"} {
		t.Run(path, func(t *testing.T) { testCancelledTransportOpen(t, path) })
	}
}

func testCancelledTransportOpen(t *testing.T, path string) {
	inspectStarted := make(chan struct{})
	f, sess, srv := setupCountedProxyWithInspect(t, "http://127.0.0.1:1", func(r *http.Request) {
		close(inspectStarted)
		<-r.Context().Done()
	})
	// Represent an already-open input/noVNC connection for the same instance.
	if err := f.h.session.ConnectionOpened(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	defer f.h.session.ConnectionClosed(context.Background(), sess.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := f.request("GET", srv.URL+"/sessions/"+sess.ID+"/"+path).WithContext(ctx)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Config.Handler.ServeHTTP(w, r)
	}()
	select {
	case <-inspectStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("transport request never reached Docker inspect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled transport request did not finish")
	}
	if count := f.h.session.ConnectedCount(sess.ID); count != 1 {
		t.Fatalf("cancelled transport open changed an unrelated established connection: got %d, want 1", count)
	}
}

func TestStoppedTransportOpenDoesNotDecrementAnotherConnection(t *testing.T) {
	for _, path := range []string{"audio", "input", "novnc/websockify"} {
		t.Run(path, func(t *testing.T) {
			var stop func()
			f, sess, srv := setupCountedProxyWithInspect(t, "http://127.0.0.1:1", func(r *http.Request) { stop() })
			stop = func() {
				if err := f.st.UpdateSessionState(context.Background(), sess.ID, model.SessionStopped, "stopped", ""); err != nil {
					t.Error(err)
				}
			}
			if err := f.h.session.ConnectionOpened(context.Background(), sess.ID); err != nil {
				t.Fatal(err)
			}
			defer f.h.session.ConnectionClosed(context.Background(), sess.ID)
			r := f.request("GET", srv.URL+"/sessions/"+sess.ID+"/"+path)
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			w := httptest.NewRecorder()
			srv.Config.Handler.ServeHTTP(w, r)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatal("concurrently stopped instance accepted transport", w.Code)
			}
			if count := f.h.session.ConnectedCount(sess.ID); count != 1 {
				t.Fatalf("rejected open consumed another connection: got %d, want 1", count)
			}
		})
	}
}

func TestWebRTCFailedConnectionRegistrationDeletesLeaseWithoutDecrement(t *testing.T) {
	offerStarted, offerContinue := make(chan struct{}), make(chan struct{})
	deleted := make(chan struct{}, 1)
	var renewed atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/offer":
			close(offerStarted)
			<-offerContinue
			fmt.Fprint(w, `{"type":"answer","sdp":"simulated"}`)
		case "/lease":
			if r.Method == http.MethodDelete {
				deleted <- struct{}{}
			} else {
				renewed.Add(1)
			}
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	f, sess, srv := setupCountedProxy(t, upstream.URL)
	if err := f.h.session.ConnectionOpened(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	defer f.h.session.ConnectionClosed(context.Background(), sess.ID)
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(f.request("POST", srv.URL+"/sessions/"+sess.ID+"/webrtc/offer"))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				err = fmt.Errorf("signaling status = %d", resp.StatusCode)
			}
		}
		done <- err
	}()
	select {
	case <-offerStarted:
	case <-time.After(3 * time.Second):
		close(offerContinue)
		t.Fatal("offer did not reach upstream")
	}
	err := f.st.UpdateSessionState(context.Background(), sess.ID, model.SessionStopped, "stopped", "")
	close(offerContinue)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("offer response did not finish")
	}
	select {
	case <-deleted:
	case <-time.After(3 * time.Second):
		t.Fatal("unregistered media lease was not deleted immediately")
	}
	if renewed.Load() != 0 || f.h.session.ConnectedCount(sess.ID) != 1 {
		t.Fatal("failed media registration renewed lease or consumed another connection")
	}
}

func awaitConnectionCount(t *testing.T, f *accessFixture, id string, expected int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if f.h.session.ConnectedCount(id) == expected {
			sess, err := f.h.session.GetSession(context.Background(), id)
			if err != nil || sess == nil || sess.ConnectedCount != expected {
				t.Fatalf("service response count: %+v %v; want %d", sess, err, expected)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection count = %d; want %d", f.h.session.ConnectedCount(id), expected)
}

func TestAudioProxyConnectionCountReturnsToZero(t *testing.T) {
	for _, ending := range []string{"client-close", "logout", "upstream-reject"} {
		t.Run(ending, func(t *testing.T) {
			upstreamEnded := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if ending == "upstream-reject" {
					http.Error(w, "test sender unavailable", http.StatusServiceUnavailable)
					return
				}
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				defer func() { upstreamEnded <- struct{}{} }()
				fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				_ = rw.Flush()
				_, _ = io.Copy(io.Discard, conn)
			}))
			defer upstream.Close()
			f, sess, srv := setupCountedProxy(t, upstream.URL)
			conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(9 * time.Second))
			fmt.Fprintf(conn, "GET /sessions/%s/audio HTTP/1.1\r\nHost: localhost\r\nCookie: rb_session=%s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n", sess.ID, f.token)
			reader := bufio.NewReader(conn)
			resp, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			if ending == "upstream-reject" {
				if resp.StatusCode != http.StatusServiceUnavailable {
					t.Fatal("unexpected failure status", resp.StatusCode)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				awaitConnectionCount(t, f, sess.ID, 0)
				return
			}
			if resp.StatusCode != http.StatusSwitchingProtocols {
				t.Fatal("audio did not upgrade", resp.StatusCode)
			}
			awaitConnectionCount(t, f, sess.ID, 1)
			if ending == "logout" {
				f.revoke(t, "logout")
				if _, err := reader.ReadByte(); err == nil {
					t.Fatal("revoked audio socket remained readable")
				} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
					t.Fatal("revoked audio socket remained open")
				}
			} else {
				_ = conn.Close()
			}
			awaitConnectionCount(t, f, sess.ID, 0)
			select {
			case <-upstreamEnded:
			case <-time.After(time.Second):
				t.Fatal("audio upstream connection leaked")
			}
		})
	}
}

func TestWebRTCProxyLeaseConnectionCountReturnsToZero(t *testing.T) {
	for _, ending := range []string{"peer-gone", "logout", "offer-reject"} {
		t.Run(ending, func(t *testing.T) {
			var renewed, deleted atomic.Int32
			var peerGone atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/offer":
					if r.Header.Get("X-RB-Access-Lease") == "" {
						http.Error(w, "no access lease", 400)
						return
					}
					if ending == "offer-reject" {
						http.Error(w, "test negotiation failure", 400)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"type":"answer","sdp":"simulated"}`)
				case "/lease":
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["lease"] == "" {
						http.Error(w, "bad lease", 400)
						return
					}
					if r.Method == http.MethodDelete {
						deleted.Add(1)
					} else {
						renewed.Add(1)
						if peerGone.Load() {
							http.NotFound(w, r)
							return
						}
					}
					w.WriteHeader(200)
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			f, sess, srv := setupCountedProxy(t, upstream.URL)
			resp, err := http.DefaultClient.Do(f.request("POST", srv.URL+"/sessions/"+sess.ID+"/webrtc/offer"))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if ending == "offer-reject" {
				if resp.StatusCode != 400 {
					t.Fatal("unexpected failure status", resp.StatusCode)
				}
				awaitConnectionCount(t, f, sess.ID, 0)
				if renewed.Load() != 0 || deleted.Load() != 0 {
					t.Fatal("failed negotiation created a media lease")
				}
				return
			}
			if resp.StatusCode != 200 {
				t.Fatal("offer failed", resp.StatusCode)
			}
			// Signaling has ended, but the negotiated media lease is still live.
			awaitConnectionCount(t, f, sess.ID, 1)
			deadline := time.Now().Add(5 * time.Second)
			for renewed.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if renewed.Load() == 0 {
				t.Fatal("live media lease was not renewed")
			}
			awaitConnectionCount(t, f, sess.ID, 1)
			if ending == "logout" {
				f.revoke(t, "logout")
			} else {
				peerGone.Store(true)
			}
			awaitConnectionCount(t, f, sess.ID, 0)
			if deleted.Load() != 1 {
				t.Fatal("media lease was not cleaned exactly once", deleted.Load())
			}
		})
	}
}
