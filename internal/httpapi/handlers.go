package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/robertji666/RemoteBrowser/internal/auth"
	"github.com/robertji666/RemoteBrowser/internal/model"
)

type sessionResponse struct {
	ID             string              `json:"id"`
	Status         model.SessionStatus `json:"status"`
	LastActiveAt   *time.Time          `json:"lastActiveAt,omitempty"`
	CreatedAt      time.Time           `json:"createdAt"`
	ExpiredAt      *time.Time          `json:"expiredAt,omitempty"`
	Name           string              `json:"name"`
	LastError      string              `json:"lastError,omitempty"`
	ConnectedCount int                 `json:"connectedCount"`
}

func toSessionResponse(sess *model.Session) sessionResponse {
	return sessionResponse{
		ID:           sess.ID,
		Status:       sess.Status,
		LastActiveAt: sess.LastActiveAt,
		CreatedAt:    sess.CreatedAt,
		ExpiredAt:    sess.ExpiredAt,
		Name:         sess.Name, LastError: sess.LastError, ConnectedCount: sess.ConnectedCount,
	}
}

// LoginPage renders the login page
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	notice := r.URL.Query().Get("notice")
	if r.URL.Query().Get("expired") == "1" {
		notice = "登录已过期或已失效，请重新登录。"
	}
	h.render(w, r, "login.html", map[string]any{
		"Title": "登录", "Notice": notice,
	})
}

// Login handles login form submission
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, r, "login.html", map[string]any{
			"Title": "登录",
			"Error": "无效的请求",
		})
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	token, err := h.auth.Login(r.Context(), username, password)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		h.render(w, r, "login.html", map[string]any{
			"Title": "登录",
			"Error": "账号或密码错误，或账号不可用",
		})
		return
	}

	h.auth.SetCookie(w, r, token)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout clears the session cookie
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.Logout(r.Context(), r); err != nil {
		h.actionError(w, r, fmt.Errorf("退出未完成，请重试：%w", err))
		return
	}
	h.auth.ClearCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// DashboardPage renders the main dashboard
func (h *Handler) DashboardPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "dashboard.html", map[string]any{
		"Title": "我的浏览器实例", "RequestKey": requestKey(),
	})
}

// ListSessions returns the session list partial
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	sessions, err := h.session.ListSessions(r.Context(), userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		h.render(w, r, "session-list.html", map[string]any{
			"Sessions": sessions,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	responses := make([]sessionResponse, 0, len(sessions))
	for _, sess := range sessions {
		responses = append(responses, toSessionResponse(sess))
	}
	_ = json.NewEncoder(w).Encode(responses)
}

// CreateSession creates a new session
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = r.FormValue("request_key")
	}
	sess, err := h.session.CreateSessionWithOptions(r.Context(), userID, r.FormValue("name"), key)
	targetID := ""
	if sess != nil {
		targetID = sess.ID
	}
	h.auditAction(r, userID, "instance.create", targetID, "accepted", err)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	if wantsJSON(r) {
		jsonReply(w, http.StatusCreated, toSessionResponse(sess))
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Return updated list
	sessions, _ := h.session.ListSessions(r.Context(), userID)
	h.render(w, r, "session-list.html", map[string]any{
		"Sessions": sessions,
	})
}

// GetSession returns session details
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toSessionResponse(sess))
}

