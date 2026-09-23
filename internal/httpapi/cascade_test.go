package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/errdefs"
	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/session"
)

// The HTTP, auth, SQLite and temporary-directory layers are real. Docker and
// its network operations are intentionally simulated; this is not deployment
// evidence and never touches the user's local Docker daemon.
type cascadeRuntime struct {
	absentRuntime
	mu            sync.Mutex
	containers    map[string]*docker.ContainerInfo
	networks      map[string]bool
	inspectError  error
	removeFailure string
}

func (d *cascadeRuntime) InspectContainer(ctx context.Context, name string) (*docker.ContainerInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inspectError != nil {
		return nil, d.inspectError
	}
	info := d.containers[name]
	if info == nil {
		return nil, errdefs.NotFound(errors.New("test container absent"))
	}
	copy := *info
	return &copy, nil
}
func (d *cascadeRuntime) StopContainer(ctx context.Context, name string, timeout int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.containers[name]; c != nil {
		c.Running = false
		c.State = "exited"
	}
	return nil
}
func (d *cascadeRuntime) RemoveContainer(ctx context.Context, name string, force bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if name == d.removeFailure {
		return errors.New("injected container removal failure")
	}
	delete(d.containers, name)
	return nil
}
func (d *cascadeRuntime) RemoveSessionNetwork(ctx context.Context, opts docker.SessionNetworkOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.networks, opts.SessionID)
	return nil
}

type cascadeFixture struct {
	*httpFixture
	runtime *cascadeRuntime
	dir     string
	owned   []*model.Session
	other   *model.Session
}

