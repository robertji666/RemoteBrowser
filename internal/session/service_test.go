package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/errdefs"
	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
)

type fakeRuntime struct {
	mu                                       sync.Mutex
	containers                               map[string]*docker.ContainerInfo
	inspectErr, startErr, stopErr, removeErr error
	creates, starts, stops, removes          int
	networkNames                             []string
	networks                                 map[string]docker.SessionNetworkOptions
	networkRemoveErr                         error
	createEntered, createContinue            chan struct{}
}

func (d *fakeRuntime) EnsureNetwork(context.Context, string) (string, error) { return "test", nil }
func (d *fakeRuntime) EnsureSessionNetwork(ctx context.Context, o docker.SessionNetworkOptions) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.networks == nil {
		d.networks = make(map[string]docker.SessionNetworkOptions)
	}
	d.networks[o.Name] = o
	return o.Name, nil
}
func (d *fakeRuntime) MigrateSessionNetwork(ctx context.Context, id string, o docker.SessionNetworkOptions) error {
	_, err := d.EnsureSessionNetwork(ctx, o)
	return err
}
func (d *fakeRuntime) RemoveSessionNetwork(ctx context.Context, o docker.SessionNetworkOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.networkRemoveErr != nil {
		return d.networkRemoveErr
	}
	delete(d.networks, o.Name)
	return nil
}
func (d *fakeRuntime) CreateSessionContainer(ctx context.Context, o docker.CreateSessionOptions) (string, error) {
	if d.createEntered != nil {
		close(d.createEntered)
		select {
		case <-d.createContinue:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.creates++
	d.networkNames = append(d.networkNames, o.NetworkName)
	info := &docker.ContainerInfo{ID: o.ContainerName, Name: o.ContainerName, Running: true, State: "running", Health: "healthy",
		Labels: map[string]string{"remotebrowser.managed": "true", "remotebrowser.session.id": o.SessionID, "remotebrowser.user.id": fmt.Sprint(o.UserID), "remotebrowser.deployment.id": o.DeploymentID},
		Mounts: map[string]string{o.ProfileDir: o.HostProfileDir, o.DownloadsDir: o.HostDownloadsDir}}
	d.containers[o.ContainerName] = info
	return info.ID, nil
}
func (d *fakeRuntime) InspectContainer(ctx context.Context, id string) (*docker.ContainerInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inspectErr != nil {
		return nil, d.inspectErr
	}
	info := d.containers[id]
	if info == nil {
		return nil, errdefs.NotFound(errors.New("missing container"))
	}
	copy := *info
	return &copy, nil
}
func (d *fakeRuntime) StartContainer(ctx context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.starts++
	if d.startErr != nil {
		return d.startErr
	}
	d.containers[id].Running = true
	return nil
}
func (d *fakeRuntime) StopContainer(ctx context.Context, id string, timeout int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stops++
	if d.stopErr != nil {
		return d.stopErr
	}
	d.containers[id].Running = false
	return nil
}
func (d *fakeRuntime) RemoveContainer(ctx context.Context, id string, force bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removes++
	if d.removeErr != nil {
		return d.removeErr
	}
	delete(d.containers, id)
	return nil
}
func (d *fakeRuntime) ListManagedContainers(context.Context) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for id := range d.containers {
		ids = append(ids, id)
	}
	return ids, nil
}

func setup(t *testing.T) (*Service, *fakeRuntime, *model.User) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	u := &model.User{Username: "person@example.com", Email: "person@example.com", Role: model.RoleUser, Status: model.UserActive, PasswordHash: "hash", InstanceQuota: 20}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{containers: make(map[string]*docker.ContainerInfo)}
	s := &Service{store: st, docker: runtime, dataDir: dir, hostDataDir: dir, deploymentID: "test", networkName: "test", connections: make(map[string]int), allocatePort: func() (string, error) { return "45000", nil }}
	return s, runtime, u
}

