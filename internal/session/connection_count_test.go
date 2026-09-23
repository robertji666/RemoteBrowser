package session

import (
	"context"
	"errors"
	"testing"

	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
)

func TestConnectionOpenErrorsNeverIncrement(t *testing.T) {
	s, d, u := setup(t)
	running := seeded(t, s, d, u, model.SessionRunning, "running", true)
	stopped := seeded(t, s, d, u, model.SessionStopped, "stopped", false)
	if err := s.ConnectionOpened(context.Background(), running.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		id   string
		want error
	}{
		{"cancelled", ctx, running.ID, context.Canceled},
		{"missing", context.Background(), "sess_missing", store.ErrNotFound},
		{"stopped", context.Background(), stopped.ID, ErrSessionNotRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.ConnectionOpened(tc.ctx, tc.id); !errors.Is(err, tc.want) {
				t.Fatalf("open error = %v; want %v", err, tc.want)
			}
			if s.ConnectedCount(running.ID) != 1 || s.ConnectedCount(stopped.ID) != 0 || s.ConnectedCount("sess_missing") != 0 {
				t.Fatal("failed open changed existing connection accounting")
			}
		})
	}
	if err := s.ConnectionOpened(context.Background(), running.ID); err != nil {
		t.Fatal(err)
	}
	if s.ConnectedCount(running.ID) != 2 {
		t.Fatal("successful second open did not increment")
	}
	for want := 1; want >= 0; want-- {
		if err := s.ConnectionClosed(context.Background(), running.ID); err != nil {
			t.Fatal(err)
		}
		if s.ConnectedCount(running.ID) != want {
			t.Fatal("close did not balance successful open")
		}
	}
}