func (f *cascadeFixture) newService() *session.Service {
	return session.NewService(f.st, f.runtime, 3, 0, f.dir, f.dir, 0, 0, 0, "cascade-test", "", "", "", "", "", "", "", "", "", "", "", "", false)
}
func setupCascade(t *testing.T) *cascadeFixture {
	t.Helper()
	f := &cascadeFixture{httpFixture: setupHTTP(t), dir: t.TempDir(), runtime: &cascadeRuntime{containers: map[string]*docker.ContainerInfo{}, networks: map[string]bool{}}}
	f.h.session = f.newService()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = f.h.session.Shutdown(ctx)
	})
	digest := sha256.Sum256([]byte(filepath.Clean(f.dir) + "\ncascade-test"))
	deployment := hex.EncodeToString(digest[:12])
	seed := func(owner *model.User, id string, status model.SessionStatus) *model.Session {
		s := &model.Session{ID: id, Name: id, UserID: owner.ID, Status: status, DesiredState: "running", ContainerName: "rb-sess-" + id, ProfileDir: filepath.Join(f.dir, "sessions", id, "profile"), DownloadsDir: filepath.Join(f.dir, "sessions", id, "downloads")}
		if status == model.SessionStopped {
			s.DesiredState = "stopped"
		}
		for _, path := range []string{s.ProfileDir, s.DownloadsDir} {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "marker.txt"), []byte(id+":persistent"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.st.CreateSession(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		f.runtime.containers[s.ContainerName] = &docker.ContainerInfo{ID: s.ContainerName, Name: s.ContainerName, Running: status == model.SessionRunning, State: "running", Labels: map[string]string{"remotebrowser.managed": "true", "remotebrowser.session.id": id, "remotebrowser.user.id": fmt.Sprint(owner.ID), "remotebrowser.deployment.id": deployment}, Mounts: map[string]string{"/home/rbuser/profile": s.ProfileDir, "/home/rbuser/Downloads": s.DownloadsDir}}
		f.runtime.networks[id] = true
		return s
	}
	for i, status := range []model.SessionStatus{model.SessionRunning, model.SessionStopped, model.SessionError} {
		f.owned = append(f.owned, seed(f.alice, fmt.Sprintf("sess_cascade_%d", i), status))
	}
	f.other = seed(f.bob, "sess_cascade_other", model.SessionRunning)
	return f
}
func (f *cascadeFixture) deleteForm() url.Values {
	return url.Values{"confirm_email": {f.alice.Email}, "confirm_delete": {"yes"}}
}
func (f *cascadeFixture) deleteURL() string {
	return fmt.Sprintf("/api/admin/users/%d/delete", f.alice.ID)
}
func (f *cascadeFixture) assertOtherUntouched(t *testing.T) {
	t.Helper()
	u, err := f.st.GetUserByID(context.Background(), f.bob.ID)
	if err != nil || u == nil || u.Status != model.UserActive || u.InstanceCount != 1 {
		t.Fatalf("other user changed: %+v %v", u, err)
	}
	s, err := f.st.GetSession(context.Background(), f.other.ID)
	if err != nil || s == nil || s.Status != model.SessionRunning {
		t.Fatal("other instance changed", s, err)
	}
	for _, dir := range []string{f.other.ProfileDir, f.other.DownloadsDir} {
		data, err := os.ReadFile(filepath.Join(dir, "marker.txt"))
		if err != nil || string(data) != f.other.ID+":persistent" {
			t.Fatal("other user's data changed", err)
		}
	}
	f.runtime.mu.Lock()
	defer f.runtime.mu.Unlock()
	if c := f.runtime.containers[f.other.ContainerName]; c == nil || !c.Running || !f.runtime.networks[f.other.ID] {
		t.Fatal("other runtime resources changed")
	}
}
func (f *cascadeFixture) assertAllOwnedRemoved(t *testing.T) {
	t.Helper()
	u, err := f.st.GetUserByID(context.Background(), f.alice.ID)
	if err != nil || u != nil {
		t.Fatal("account retained after all cleanup", u, err)
	}
	for _, s := range f.owned {
		if row, err := f.st.GetSession(context.Background(), s.ID); err != nil || row != nil {
			t.Fatal("instance ownership row remains", row, err)
		}
		for _, dir := range []string{s.ProfileDir, s.DownloadsDir} {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("data remains: %s %v", dir, err)
			}
		}
		f.runtime.mu.Lock()
		exists := f.runtime.containers[s.ContainerName] != nil || f.runtime.networks[s.ID]
		f.runtime.mu.Unlock()
		if exists {
			t.Fatal("simulated runtime resources remain", s.ID)
		}
	}
	f.assertOtherUntouched(t)
}

func TestHTTPMixedStateUserDeletionRequiresConfirmationAndCleansAllResources(t *testing.T) {
	f := setupCascade(t)
	page := f.request("GET", fmt.Sprintf("/admin/users/%d", f.alice.ID), f.adminToken, nil, false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "3 / 3 个实例") {
		t.Fatal("confirmation page lacks complete instance count", page.Code)
	}
	for _, s := range f.owned {
		if !strings.Contains(page.Body.String(), s.ID) {
			t.Fatal("confirmation omitted instance", s.ID)
		}
	}
	if got := f.request("POST", f.deleteURL(), f.bobToken, f.deleteForm(), true); got.Code != 403 {
		t.Fatal("ordinary user could delete account", got.Code)
	}
	for _, form := range []url.Values{{"confirm_email": {f.alice.Email}}, {"confirm_email": {f.bob.Email}, "confirm_delete": {"yes"}}} {
		if got := f.request("POST", f.deleteURL(), f.adminToken, form, true); got.Code != 400 {
			t.Fatal("missing or wrong confirmation accepted", got.Code)
		}
		u, err := f.st.GetUserByID(context.Background(), f.alice.ID)
		if err != nil || u == nil || u.Status != model.UserActive || u.InstanceCount != 3 {
			t.Fatal("failed confirmation mutated account", u, err)
		}
	}
	if got := f.request("POST", f.deleteURL(), f.adminToken, f.deleteForm(), true); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	f.assertAllOwnedRemoved(t)
	if got := f.request("GET", "/api/account", f.aliceToken, nil, true); got.Code != 401 {
		t.Fatal("deleted user's login survived")
	}
}