func seeded(t *testing.T, s *Service, d *fakeRuntime, u *model.User, status model.SessionStatus, desired string, running bool) *model.Session {
	t.Helper()
	id := generateID("sess_")
	sess := &model.Session{ID: id, UserID: u.ID, Name: "test", Status: status, DesiredState: desired, ContainerName: "rb-sess-" + id,
		ProfileDir: filepath.Join(s.dataDir, "sessions", id, "profile"), DownloadsDir: filepath.Join(s.dataDir, "sessions", id, "downloads"), SessionTokenHash: "unchanged-hash"}
	if err := s.store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	// Explicitly allow legacy blank desired state (new creates default running).
	if _, err := s.store.DB().Exec("UPDATE sessions SET status=?,desired_state=? WHERE id=?", status, desired, id); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareDirectories(sess, true); err != nil {
		t.Fatal(err)
	}
	d.containers[sess.ContainerName] = &docker.ContainerInfo{ID: sess.ContainerName, Name: sess.ContainerName, Running: running, Health: "healthy", Labels: map[string]string{"remotebrowser.managed": "true", "remotebrowser.session.id": id},
		Mounts: map[string]string{"/home/rbuser/profile": sess.ProfileDir, "/home/rbuser/Downloads": sess.DownloadsDir}}
	return sess
}

func mustSession(t *testing.T, s *Service, id string) *model.Session {
	t.Helper()
	sess, err := s.GetSession(context.Background(), id)
	if err != nil || sess == nil {
		t.Fatalf("session: %v %v", sess, err)
	}
	return sess
}

// Reopen SQLite and discard all process-local locks/connection state, as a
// manager restart does. Recovery decisions must come from persistent metadata.
func restarted(t *testing.T, previous *Service) *Service {
	t.Helper()
	if err := previous.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(previous.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return &Service{store: st, docker: previous.docker, dataDir: previous.dataDir,
		hostDataDir: previous.hostDataDir, deploymentID: previous.deploymentID,
		networkName: previous.networkName, connections: make(map[string]int), allocatePort: previous.allocatePort}
}

func TestNoIdleExpirationAndConnectionStateIndependent(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionRunning, "running", true)
	old := time.Now().Add(-48 * time.Hour)
	if _, err := s.store.DB().Exec("UPDATE sessions SET last_active_at=? WHERE id=?", old, sess.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionRunning || got.ExpiredAt != nil {
		t.Fatalf("idle changed instance: %+v", got)
	}
	if err := s.ConnectionOpened(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ConnectionClosed(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionRunning || got.ConnectedCount != 0 {
		t.Fatalf("disconnect changed runtime: %+v", got)
	}
	if d.stops != 0 || d.removes != 0 {
		t.Fatal("inactivity stopped resources")
	}
}

func TestNewInstanceUsesDedicatedNetwork(t *testing.T) {
	s, d, u := setup(t)
	s.allocatePort = func() (string, error) { return "45001", nil }
	sess, err := s.CreateSessionWithOptions(context.Background(), u.ID, "isolated", "network-test")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ready := mustSession(t, s, sess.ID).Status == model.SessionRunning
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.networkNames) != 1 || d.networkNames[0] != "rb-net-test-"+strings.TrimPrefix(sess.ID, "sess_") {
		t.Fatalf("instance network = %v", d.networkNames)
	}
	owner := d.networks[d.networkNames[0]]
	if owner.UserID != u.ID || owner.DeploymentID != "test" || owner.SessionID != sess.ID {
		t.Fatalf("network ownership not supplied: %+v", owner)
	}
}

func TestNetworkCleanupFailureRetainsInstanceUntilRetry(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionRunning, "running", true)
	d.networkRemoveErr = errors.New("network endpoint still attached")
	if err := s.DeleteSession(context.Background(), sess.ID); err == nil {
		t.Fatal("network cleanup failure ignored")
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionError || got.DesiredState != "deleted" {
		t.Fatalf("cleanup association lost: %+v", got)
	}
	if _, err := os.Stat(sess.ProfileDir); err != nil {
		t.Fatal("data removed before network cleanup was confirmed")
	}
	d.networkRemoveErr = nil
	if err := s.DeleteSession(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(context.Background(), sess.ID); got != nil {
		t.Fatal("completed deletion not finalized")
	}
}

func TestNetworkNamesSeparateDeploymentsAndInstances(t *testing.T) {
	a := &Service{deploymentID: "deployment-a"}
	b := &Service{deploymentID: "deployment-b"}
	if a.sessionNetworkName("sess_same") == b.sessionNetworkName("sess_same") {
		t.Fatal("cloned deployments share network")
	}
	if a.sessionNetworkName("sess_one") == a.sessionNetworkName("sess_two") {
		t.Fatal("instances share network")
	}
}

func TestLegacyMigrationReflectsActualContainer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  model.SessionStatus
		running bool
		want    model.SessionStatus
	}{
		{"expired stopped", model.SessionExpired, false, model.SessionStopped}, {"expired running", model.SessionExpired, true, model.SessionRunning},
		{"disconnected running", model.SessionDisconnected, true, model.SessionRunning}, {"disconnected exited", model.SessionDisconnected, false, model.SessionRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d, u := setup(t)
			sess := seeded(t, s, d, u, tc.status, "", tc.running)
			if err := s.ReconcileOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := mustSession(t, s, sess.ID); got.Status != tc.want {
				t.Fatalf("got %s want %s", got.Status, tc.want)
			}
		})
	}
}

