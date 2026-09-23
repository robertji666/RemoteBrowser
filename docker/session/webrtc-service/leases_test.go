package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestMediaLeaseRenewAndRevoke(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	var releases atomic.Int32
	lease := strings.Repeat("a", 32)
	s := &server{peers: map[*webrtc.PeerConnection]*peerSession{pc: {lease: lease, expiresAt: time.Now(), release: func() { releases.Add(1) }}}}
	call := func(method, value string) int {
		w := httptest.NewRecorder()
		s.handleLease(w, httptest.NewRequest(method, "/lease", strings.NewReader(`{"lease":"`+value+`"}`)))
		return w.Code
	}
	if got := call("POST", strings.Repeat("b", 32)); got != 404 {
		t.Fatal(got)
	}
	if got := call("POST", lease); got != 200 {
		t.Fatal(got)
	}
	if time.Until(s.peers[pc].expiresAt) < 7*time.Second {
		t.Fatal("lease not renewed")
	}
	if got := call("DELETE", lease); got != 200 {
		t.Fatal(got)
	}
	if releases.Load() != 1 || len(s.peers) != 0 {
		t.Fatal("peer not revoked exactly once")
	}
	if got := call("DELETE", lease); got != 404 {
		t.Fatal(got)
	}
	if got := call("GET", lease); got != 405 {
		t.Fatal(got)
	}
	if got := call("POST", "bad"); got != 400 {
		t.Fatal(got)
	}
}

func TestMediaLeaseExpiresWithoutManager(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	released := make(chan struct{})
	s := &server{peers: map[*webrtc.PeerConnection]*peerSession{pc: {lease: strings.Repeat("a", 32), expiresAt: time.Now().Add(-time.Second), release: func() { close(released) }}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.expireLeases(ctx)
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("expired media stayed active")
	}
	w := httptest.NewRecorder()
	s.handleOffer(w, httptest.NewRequest(http.MethodPost, "/offer", strings.NewReader(`{}`)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unleased offer accepted: %d", w.Code)
	}
}

func TestRenewedMediaLeaseExpiresWithinTenSecondsWithoutManager(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	released := make(chan struct{})
	lease := strings.Repeat("c", 32)
	closed := make(chan struct{})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateClosed {
			close(closed)
		}
	})
	s := &server{peers: map[*webrtc.PeerConnection]*peerSession{pc: {lease: lease, release: func() { close(released) }}}}
	w := httptest.NewRecorder()
	s.handleLease(w, httptest.NewRequest(http.MethodPost, "/lease", strings.NewReader(`{"lease":"`+lease+`"}`)))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := time.Now()
	go s.expireLeases(ctx)
	deadline := time.After(10 * time.Second)
	select {
	case <-released:
		if time.Since(started) < 7*time.Second {
			t.Fatal("fresh lease expired before its promised lifetime")
		}
	case <-deadline:
		t.Fatal("media transport survived the ten-second revocation limit")
	}
	select {
	case <-closed:
	case <-deadline:
		t.Fatal("media transport stayed open after lease expired")
	}
}