func TestHTTPMixedStateDeletionFailureFreezesAccessCountsAndRetries(t *testing.T) {
	for _, failure := range []string{"container-remove", "daemon-unavailable", "directory-symlink"} {
		t.Run(failure, func(t *testing.T) {
			f := setupCascade(t)
			failed := f.owned[1]
			expectedRemaining := 1
			restore := func() {}
			switch failure {
			case "container-remove":
				f.runtime.removeFailure = failed.ContainerName
			case "daemon-unavailable":
				f.runtime.inspectError = errors.New("injected Docker outage")
				expectedRemaining = 3
			case "directory-symlink":
				root := filepath.Dir(failed.ProfileDir)
				backup := filepath.Join(f.dir, "held-profile-for-test")
				if err := os.Rename(root, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(f.other.ProfileDir), root); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Remove(root); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(backup, root); err != nil {
						t.Fatal(err)
					}
				}
			}
			got := f.request("POST", f.deleteURL(), f.adminToken, f.deleteForm(), true)
			if got.Code != 400 {
				t.Fatal("failure not reported", got.Code, got.Body.String())
			}
			frozen, err := f.st.GetUserByID(context.Background(), f.alice.ID)
			if err != nil || frozen == nil || frozen.Status != model.UserDeleteFailed || frozen.InstanceCount != expectedRemaining || frozen.LastError == "" {
				t.Fatalf("wrong failed cleanup state: %+v %v", frozen, err)
			}
			list := f.request("GET", "/api/admin/users?q="+url.QueryEscape(f.alice.Email), f.adminToken, nil, true)
			var response struct {
				Users []struct {
					InstanceCount int    `json:"instanceCount"`
					Status        string `json:"status"`
				}
			}
			if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil || len(response.Users) != 1 || response.Users[0].InstanceCount != expectedRemaining || response.Users[0].Status != model.UserDeleteFailed {
				t.Fatal("failed cleanup list count incorrect", list.Body.String(), err)
			}
			for _, p := range []struct{ method, path string }{{"GET", "/api/account"}, {"POST", "/api/sessions"}, {"POST", "/api/sessions/" + failed.ID + "/start"}, {"POST", "/api/sessions/" + failed.ID + "/files/upload"}, {"POST", "/api/account/password"}} {
				if got := f.request(p.method, p.path, f.aliceToken, nil, true); got.Code != 401 {
					t.Fatalf("frozen user retains %s %s: %d", p.method, p.path, got.Code)
				}
			}
			if _, err := f.h.auth.Login(context.Background(), f.alice.Email, "permanent-password"); err == nil {
				t.Fatal("deleting user could start a fresh login")
			}
			f.assertOtherUntouched(t)
			f.runtime.mu.Lock()
			f.runtime.inspectError = nil
			f.runtime.removeFailure = ""
			f.runtime.mu.Unlock()
			restore()
			// Reconstruct only the service object, keeping the same SQLite / dirs /
			// fake runtime. This models resuming durable state, not a process restart.
			if err := f.h.session.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.h.session = f.newService()
			if got = f.request("POST", f.deleteURL(), f.adminToken, f.deleteForm(), true); got.Code != 200 {
				t.Fatal("retry failed", got.Code, got.Body.String())
			}
			f.assertAllOwnedRemoved(t)
			events, err := f.st.ListAuditEvents(context.Background(), 100)
			if err != nil {
				t.Fatal(err)
			}
			success, failedAudit := false, false
			for _, event := range events {
				if event.Action == "user.delete" && event.ActorID == f.admin.ID && event.TargetUserID == f.alice.ID {
					success = success || event.Result == "success"
					failedAudit = failedAudit || event.Result == "failed"
				}
			}
			if !success || !failedAudit {
				t.Fatal("cleanup retry lost success/failure audit")
			}
		})
	}
}