func TestLegacyExpiredOutagePreservesStoppedIntentAcrossRestart(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionExpired, "", false)
	d.inspectErr = errors.New("daemon unavailable")
	if err := s.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("outage hidden")
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionUnknown || got.DesiredState != "legacy_expired" {
		t.Fatalf("migration intent lost: %+v", got)
	}
	s = restarted(t, s)
	d.inspectErr = nil
	if err := s.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionStopped {
		t.Fatalf("old expired instance restarted: %+v", got)
	}
	if d.starts != 0 {
		t.Fatal("unexpected restart")
	}
}

func TestStoppedMissingContainerRemainsVisibleAndCanBeRecreated(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionExpired, "", false)
	delete(d.containers, sess.ContainerName)
	if err := s.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("missing container not reported")
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionError || got.DesiredState != "stopped" {
		t.Fatalf("bad missing state: %+v", got)
	}
	if err := s.StartSession(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionRunning {
		t.Fatal("could not recreate retained instance")
	}
}

func TestStopFailureRetainsDesiredAndRetryConfirms(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionRunning, "running", true)
	d.stopErr = errors.New("stop denied")
	if err := s.StopSession(context.Background(), sess.ID); err == nil {
		t.Fatal("stop failure hidden")
	}
	got := mustSession(t, s, sess.ID)
	if got.Status != model.SessionError || got.DesiredState != "stopped" || got.LastError == "" {
		t.Fatalf("incorrect failure: %+v", got)
	}
	d.stopErr = nil
	if err := s.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionStopped {
		t.Fatal(got.Status)
	}
}

func TestDockerUnavailableNeverAssumedMissing(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionRunning, "running", true)
	d.inspectErr = errors.New("daemon unavailable")
	if err := s.StartSession(context.Background(), sess.ID); err == nil {
		t.Fatal("expected outage")
	}
	if d.creates != 0 {
		t.Fatal("outage recreated container")
	}
	if err := s.DeleteSession(context.Background(), sess.ID); err == nil {
		t.Fatal("delete outage hidden")
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionUnknown {
		t.Fatal(got.Status)
	}
	if _, err := os.Stat(sess.ProfileDir); err != nil {
		t.Fatal("data deleted during outage")
	}
	if d.removes != 0 {
		t.Fatal("unexpected removal")
	}
}

func TestDeleteFailureKeepsQuotaAndDataUntilRetry(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionRunning, "running", true)
	d.removeErr = errors.New("remove denied")
	if err := s.DeleteSession(context.Background(), sess.ID); err == nil {
		t.Fatal("expected failure")
	}
	if count, err := s.store.CountSessions(context.Background(), u.ID); err != nil || count != 1 {
		t.Fatalf("quota released: %d %v", count, err)
	}
	if _, err := os.Stat(sess.ProfileDir); err != nil {
		t.Fatal(err)
	}
	d.removeErr = nil
	if err := s.DeleteSession(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(context.Background(), sess.ID); got != nil {
		t.Fatal("record retained after complete cleanup")
	}
	if _, err := os.Stat(sess.ProfileDir); !os.IsNotExist(err) {
		t.Fatalf("data retained: %v", err)
	}
}

func TestDirectoryCleanupCannotFollowSymlinkIntoOtherInstance(t *testing.T) {
	s, d, u := setup(t)
	victim := seeded(t, s, d, u, model.SessionStopped, "stopped", false)
	target := seeded(t, s, d, u, model.SessionStopped, "stopped", false)
	base := filepath.Dir(target.ProfileDir)
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(victim.ProfileDir), base); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(context.Background(), target.ID); err == nil {
		t.Fatal("unsafe path deletion accepted")
	}
	if _, err := os.Stat(victim.ProfileDir); err != nil {
		t.Fatal("other instance deleted")
	}
	if got := mustSession(t, s, target.ID); got.LastError == "" {
		t.Fatal("directory failure not recorded")
	}
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(context.Background(), target.ID); err != nil {
		t.Fatal(err)
	}
}

