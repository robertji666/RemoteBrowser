package main

import (
	"encoding/json"
	"net/http"
)

// handleStats is exposed only on the isolated instance network. The manager
// proxy allowlist does not forward this diagnostic endpoint to users.
func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	count := len(s.peers)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		PeerCount int `json:"peerCount"`
	}{PeerCount: count})
}
