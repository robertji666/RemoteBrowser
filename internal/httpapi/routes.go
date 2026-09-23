package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/robertji666/RemoteBrowser/internal/auth"
	"github.com/robertji666/RemoteBrowser/internal/clipboard"
	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/files"
	"github.com/robertji666/RemoteBrowser/internal/proxy"
	"github.com/robertji666/RemoteBrowser/internal/session"
	"github.com/robertji666/RemoteBrowser/internal/view"
)

type Handler struct {
	auth          *auth.Auth
	session       *session.Service
	proxy         *proxy.Proxy
	clipboard     *clipboard.Service
	files         *files.Service
	view          *view.View
	docker        *docker.Client
	maxUploadSize int64
}

func NewHandler(auth *auth.Auth, session *session.Service, proxy *proxy.Proxy, clipboard *clipboard.Service, files *files.Service, view *view.View, docker *docker.Client, maxUploadSize int64) *Handler {
	return &Handler{
		auth:          auth,
		session:       session,
		proxy:         proxy,
		clipboard:     clipboard,
		files:         files,
		view:          view,
		docker:        docker,
		maxUploadSize: maxUploadSize,
	}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(h.register)
}

func (h *Handler) register(r chi.Router) {
	r.Use(h.auth.CSRFMiddleware)
	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

	// Public routes
	r.Get("/login", h.LoginPage)
	r.With(h.auth.LoginHTTPMiddleware).Post("/api/login", h.Login)

	// Protected routes
	r.Group(func(r chi.Router) {
		r.Use(h.auth.Middleware)
		r.Post("/api/logout", h.Logout)
		r.Get("/account/password", h.PasswordPage)
		r.Post("/api/account/password", h.ChangePassword)
		r.Get("/api/account", h.CurrentAccount)
		r.Group(func(r chi.Router) {
			r.Use(h.auth.RequirePasswordChanged)

			r.Get("/", h.DashboardPage)
			r.Group(func(r chi.Router) {
				r.Use(h.requireAdmin)
				r.Get("/admin/users", h.UsersPage)
				r.Get("/admin/users/{userID}", h.UserPage)
				r.Get("/admin/resources", h.ResourcesPage)
				r.Get("/api/admin/users", h.ListUsers)
				r.Post("/api/admin/users", h.CreateUser)
				r.Post("/api/admin/users/{userID}/quota", h.UpdateQuota)
				r.Post("/api/admin/users/{userID}/password", h.ResetUserPassword)
				r.Post("/api/admin/users/{userID}/resend", h.ResendWelcome)
				r.Post("/api/admin/users/{userID}/status", h.UpdateUserStatus)
				r.Post("/api/admin/users/{userID}/delete", h.DeleteUser)
				r.Post("/api/admin/mail/test", h.TestMail)
			})

			// Session API
			r.Get("/api/sessions", h.ListSessions)
			r.Post("/api/sessions", h.CreateSession)
			r.Method(http.MethodGet, "/api/sessions/{id}", h.owned(h.GetSession))
			r.Method(http.MethodPost, "/api/sessions/{id}/start", h.owned(h.StartSession))
			r.Method(http.MethodPost, "/api/sessions/{id}/stop", h.owned(h.StopSession))
			r.Method(http.MethodDelete, "/api/sessions/{id}", h.owned(h.DeleteSession))

			// Clipboard API
			r.Method(http.MethodPost, "/api/sessions/{id}/clipboard/push", h.owned(h.ClipboardPush))
			r.Method(http.MethodGet, "/api/sessions/{id}/clipboard/pull", h.owned(h.ClipboardPull))

			// File API
			r.Method(http.MethodGet, "/api/sessions/{id}/files", h.owned(h.ListFiles))
			r.Method(http.MethodPost, "/api/sessions/{id}/files/upload", h.owned(h.UploadFile))
			r.Method(http.MethodGet, "/api/sessions/{id}/files/{name}", h.owned(h.DownloadFile))

			// Session view and proxy
			r.Method(http.MethodGet, "/sessions/{id}/view", h.owned(h.SessionView))
			r.HandleFunc("/sessions/{id}/novnc/*", h.owned(h.ProxyNoVNC))
			r.HandleFunc("/sessions/{id}/audio", h.owned(h.ProxyAudio))
			r.HandleFunc("/sessions/{id}/input", h.owned(h.ProxyInput))
			r.HandleFunc("/sessions/{id}/input/*", h.owned(h.ProxyInput))
			r.HandleFunc("/sessions/{id}/webrtc/*", h.owned(h.ProxyWebRTC))
		})
	})
}

func (h *Handler) owned(next http.HandlerFunc) http.HandlerFunc {
	return h.requireSessionOwner(next).ServeHTTP
}

func (h *Handler) requireSessionOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := h.session.GetSession(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "failed to load session", http.StatusInternalServerError)
			return
		}
		if sess == nil || sess.UserID != auth.UserIDFromContext(r.Context()) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		h.guardAccess(next, w, r)
	})
}
