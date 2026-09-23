package files

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProxyDownloadUsesBrowserSafeContentDisposition(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="下载.txt"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	service := NewService(nil)
	if err := service.ProxyDownload(context.Background(), upstream.URL, "下载.txt", rec); err != nil {
		t.Fatalf("ProxyDownload returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	want := "attachment; filename*=utf-8''%E4%B8%8B%E8%BD%BD.txt"
	if got := rec.Header().Get("Content-Disposition"); got != want {
		t.Fatalf("Content-Disposition = %q, want %q", got, want)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestFileListingStopsWhenRequestIsCancelled(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) }))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := NewService(nil).ListFiles(ctx, upstream.URL); result <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("list request did not reach upstream")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("expected cancelled request", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listing retained cancelled request")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream listing context remained active")
	}
}

func TestSanitizeFilenameRejectsPathTraversal(t *testing.T) {
	service := NewService(nil)
	for _, name := range []string{"", ".", "..", "..\\secret"} {
		if _, err := service.SanitizeFilename(name); err == nil {
			t.Errorf("SanitizeFilename(%q) accepted unsafe name", name)
		}
	}
	if got, err := service.SanitizeFilename("../secret"); err != nil || got != "secret" {
		t.Errorf("SanitizeFilename(../secret) = %q, %v; want basename secret", got, err)
	}
	for _, name := range []string{"report.txt", "中文 文件.txt"} {
		if got, err := service.SanitizeFilename(name); err != nil || got != name {
			t.Errorf("SanitizeFilename(%q) = %q, %v", name, got, err)
		}
	}
}
