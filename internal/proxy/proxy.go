package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/session"
)

type Proxy struct {
	sessionService *session.Service
	dockerClient   *docker.Client
	upstreams      map[string]string // session_id -> container URL
	mu             sync.RWMutex
}

func New(sessionService *session.Service, dockerClient *docker.Client) *Proxy {
	return &Proxy{
		sessionService: sessionService,
		dockerClient:   dockerClient,
		upstreams:      make(map[string]string),
	}
}

func (p *Proxy) Register(sessionID, containerURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.upstreams[sessionID] = containerURL
}

func (p *Proxy) Unregister(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.upstreams, sessionID)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if sessionID == "" {
		http.Error(w, "session ID required", http.StatusBadRequest)
		return
	}

	// Container IPs and published ports change after replacement. Resolve each
	// request rather than caching an endpoint for the lifetime of the manager.
	var upstream string
	{
		// Try to resolve from session service
		sess, err := p.sessionService.GetSession(r.Context(), sessionID)
		if err != nil || sess == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		// Resolve container IP via docker inspect
		info, err := p.dockerClient.InspectContainer(r.Context(), sess.ContainerName)
		if err != nil {
			http.Error(w, "container status unavailable", http.StatusServiceUnavailable)
			return
		}
		if !info.Running {
			http.Error(w, "instance is not running", http.StatusConflict)
			return
		}
		if hostPort := info.HostPorts["6080/tcp"]; hostPort != "" {
			upstream = fmt.Sprintf("http://localhost:%s", hostPort)
		} else if info.IP == "" {
			// Fallback to container name (works within Docker network on Linux)
			upstream = fmt.Sprintf("http://%s:6080", sess.ContainerName)
		} else {
			upstream = fmt.Sprintf("http://%s:6080", info.IP)
		}
	}

	target, err := url.Parse(upstream)
	if err != nil {
		http.Error(w, "invalid upstream", http.StatusInternalServerError)
		return
	}

	// Strip the /sessions/{id}/novnc prefix
	path := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/sessions/%s/novnc", sessionID))
	if path == "" {
		path = "/"
	}
	r.URL.Path = path
	r.URL.RawPath = ""

	// Update last active time (use background context to outlive the request)
	go p.sessionService.UpdateLastActive(context.Background(), sessionID)

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "proxy error: "+err.Error(), http.StatusBadGateway)
	}
	if isWebSocketUpgrade(r) {
		if err := p.sessionService.ConnectionOpened(r.Context(), sessionID); err != nil {
			http.Error(w, "instance connection unavailable", http.StatusServiceUnavailable)
			return
		}
		defer p.sessionService.ConnectionClosed(context.Background(), sessionID)
	}
	proxy.ServeHTTP(w, r)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}
