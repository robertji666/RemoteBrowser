package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultMaxUploadSize = 100 << 20

func safeFilename(name string) (string, bool) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '\x00') {
		return "", false
	}
	if strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return "", false
	}
	return name, true
}

func parsePositiveInt64(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("value must be positive")
	}
	return value, nil
}

func main() {
	port := os.Getenv("FILE_SERVICE_PORT")
	if port == "" {
		port = os.Getenv("RB_FILE_SERVICE_PORT")
	}
	if port == "" {
		port = "8081"
	}
	downloadsDir := os.Getenv("RB_DOWNLOADS_DIR")
	if downloadsDir == "" {
		downloadsDir = "/home/rbuser/Downloads"
	}
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "create downloads dir: %v\n", err)
		os.Exit(1)
	}
	maxUploadSize := int64(defaultMaxUploadSize)
	if raw := os.Getenv("RB_MAX_UPLOAD_SIZE"); raw != "" {
		parsed, err := parsePositiveInt64(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid RB_MAX_UPLOAD_SIZE: %v\n", err)
			os.Exit(1)
		}
		maxUploadSize = parsed
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		entries, err := os.ReadDir(downloadsDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("["))
		wrote := 0
		for _, entry := range entries {
			if entry.IsDir() || strings.HasSuffix(entry.Name(), ".crdownload") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if wrote > 0 {
				w.Write([]byte(","))
			}
			wrote++
			fmt.Fprintf(w, `{"name":%q,"size":%d,"modified":%q}`,
				entry.Name(), info.Size(), info.ModTime().Format(time.RFC3339))
		}
		w.Write([]byte("]"))
	})

	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize+(1<<20))
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		if header.Size > maxUploadSize {
			http.Error(w, "file exceeds upload size limit", http.StatusRequestEntityTooLarge)
			return
		}

		name, ok := safeFilename(header.Filename)
		if !ok {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return
		}

		dst := filepath.Join(downloadsDir, name)
		f, err := os.Create(dst)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		if _, err := io.Copy(f, file); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name, ok := safeFilename(strings.TrimPrefix(r.URL.Path, "/download/"))
		if !ok {
			http.Error(w, "invalid filename", http.StatusBadRequest)
			return
		}
		src := filepath.Join(downloadsDir, name)
		if _, err := os.Stat(src); err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		http.ServeFile(w, r, src)
	})

	fmt.Printf("File service listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "file service error: %v\n", err)
		os.Exit(1)
	}
}
