package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/auth"
)

// A hijacked WebSocket must be closed explicitly: cancelling an HTTP context
// alone does not revoke an upgraded connection.
type guardedWriter struct {
	http.ResponseWriter
	mu     sync.Mutex
	conn   net.Conn
	closed bool
}

func (w *guardedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *guardedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return 0, context.Canceled
	}
	return w.ResponseWriter.Write(p)
}
func (w *guardedWriter) Flush() {
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *guardedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("connection cannot upgrade")
	}
	c, rw, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.mu.Lock()
	w.conn = c
	closed := w.closed
	w.mu.Unlock()
	if closed {
		_ = c.Close()
	}
	return c, rw, nil
}
func (w *guardedWriter) revoke() {
	w.mu.Lock()
	w.closed = true
	conn := w.conn
	w.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	} else {
		// Abort a download that is already blocked writing to a slow client.
		// Cancelling only the upstream request cannot interrupt that Write.
		controller := http.NewResponseController(w.ResponseWriter)
		_ = controller.SetWriteDeadline(time.Now())
		// Closing Request.Body waits for an in-flight Read's mutex. A client
		// that stalls an upload must not hold the deletion/user lock forever.
		_ = controller.SetReadDeadline(time.Now())
	}
}

func (h *Handler) guardAccess(next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	gw := &guardedWriter{ResponseWriter: w}
	// Proxy handlers rewrite URL and headers. Keep authentication's copy
	// immutable while the handler and revocation goroutine run concurrently.
	authRequest := r.Clone(ctx)
	requestBody := r.Body
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, stopCheck := context.WithTimeout(ctx, time.Second)
				u, err := h.auth.AuthenticateRequest(authRequest.Clone(checkCtx))
				stopCheck()
				if err != nil || u == nil || u.MustChangePassword {
					gw.revoke()
					cancel()
					if requestBody != nil {
						_ = requestBody.Close()
					}
					return
				}
			}
		}
	}()
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files/upload") {
		err := h.session.WithUserLock(ctx, auth.UserIDFromContext(r.Context()), func() error { next.ServeHTTP(gw, r.WithContext(ctx)); return nil })
		if err != nil {
			h.actionError(gw, r, err)
		}
		return
	}
	next.ServeHTTP(gw, r.WithContext(ctx))
}

// WebRTC media bypasses HTTP once negotiated. The session service therefore
// receives a short lease that only the manager can renew after authentication.
func (h *Handler) maintainMediaLease(r *http.Request, upstream, lease string) {
	client := &http.Client{Timeout: 2 * time.Second}
	update := func(ctx context.Context, method string) bool {
		b, _ := json.Marshal(map[string]string{"lease": lease})
		req, err := http.NewRequestWithContext(ctx, method, upstream+"/lease", bytes.NewReader(b))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		update(ctx, http.MethodDelete)
	}()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		checkCtx, stopCheck := context.WithTimeout(r.Context(), time.Second)
		u, err := h.auth.AuthenticateRequest(r.Clone(checkCtx))
		stopCheck()
		if err != nil || u == nil || u.MustChangePassword {
			return
		}
		if !update(r.Context(), http.MethodPost) {
			return
		}
	}
}