// StartSession starts a stopped session
func (h *Handler) StartSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.session.StartSession(r.Context(), id); err != nil {
		h.auditAction(r, auth.UserIDFromContext(r.Context()), "instance.start", id, "", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.auditAction(r, auth.UserIDFromContext(r.Context()), "instance.start", id, "success", nil)
	if wantsJSON(r) {
		jsonReply(w, 200, map[string]bool{"ok": true})
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	sessions, _ := h.session.ListSessions(r.Context(), userID)
	h.render(w, r, "session-list.html", map[string]any{
		"Sessions": sessions,
	})
}

// StopSession stops a running session
func (h *Handler) StopSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.session.StopSession(r.Context(), id); err != nil {
		h.auditAction(r, auth.UserIDFromContext(r.Context()), "instance.stop", id, "", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.auditAction(r, auth.UserIDFromContext(r.Context()), "instance.stop", id, "success", nil)
	if wantsJSON(r) {
		jsonReply(w, 200, map[string]bool{"ok": true})
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	sessions, _ := h.session.ListSessions(r.Context(), userID)
	h.render(w, r, "session-list.html", map[string]any{
		"Sessions": sessions,
	})
}

// DeleteSession deletes a session
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.session.DeleteSession(r.Context(), id); err != nil {
		h.auditAction(r, auth.UserIDFromContext(r.Context()), "instance.delete", id, "", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.auditAction(r, auth.UserIDFromContext(r.Context()), "instance.delete", id, "success", nil)
	if wantsJSON(r) {
		jsonReply(w, 200, map[string]bool{"ok": true})
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	sessions, _ := h.session.ListSessions(r.Context(), userID)
	h.render(w, r, "session-list.html", map[string]any{
		"Sessions": sessions,
	})
}

// SessionView renders the session view page
func (h *Handler) SessionView(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil {
		h.render(w, r, "error.html", map[string]any{
			"Icon":        "⚠️",
			"Title":       "错误",
			"Message":     err.Error(),
			"RedirectURL": "/",
			"ButtonText":  "返回管理面板",
		})
		return
	}
	if sess == nil {
		h.render(w, r, "error.html", map[string]any{
			"Icon":        "🔍",
			"Title":       "会话不存在",
			"Message":     "请求的会话未找到或已被删除",
			"RedirectURL": "/",
			"ButtonText":  "返回管理面板",
		})
		return
	}

	if sess.Status != model.SessionRunning && sess.Status != model.SessionDisconnected {
		h.render(w, r, "error.html", map[string]any{
			"Icon":        "⏳",
			"Title":       "会话未就绪",
			"Message":     fmt.Sprintf("当前状态: %s，请稍后重试", sess.Status),
			"RedirectURL": "/",
			"ButtonText":  "返回管理面板",
		})
		return
	}

	h.render(w, r, "session-view.html", map[string]any{
		"Title": "浏览器实例 " + sess.Name, "Bare": true,
		"Session": sess,
	})
}

// ProxyNoVNC proxies WebSocket and HTTP requests to the session container
func (h *Handler) ProxyNoVNC(w http.ResponseWriter, r *http.Request) {
	h.proxy.ServeHTTP(w, r)
}

// ProxyAudio proxies the raw PCM WebSocket stream from the session container.
func (h *Handler) ProxyAudio(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil || sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	target, err := url.Parse(h.resolveContainerURL(r.Context(), sess, "6081"))
	if err != nil {
		http.Error(w, "invalid audio upstream", http.StatusInternalServerError)
		return
	}

	r.URL.Path = "/stream"
	r.URL.RawPath = ""
	go h.session.UpdateLastActive(context.Background(), id)

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "audio proxy error: "+err.Error(), http.StatusBadGateway)
	}
	if isWebSocketUpgrade(r) {
		if err := h.session.ConnectionOpened(r.Context(), id); err != nil {
			http.Error(w, "instance connection unavailable", http.StatusServiceUnavailable)
			return
		}
		defer h.session.ConnectionClosed(context.Background(), id)
	}
	proxy.ServeHTTP(w, r)
}

// ProxyInput proxies mouse, keyboard, and clipboard WebSocket input.
func (h *Handler) ProxyInput(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil || sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	target, err := url.Parse(h.resolveContainerURL(r.Context(), sess, "6084"))
	if err != nil {
		http.Error(w, "invalid input upstream", http.StatusInternalServerError)
		return
	}

	if strings.HasSuffix(r.URL.Path, "/state") {
		r.URL.Path = "/state"
	} else {
		r.URL.Path = "/ws"
	}
	r.URL.RawPath = ""
	go h.session.UpdateLastActive(context.Background(), id)

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "input proxy error: "+err.Error(), http.StatusBadGateway)
	}
	if isWebSocketUpgrade(r) {
		if err := h.session.ConnectionOpened(r.Context(), id); err != nil {
			http.Error(w, "instance connection unavailable", http.StatusServiceUnavailable)
			return
		}
		defer h.session.ConnectionClosed(context.Background(), id)
	}
	proxy.ServeHTTP(w, r)
}

// ProxyWebRTC proxies WebRTC signaling requests to the session container.
func (h *Handler) ProxyWebRTC(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	path := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/sessions/%s/webrtc", id))
	if path != "/offer" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil || sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	target, err := url.Parse(h.resolveContainerURL(r.Context(), sess, "6082"))
	if err != nil {
		http.Error(w, "invalid webrtc upstream", http.StatusInternalServerError)
		return
	}

	lease := requestKey()
	r.Header.Set("X-RB-Access-Lease", lease)
	r.URL.Path = path
	r.URL.RawPath = ""
	go h.session.UpdateLastActive(context.Background(), id)

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode == http.StatusOK {
			go func() {
				if err := h.session.ConnectionOpened(context.Background(), id); err != nil {
					// Cancel immediately to delete the just-created backend lease
					// without renewing media that could not be accounted for.
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					h.maintainMediaLease(r.Clone(ctx), target.String(), lease)
					return
				}
				defer h.session.ConnectionClosed(context.Background(), id)
				h.maintainMediaLease(r.Clone(context.Background()), target.String(), lease)
			}()
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "webrtc proxy error: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// ClipboardPush pushes local clipboard to remote
func (h *Handler) ClipboardPush(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	content := r.FormValue("content")
	h.clipboard.Push(id, content)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

// ClipboardPull pulls remote clipboard to local
func (h *Handler) ClipboardPull(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	content := h.clipboard.Pull(id)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"content":%q}`, content)
}

func (h *Handler) resolveContainerURL(ctx context.Context, sess *model.Session, port string) string {
	url := fmt.Sprintf("http://%s:%s", sess.ContainerName, port)
	info, err := h.docker.InspectContainer(ctx, sess.ContainerName)
	if err == nil && len(info.HostPorts) > 0 {
		if hostPort, ok := info.HostPorts[port+"/tcp"]; ok {
			url = fmt.Sprintf("http://localhost:%s", hostPort)
		}
	}
	return url
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// ListFiles lists files in a session
func (h *Handler) ListFiles(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil || sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	containerURL := h.resolveContainerURL(r.Context(), sess, "8081")
	files, err := h.files.ListFiles(r.Context(), containerURL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		viewFiles := make([]map[string]any, 0, len(files))
		for _, f := range files {
			item := make(map[string]any, len(f)+1)
			for k, v := range f {
				item[k] = v
			}
			if name, ok := f["name"].(string); ok {
				item["urlName"] = url.PathEscape(name)
			}
			item["sizeDisplay"] = formatFileSize(f["size"])
			viewFiles = append(viewFiles, item)
		}
		h.render(w, r, "file-list.html", map[string]any{
			"Files":     viewFiles,
			"SessionID": id,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(files)
}

func formatFileSize(value any) string {
	var bytes float64
	switch size := value.(type) {
	case float64:
		bytes = size
	case int64:
		bytes = float64(size)
	case int:
		bytes = float64(size)
	default:
		return "-"
	}
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.1f MB", bytes/(1024*1024))
	}
	return fmt.Sprintf("%.1f KB", bytes/1024)
}

// UploadFile uploads a file to a session
func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Allow a small amount of multipart framing in addition to the configured
	// file limit. ParseMultipartForm's argument controls memory use, while
	// MaxBytesReader enforces the request size.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUploadSize+(1<<20))
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
	if header.Size > h.maxUploadSize {
		http.Error(w, "file exceeds upload size limit", http.StatusRequestEntityTooLarge)
		return
	}

	name, err := h.files.SanitizeFilename(header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil || sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	containerURL := h.resolveContainerURL(r.Context(), sess, "8081")
	if err := h.files.ProxyUpload(r.Context(), containerURL, file, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = h.files.RecordFile(r.Context(), id, name, header.Size)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

// DownloadFile downloads a file from a session
func (h *Handler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	name := chi.URLParam(r, "name")
	name, err := h.files.SanitizeFilename(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess, err := h.session.GetSession(r.Context(), id)
	if err != nil || sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	containerURL := h.resolveContainerURL(r.Context(), sess, "8081")
	if err := h.files.ProxyDownload(r.Context(), containerURL, name, w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
