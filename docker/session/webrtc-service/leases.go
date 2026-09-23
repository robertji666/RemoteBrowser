package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/pion/webrtc/v4"
)

// Media access expires independently of the browser tab or manager process.
// The manager renews these unguessable leases only while the login is valid.
func (s *server) handleLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Lease string `json:"lease"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil || len(body.Lease) != 32 {
		http.Error(w, "invalid lease", 400)
		return
	}
	var closing []*webrtc.PeerConnection
	found := false
	s.mu.Lock()
	for pc, ps := range s.peers {
		if ps.lease == body.Lease {
			found = true
			if r.Method == http.MethodDelete {
				closing = append(closing, pc)
			} else {
				ps.expiresAt = time.Now().Add(8 * time.Second)
			}
		}
	}
	s.mu.Unlock()
	for _, pc := range closing {
		s.closePeer(pc)
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *server) expireLeases(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			var closing []*webrtc.PeerConnection
			s.mu.Lock()
			for pc, ps := range s.peers {
				if !ps.expiresAt.IsZero() && !now.Before(ps.expiresAt) {
					closing = append(closing, pc)
				}
			}
			s.mu.Unlock()
			for _, pc := range closing {
				s.closePeer(pc)
			}
		}
	}
}
