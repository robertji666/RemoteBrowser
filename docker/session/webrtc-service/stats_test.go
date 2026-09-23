package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pion/webrtc/v4"
)

func TestStatsContainsOnlyPeerCount(t *testing.T) {
	s := &server{peers: make(map[*webrtc.PeerConnection]*peerSession)}
	for _, count := range []int{0, 2} {
		for len(s.peers) < count {
			s.peers[&webrtc.PeerConnection{}] = &peerSession{lease: "private-access-lease"}
		}
		w := httptest.NewRecorder()
		s.handleStats(w, httptest.NewRequest(http.MethodGet, "/stats", nil))
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("statistics can be cached")
		}
		var body map[string]int
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 1 || body["peerCount"] != count {
			t.Fatalf("unexpected public data: %s", w.Body.String())
		}
	}
}

func TestStatsRejectsNonGET(t *testing.T) {
	s := &server{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead} {
		w := httptest.NewRecorder()
		s.handleStats(w, httptest.NewRequest(method, "/stats", nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("%s returned %d", method, w.Code)
		}
	}
}

func TestStatsConcurrentPeerChanges(t *testing.T) {
	s := &server{peers: make(map[*webrtc.PeerConnection]*peerSession)}
	pc := &webrtc.PeerConnection{}
	var work sync.WaitGroup
	work.Add(1)
	go func() {
		defer work.Done()
		for i := 0; i < 100; i++ {
			s.mu.Lock()
			s.peers[pc] = &peerSession{}
			delete(s.peers, pc)
			s.mu.Unlock()
		}
	}()
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		s.handleStats(w, httptest.NewRequest(http.MethodGet, "/stats", nil))
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
	}
	work.Wait()
}
