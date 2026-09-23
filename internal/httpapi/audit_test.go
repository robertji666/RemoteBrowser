package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestLifecycleAuditSurvivesInstanceAndUserDeletion(t *testing.T) {
	f := setupHTTP(t)
	w := f.request("POST", "/api/sessions", f.aliceToken, url.Values{"name": {"private-browser-content-marker"}, "request_key": {"audit-create-one"}}, true)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ID == "" {
		t.Fatal("missing created instance", err)
	}
	for _, action := range []struct {
		method, suffix string
		want           int
	}{{"POST", "stop", 200}, {"POST", "start", 500}, {"DELETE", "", 200}} {
		path := "/api/sessions/" + result.ID
		if action.suffix != "" {
			path += "/" + action.suffix
		}
		got := f.request(action.method, path, f.aliceToken, nil, true)
		if got.Code != action.want {
			t.Fatalf("%s %s: %d %s", action.method, path, got.Code, got.Body.String())
		}
	}
	deletePath := fmt.Sprintf("/api/admin/users/%d/delete", f.alice.ID)
	if got := f.request("POST", deletePath, f.adminToken, url.Values{"confirm_email": {f.alice.Email}}, true); got.Code != 400 {
		t.Fatal("missing delete confirmation accepted")
	}
	if got := f.request("POST", deletePath, f.adminToken, url.Values{"confirm_email": {f.alice.Email}, "confirm_delete": {"yes"}}, true); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if u, _ := f.st.GetUserByID(context.Background(), f.alice.ID); u != nil {
		t.Fatal("fixture account not deleted")
	}
	events, err := f.st.ListAuditEvents(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"instance.create": "accepted", "instance.stop": "success", "instance.start": "failed", "instance.delete": "success", "user.delete": "success"}
	seen := map[string]bool{}
	for _, e := range events {
		if strings.Contains(e.Detail, "private-browser-content-marker") || strings.Contains(e.Detail, "temporary-password") || strings.Contains(e.Detail, f.alice.Email) {
			t.Fatal("audit recorded sensitive form fields")
		}
		if expected, ok := want[e.Action]; ok && e.Result == expected {
			if e.TargetUserID != f.alice.ID {
				t.Fatalf("wrong target user %#v", e)
			}
			actor := f.alice.ID
			if e.Action == "user.delete" {
				actor = f.admin.ID
			} else if e.TargetID != result.ID {
				t.Fatal("audit lost deleted instance ID")
			}
			if e.ActorID != actor {
				t.Fatal("wrong actor", e.ActorID)
			}
			seen[e.Action] = true
		}
	}
	for action := range want {
		if !seen[action] {
			t.Errorf("missing persistent audit for %s", action)
		}
	}
}

func TestQuotaFailureAuditUsesFixedErrorCode(t *testing.T) {
	f := setupHTTP(t)
	if err := f.st.UpdateUserQuota(context.Background(), f.alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	w := f.request("POST", "/api/sessions", f.aliceToken, url.Values{"name": {"never-log-this-private-name"}}, true)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	events, err := f.st.ListAuditEvents(context.Background(), 1)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	e := events[0]
	if e.Action != "instance.create" || e.Result != "failed" || e.Detail != "quota_exceeded" || e.ActorID != f.alice.ID || e.TargetUserID != f.alice.ID {
		t.Fatalf("bad sanitized audit %#v", e)
	}
}
