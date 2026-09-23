package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		name, proto string
		wantHSTS    bool
	}{
		{name: "plain HTTP"},
		{name: "proxied HTTPS", proto: "https", wantHSTS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			if tc.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			w := httptest.NewRecorder()
			SecurityHeaders(next).ServeHTTP(w, r)
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options=%q", got)
			}
			if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
				t.Fatalf("X-Frame-Options=%q", got)
			}
			if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'self'") {
				t.Fatalf("Content-Security-Policy=%q", got)
			}
			if got := w.Header().Get("Strict-Transport-Security"); (got != "") != tc.wantHSTS {
				t.Fatalf("Strict-Transport-Security=%q", got)
			}
		})
	}
}
