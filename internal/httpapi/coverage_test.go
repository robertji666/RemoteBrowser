package httpapi

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/robertji666/RemoteBrowser/internal/model"
)

func TestUserListCountsAllStatesAndRefreshesReservations(t *testing.T) {
	f := setupHTTP(t)
	for i, status := range []model.SessionStatus{model.SessionRunning, model.SessionStopped, model.SessionError} {
		id := []string{"sess_count_running", "sess_count_stopped", "sess_count_error"}[i]
		if err := f.st.CreateSession(context.Background(), &model.Session{ID: id, Name: id, UserID: f.alice.ID, Status: status, DesiredState: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want int) {
		t.Helper()
		w := f.request("GET", "/api/admin/users?q="+url.QueryEscape(f.alice.Email), f.adminToken, nil, true)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var data struct {
			Users []struct {
				ID    int64 `json:"id"`
				Count int   `json:"instanceCount"`
				Quota int   `json:"instanceQuota"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Users) != 1 || data.Users[0].ID != f.alice.ID || data.Users[0].Count != want || data.Users[0].Quota != 3 {
			t.Fatalf("list count does not match metadata: %+v", data)
		}
	}
	check(3)
	if w := f.request("POST", "/api/sessions", f.aliceToken, url.Values{"name": {"over-limit"}}, true); w.Code != 400 {
		t.Fatal("nonrunning instances did not consume quota")
	}
	// Store deletion represents completed resource cleanup; this test exercises
	// fresh HTTP counts, while lifecycle tests verify physical cleanup ordering.
	if err := f.st.DeleteSession(context.Background(), "sess_count_error"); err != nil {
		t.Fatal(err)
	}
	check(2)
	if _, created, err := f.st.CreateSessionWithinQuota(context.Background(), &model.Session{ID: "sess_count_creating", UserID: f.alice.ID, Status: model.SessionCreating, DesiredState: "running"}, "count-reservation"); err != nil || !created {
		t.Fatal(created, err)
	}
	check(3)
}
