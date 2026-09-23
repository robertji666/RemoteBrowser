package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestWebRTCProxyDoesNotExposeInternalDiagnosticsOrLeases(t *testing.T) {
	// No dependencies: rejection must happen before accessing session metadata,
	// inspecting Docker, or making any request to the internal signaling service.
	h := &Handler{}
	for _, path := range []string{"/stats", "/lease", "/healthz", "/offer/../stats"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			r := httptest.NewRequest(method, "/sessions/sess_test/webrtc"+path, nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("id", "sess_test")
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
			w := httptest.NewRecorder()
			h.ProxyWebRTC(w, r)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s returned %d", method, path, w.Code)
			}
		}
	}
}