func TestContainerOwnershipPreventsCrossInstanceRemoval(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionRunning, "running", true)
	d.containers[sess.ContainerName].Labels["remotebrowser.user.id"] = "999"
	if err := s.DeleteSession(context.Background(), sess.ID); err == nil {
		t.Fatal("ownership mismatch accepted")
	}
	if d.stops != 0 || d.removes != 0 {
		t.Fatal("foreign container modified")
	}
}

func TestPersistentReplacementAndStoppedSurviveManagerRestart(t *testing.T) {
	s, d, u := setup(t)
	running := seeded(t, s, d, u, model.SessionRunning, "running", true)
	stopped := seeded(t, s, d, u, model.SessionStopped, "stopped", false)
	marker := filepath.Join(running.ProfileDir, "marker")
	if err := os.WriteFile(marker, []byte("persistent"), 0600); err != nil {
		t.Fatal(err)
	}
	delete(d.containers, running.ContainerName)
	s = restarted(t, s)
	if err := s.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "persistent" {
		t.Fatalf("profile changed: %s %v", data, err)
	}
	got := mustSession(t, s, running.ID)
	if got.Status != model.SessionRunning || got.SessionTokenHash != "unchanged-hash" {
		t.Fatalf("replacement lost identity: %+v", got)
	}
	if got := mustSession(t, s, stopped.ID); got.Status != model.SessionStopped {
		t.Fatal("stopped instance restarted")
	}
	if d.creates != 1 || d.starts != 0 {
		t.Fatalf("unexpected recovery: create=%d start=%d", d.creates, d.starts)
	}
}

func TestMissingProfileNotReplacedWithEmptyData(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionRunning, "running", true)
	delete(d.containers, sess.ContainerName)
	if err := os.Remove(sess.ProfileDir); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("missing data ignored")
	}
	if d.creates != 0 {
		t.Fatal("created empty replacement")
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionError {
		t.Fatal(got.Status)
	}
}

func TestRecoveryIsBoundedAcrossServiceRestart(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionError, "running", false)
	d.startErr = errors.New("insufficient memory")
	for i := 0; i < 5; i++ {
		if i == 2 {
			s = restarted(t, s)
		}
		current := mustSession(t, s, sess.ID)
		if err := s.store.UpdateSessionRecovery(context.Background(), sess.ID, current.RecoveryAttempts, nil); err != nil {
			t.Fatal(err)
		}
		_ = s.ReconcileOnce(context.Background())
	}
	if d.starts != 3 {
		t.Fatalf("automatic retry count %d", d.starts)
	}
	if got := mustSession(t, s, sess.ID); got.RecoveryAttempts != 3 {
		t.Fatal("attempts not persisted")
	}
	d.startErr = nil
	if err := s.StartSession(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, sess.ID); got.Status != model.SessionRunning {
		t.Fatal("manual retry failed")
	}
}

func TestExhaustedRecoveryStopsDockerRestartLoop(t *testing.T) {
	s, d, u := setup(t)
	sess := seeded(t, s, d, u, model.SessionError, "running", true)
	d.containers[sess.ContainerName].Health = "unhealthy"
	if err := s.store.UpdateSessionRecovery(context.Background(), sess.ID, maxRecoveryAttempts, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("exhausted unhealthy runtime not reported")
	}
	if d.stops != 1 || d.starts != 0 {
		t.Fatalf("unbounded Docker restart remains: stops=%d starts=%d", d.stops, d.starts)
	}
	if got := mustSession(t, s, sess.ID); got.DesiredState != "running" || got.Status != model.SessionError {
		t.Fatalf("retry intent lost: %+v", got)
	}
}

func TestOrphansReportedNeverRemoved(t *testing.T) {
	s, d, u := setup(t)
	_ = seeded(t, s, d, u, model.SessionRunning, "running", true)
	d.containers["orphan"] = &docker.ContainerInfo{ID: "orphan", Labels: map[string]string{"remotebrowser.managed": "true", "remotebrowser.session.id": "sess_orphan"}}
	if err := s.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	orphans, err := s.ListOrphanContainers(context.Background())
	if err != nil || len(orphans) != 1 || orphans[0].ID != "orphan" {
		t.Fatalf("orphans %v %v", orphans, err)
	}
	if d.removes != 0 {
		t.Fatal("orphan deleted")
	}
}

