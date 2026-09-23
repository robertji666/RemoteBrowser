package files

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
)

type Service struct {
	store *store.Store
}

func NewService(st *store.Store) *Service {
	return &Service{store: st}
}

func (s *Service) ListFiles(ctx context.Context, containerURL string) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, containerURL+"/files", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list files failed: %s", resp.Status)
	}
	var files []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Service) ProxyUpload(ctx context.Context, containerURL string, file io.Reader, filename string) error {
	reader, pipeWriter := io.Pipe()
	defer reader.Close()
	multipartWriter := multipart.NewWriter(pipeWriter)
	go func() {
		part, err := multipartWriter.CreateFormFile("file", filename)
		if err == nil {
			_, err = io.Copy(part, file)
		}
		if closeErr := multipartWriter.Close(); err == nil {
			err = closeErr
		}
		_ = pipeWriter.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, containerURL+"/upload", reader)
	if err != nil {
		_ = reader.Close()
		return err
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload failed: %s", resp.Status)
	}
	return nil
}

func (s *Service) ProxyDownload(ctx context.Context, containerURL, filename string, w http.ResponseWriter) error {
	proxyURL := containerURL + "/download/" + url.PathEscape(filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		if strings.EqualFold(k, "Content-Disposition") {
			continue
		}
		w.Header()[k] = v
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

func (s *Service) RecordFile(ctx context.Context, sessionID, name string, sizeBytes int64) error {
	if s.store == nil {
		return fmt.Errorf("store not initialized")
	}
	return s.store.CreateSessionFile(ctx, &model.SessionFile{
		SessionID: sessionID,
		Name:      name,
		SizeBytes: sizeBytes,
	})
}

func (s *Service) SanitizeFilename(name string) (string, error) {
	name = filepath.Base(name)
	if strings.Contains(name, "..") || name == "" || name == "." {
		return "", fmt.Errorf("invalid filename")
	}
	return name, nil
}