func TestDeleteUserFailureResumesAfterRestartAndPreservesOtherUser(t *testing.T) {
	s, d, u := setup(t)
	owned := seeded(t, s, d, u, model.SessionRunning, "running", true)
	other := &model.User{Username: "other@example.com", Email: "other@example.com", PasswordHash: "hash", Role: model.RoleUser, Status: model.UserActive, InstanceQuota: 2}
	if err := s.store.CreateUser(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	untouched := seeded(t, s, d, other, model.SessionRunning, "running", true)
	d.inspectErr = errors.New("daemon unavailable")
	if err := s.DeleteUser(context.Background(), u.ID); err == nil {
		t.Fatal("delete outage hidden")
	}
	frozen, err := s.store.GetUserByID(context.Background(), u.ID)
	if err != nil || frozen.Status != model.UserDeleteFailed {
		t.Fatalf("user not frozen: %+v %v", frozen, err)
	}
	if _, err := s.CreateSession(context.Background(), u.ID); !errors.Is(err, store.ErrUserInactive) {
		t.Fatalf("frozen user created: %v", err)
	}
	if err := s.StartSession(context.Background(), owned.ID); !errors.Is(err, store.ErrUserInactive) {
		t.Fatalf("frozen user started: %v", err)
	}
	d.inspectErr = nil
	s = restarted(t, s)
	if err := s.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.store.GetUserByID(context.Background(), u.ID); got != nil {
		t.Fatal("user retained after cleanup")
	}
	if _, err := os.Stat(owned.ProfileDir); !os.IsNotExist(err) {
		t.Fatal("deleted user data retained")
	}
	if _, err := os.Stat(untouched.ProfileDir); err != nil {
		t.Fatal("other user data deleted")
	}
	if got := mustSession(t, s, untouched.ID); got.Status != model.SessionRunning {
		t.Fatal("other user altered")
	}
}

func TestDeleteUserDrainsInflightCreation(t *testing.T) {
	s, d, u := setup(t)
	d.createEntered = make(chan struct{})
	d.createContinue = make(chan struct{})
	sess, err := s.CreateSessionWithOptions(context.Background(), u.ID, "race", "one-request")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.createEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("creation did not begin")
	}
	done := make(chan error, 1)
	go func() { done <- s.DeleteUser(context.Background(), u.ID) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, err := s.store.GetUserByID(context.Background(), u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == model.UserDeleting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deletion did not freeze account")
		}
		time.Sleep(time.Millisecond)
	}
	close(d.createContinue)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(context.Background(), sess.ID); got != nil {
		t.Fatal("inflight instance record leaked")
	}
	if len(d.containers) != 0 {
		t.Fatal("inflight container orphaned")
	}
	if _, err := os.Stat(sess.ProfileDir); !os.IsNotExist(err) {
		t.Fatal("inflight data leaked")
	}
}

func TestCreateIdempotencyDoesNotDuplicateOrConsumeQuota(t *testing.T) {
	s, d, u := setup(t)
	first, err := s.CreateSessionWithOptions(context.Background(), u.ID, "original", "request-123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateSessionWithOptions(context.Background(), u.ID, "duplicate", "request-123")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatal("duplicate request created second instance")
	}
	// Wait for the asynchronous launch before fixture cleanup.
	deadline := time.Now().Add(3 * time.Second)
	for mustSession(t, s, first.ID).Status != model.SessionRunning {
		if time.Now().After(deadline) {
			t.Fatal("asynchronous launch did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if count, err := s.store.CountSessions(context.Background(), u.ID); err != nil || count != 1 {
		t.Fatalf("count %d %v", count, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.creates != 1 {
		t.Fatalf("created %d containers", d.creates)
	}
}

func TestShutdownCancelsProvisioningAndLeavesRecoverableRecord(t *testing.T) {
	s, d, u := setup(t)
	d.createEntered = make(chan struct{})
	d.createContinue = make(chan struct{})
	sess, err := s.CreateSessionWithOptions(context.Background(), u.ID, "shutdown", "shutdown-request")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.createEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("creation did not enter runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := mustSession(t, s, sess.ID); got.DesiredState != "running" || got.LastError == "" {
		t.Fatalf("shutdown lost recovery information: %+v", got)
	}
	if _, ok := s.beginBackgroundTask(); ok {
		t.Fatal("accepted task after shutdown")
	}
}

func TestLifecycleLockWaitHonorsCancellation(t *testing.T) {
	s, _, u := setup(t)
	unlock := s.lockUser(u.ID)
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.WithUserLock(ctx, u.ID, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock wait did not cancel: %v", err)
	}
}
