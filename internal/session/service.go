package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// ContainerRuntime distinguishes confirmed absence from daemon outages.
type ContainerRuntime interface {
	EnsureNetwork(context.Context, string) (string, error)
	CreateSessionContainer(context.Context, docker.CreateSessionOptions) (string, error)
	InspectContainer(context.Context, string) (*docker.ContainerInfo, error)
	StartContainer(context.Context, string) error
	StopContainer(context.Context, string, int) error
	RemoveContainer(context.Context, string, bool) error
	ListManagedContainers(context.Context) ([]string, error)
}

type Service struct {
	store                                                                     *store.Store
	docker                                                                    ContainerRuntime
	dataDir, hostDataDir, deploymentID, networkName                           string
	sessionNanoCPUs, sessionMemoryBytes, sessionShmBytes                      int64
	webrtcICEIP, screenWidth, screenHeight, screenDepth                       string
	chromeWindowTop, chromeWindowBottom, webrtcWidth, webrtcHeight            string
	webrtcFramerate, webrtcVideoCodec, webrtcVideoBitrate, webrtcAudioBitrate string
	publishSessionTCPPorts                                                    bool
	connections                                                               map[string]int
	connectionsMu                                                             sync.Mutex
	userLocks                                                                 sync.Map
	allocatePort                                                              func() (string, error)
	lifecycleMu                                                               sync.Mutex
	lifecycleCtx                                                              context.Context
	lifecycleCancel                                                           context.CancelFunc
	backgroundTasks                                                           sync.WaitGroup
	closing                                                                   bool
}

// Shutdown cancels in-flight background provisioning before the store closes.
// Instance desired state remains durable so the next manager resumes recovery.
func (s *Service) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	s.closing = true
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	s.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() { s.backgroundTasks.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) beginBackgroundTask() (context.Context, bool) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return nil, false
	}
	if s.lifecycleCtx == nil {
		s.lifecycleCtx, s.lifecycleCancel = context.WithCancel(context.Background())
	}
	s.backgroundTasks.Add(1)
	return s.lifecycleCtx, true
}

// maxSessions and idleTimeout remain for source compatibility only. Quotas
// belong to users; inactivity never stops or expires an instance.
func NewService(st *store.Store, dc ContainerRuntime, maxSessions int, idleTimeout time.Duration, dataDir, hostDataDir string, nanoCPUs, memory, shm int64, network, iceIP, screenWidth, screenHeight, screenDepth, windowTop, windowBottom, width, height, framerate, codec, videoBitrate, audioBitrate string, publishTCP bool) *Service {
	if nanoCPUs <= 0 {
		nanoCPUs = 2e9
	}
	if memory <= 0 {
		memory = 3 << 30
	}
	if shm <= 0 {
		shm = 1 << 30
	}
	if hostDataDir == "" {
		hostDataDir = dataDir
	}
	if network == "" {
		network = "remotebrowser"
	}
	defaults := []struct {
		value    *string
		fallback string
	}{
		{&iceIP, "127.0.0.1"}, {&screenWidth, "1280"}, {&screenHeight, "752"}, {&screenDepth, "24"},
		{&windowTop, "32"}, {&windowBottom, "0"}, {&width, "1280"}, {&height, "720"},
		{&framerate, "24"}, {&codec, "vp8"}, {&videoBitrate, "1800k"}, {&audioBitrate, "96k"},
	}
	for _, d := range defaults {
		if *d.value == "" {
			*d.value = d.fallback
		}
	}
	hash := sha256.Sum256([]byte(filepath.Clean(hostDataDir) + "\n" + network))
	return &Service{store: st, docker: dc, dataDir: dataDir, hostDataDir: hostDataDir,
		deploymentID: hex.EncodeToString(hash[:12]), networkName: network, sessionNanoCPUs: nanoCPUs,
		sessionMemoryBytes: memory, sessionShmBytes: shm, webrtcICEIP: iceIP, screenWidth: screenWidth,
		screenHeight: screenHeight, screenDepth: screenDepth, chromeWindowTop: windowTop,
		chromeWindowBottom: windowBottom, webrtcWidth: width, webrtcHeight: height, webrtcFramerate: framerate,
		webrtcVideoCodec: codec, webrtcVideoBitrate: videoBitrate, webrtcAudioBitrate: audioBitrate,
		publishSessionTCPPorts: publishTCP, connections: make(map[string]int)}
}

func (s *Service) Init(ctx context.Context) error {
	_, err := s.docker.EnsureNetwork(ctx, s.networkName)
	return err
}

func (s *Service) lockUser(id int64) func() {
	value, _ := s.userLocks.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) lockUserContext(ctx context.Context, id int64) (func(), error) {
	value, _ := s.userLocks.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if mu.TryLock() {
			return mu.Unlock, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (s *Service) requireActive(ctx context.Context, userID int64) error {
	u, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil || u.Status != "active" {
		return store.ErrUserInactive
	}
	return nil
}

// WithUserLock serializes file writes with user deletion. Authorization that
// depends on the instance must be rechecked inside the callback.
func (s *Service) WithUserLock(ctx context.Context, userID int64, fn func() error) error {
	unlock, err := s.lockUserContext(ctx, userID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.requireActive(ctx, userID); err != nil {
		return err
	}
	return fn()
}

func (s *Service) CreateSession(ctx context.Context, userID int64) (*model.Session, error) {
	return s.CreateSessionWithOptions(ctx, userID, "", "")
}

func (s *Service) CreateSessionWithOptions(ctx context.Context, userID int64, name, requestKey string) (*model.Session, error) {
	unlock, err := s.lockUserContext(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 80 {
		return nil, fmt.Errorf("实例名称不能超过 80 个字符")
	}
	if len(requestKey) > 128 {
		return nil, fmt.Errorf("invalid creation request key")
	}
	id := generateID("sess_")
	if name == "" {
		name = id
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(generateToken()), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	sess := &model.Session{ID: id, UserID: userID, Name: name, Status: model.SessionCreating,
		DesiredState: "running", ContainerName: "rb-sess-" + id,
		ProfileDir:   filepath.Join(s.dataDir, "sessions", id, "profile"),
		DownloadsDir: filepath.Join(s.dataDir, "sessions", id, "downloads"), SessionTokenHash: string(hash)}
	sess, created, err := s.store.CreateSessionWithinQuota(ctx, sess, requestKey)
	if err != nil {
		return nil, err
	}
	if !created {
		return sess, nil
	}
	// Durable ownership and quota reservation precede resource allocation.
	if err := s.prepareDirectories(sess, true); err != nil {
		_ = s.recordFailure(context.WithoutCancel(ctx), sess, model.SessionError, err)
		return sess, err
	}
	s.event(ctx, sess.ID, model.EventCreated, "")
	lifetime, ok := s.beginBackgroundTask()
	if !ok {
		return sess, fmt.Errorf("manager is shutting down; instance creation will resume after restart")
	}
	go func() {
		defer s.backgroundTasks.Done()
		ctx, cancel := context.WithTimeout(lifetime, 90*time.Second)
		defer cancel()
		unlock, err := s.lockUserContext(ctx, userID)
		if err != nil {
			return
		}
		defer unlock()
		current, err := s.store.GetSession(ctx, sess.ID)
		if err != nil || current == nil || current.DesiredState != "running" {
			return
		}
		if err := s.requireActive(ctx, userID); err != nil {
			return
		}
		_ = s.startLocked(ctx, current)
	}()
	return sess, nil
}

func (s *Service) createContainer(ctx context.Context, sess *model.Session) error {
	if err := s.prepareDirectories(sess, false); err != nil {
		return err
	}
	allocator := s.allocatePort
	if allocator == nil {
		allocator = allocateUDPPort
	}
	port, err := allocator()
	if err != nil {
		return err
	}
	sessionNetwork := s.sessionNetworkName(sess.ID)
	if err := s.ensureSessionNetwork(ctx, sess); err != nil {
		return fmt.Errorf("prepare isolated instance network: %w", err)
	}
	_, err = s.docker.CreateSessionContainer(ctx, docker.CreateSessionOptions{
		SessionID: sess.ID, UserID: sess.UserID, DeploymentID: s.deploymentID, ContainerName: sess.ContainerName,
		Image: "remotebrowser/session:latest", ProfileDir: "/home/rbuser/profile", DownloadsDir: "/home/rbuser/Downloads",
		HostProfileDir:   filepath.Join(s.hostDataDir, "sessions", sess.ID, "profile"),
		HostDownloadsDir: filepath.Join(s.hostDataDir, "sessions", sess.ID, "downloads"),
		NoVNCPort:        "6080", AudioServicePort: "6081", WebRTCPort: "6082", InputServicePort: "6084", FileServicePort: "8081",
		WebRTCUDPPort: port, WebRTCICEIP: s.webrtcICEIP, ScreenWidth: s.screenWidth, ScreenHeight: s.screenHeight,
		ScreenDepth: s.screenDepth, ChromeWindowTop: s.chromeWindowTop, ChromeWindowBottom: s.chromeWindowBottom,
		WebRTCWidth: s.webrtcWidth, WebRTCHeight: s.webrtcHeight, WebRTCFramerate: s.webrtcFramerate,
		WebRTCVideoCodec: s.webrtcVideoCodec, WebRTCVideoBitrate: s.webrtcVideoBitrate, WebRTCAudioBitrate: s.webrtcAudioBitrate,
		NanoCPUs: s.sessionNanoCPUs, Memory: s.sessionMemoryBytes, ShmSize: s.sessionShmBytes,
		NetworkName: sessionNetwork, PublishTCPPorts: s.publishSessionTCPPorts})
	return err
}

func (s *Service) sessionNetworkName(id string) string {
	return "rb-net-" + s.deploymentID + "-" + strings.TrimPrefix(id, "sess_")
}

func (s *Service) sessionNetworkOptions(sess *model.Session) docker.SessionNetworkOptions {
	return docker.SessionNetworkOptions{Name: s.sessionNetworkName(sess.ID), SessionID: sess.ID, UserID: sess.UserID, DeploymentID: s.deploymentID}
}

func (s *Service) ensureSessionNetwork(ctx context.Context, sess *model.Session) error {
	if runtime, ok := s.docker.(interface {
		EnsureSessionNetwork(context.Context, docker.SessionNetworkOptions) (string, error)
	}); ok {
		_, err := runtime.EnsureSessionNetwork(ctx, s.sessionNetworkOptions(sess))
		return err
	}
	_, err := s.docker.EnsureNetwork(ctx, s.sessionNetworkName(sess.ID))
	return err
}

func (s *Service) isolateContainerNetwork(ctx context.Context, sess *model.Session, info *docker.ContainerInfo) error {
	migrator, ok := s.docker.(interface {
		MigrateSessionNetwork(context.Context, string, docker.SessionNetworkOptions) error
	})
	if !ok || info == nil {
		return nil
	}
	return migrator.MigrateSessionNetwork(ctx, info.ID, s.sessionNetworkOptions(sess))
}

func (s *Service) startLocked(ctx context.Context, sess *model.Session) error {
	if err := s.store.UpdateSessionState(ctx, sess.ID, model.SessionStarting, "running", ""); err != nil {
		return err
	}
	sess.DesiredState = "running"
	info, err := s.docker.InspectContainer(ctx, sess.ContainerName)
	inspectFailed := err != nil && !docker.IsNotFound(err)
	if docker.IsNotFound(err) {
		err = s.createContainer(ctx, sess)
	} else if err == nil {
		err = s.verifyContainer(sess, info)
		if err == nil {
			err = s.isolateContainerNetwork(ctx, sess, info)
		}
		if err == nil && info.Running && info.Health == "unhealthy" {
			err = s.docker.StopContainer(ctx, info.ID, 10)
			if err == nil {
				info.Running = false
			}
		}
		if err == nil && !info.Running {
			err = s.docker.StartContainer(ctx, info.ID)
		}
	}
	if err != nil {
		status := model.SessionError
		if inspectFailed {
			status = model.SessionUnknown
		}
		return s.recordFailure(ctx, sess, status, err)
	}
	if err := s.waitForHealthy(ctx, sess.ContainerName); err != nil {
		return s.recordFailure(ctx, sess, model.SessionError, err)
	}
	if err := s.store.UpdateSessionState(ctx, sess.ID, model.SessionRunning, "running", ""); err != nil {
		return err
	}
	s.event(ctx, sess.ID, model.EventStarted, "")
	return nil
}

func (s *Service) waitForHealthy(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	for {
		info, err := s.docker.InspectContainer(ctx, name)
		if err != nil {
			return fmt.Errorf("inspect container health: %w", err)
		}
		if !info.Running {
			return fmt.Errorf("container exited before becoming ready (%s)", info.State)
		}
		if info.Health == "healthy" || (info.Health == "" && time.Since(start) >= 3*time.Second) {
			return nil
		}
		if info.Health == "unhealthy" {
			return fmt.Errorf("container healthcheck failed")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("container readiness: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (s *Service) withSession(ctx context.Context, id string, active bool, fn func(*model.Session) error) error {
	sess, err := s.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if sess == nil {
		return store.ErrNotFound
	}
	unlock, err := s.lockUserContext(ctx, sess.UserID)
	if err != nil {
		return err
	}
	defer unlock()
	sess, err = s.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if sess == nil {
		return store.ErrNotFound
	}
	if active {
		if err := s.requireActive(ctx, sess.UserID); err != nil {
			return err
		}
	}
	return fn(sess)
}

func (s *Service) StartSession(ctx context.Context, id string) error {
	return s.withSession(ctx, id, true, func(sess *model.Session) error {
		if sess.DesiredState == "deleted" {
			return fmt.Errorf("实例正在删除，不能启动")
		}
		if err := s.store.UpdateSessionRecovery(ctx, id, 0, nil); err != nil {
			return err
		}
		return s.startLocked(ctx, sess)
	})
}

func (s *Service) StopSession(ctx context.Context, id string) error {
	return s.withSession(ctx, id, true, func(sess *model.Session) error {
		if sess.DesiredState == "deleted" {
			return fmt.Errorf("实例正在删除")
		}
		if err := s.store.UpdateSessionState(ctx, id, model.SessionStopping, "stopped", ""); err != nil {
			return err
		}
		sess.DesiredState = "stopped"
		return s.stopLocked(ctx, sess)
	})
}

func (s *Service) stopLocked(ctx context.Context, sess *model.Session) error {
	info, err := s.docker.InspectContainer(ctx, sess.ContainerName)
	if err != nil && !docker.IsNotFound(err) {
		return s.recordFailure(ctx, sess, model.SessionUnknown, err)
	}
	if err == nil {
		if err := s.verifyContainer(sess, info); err != nil {
			return s.recordFailure(ctx, sess, model.SessionError, err)
		}
		if err := s.isolateContainerNetwork(ctx, sess, info); err != nil {
			return s.recordFailure(ctx, sess, model.SessionError, err)
		}
		if info.Running {
			if err := s.docker.StopContainer(ctx, info.ID, 10); err != nil {
				return s.recordFailure(ctx, sess, model.SessionError, err)
			}
			info, err = s.docker.InspectContainer(ctx, sess.ContainerName)
			if err != nil && !docker.IsNotFound(err) {
				return s.recordFailure(ctx, sess, model.SessionUnknown, err)
			}
			if err == nil && info.Running {
				return s.recordFailure(ctx, sess, model.SessionError, fmt.Errorf("container remains running after stop"))
			}
		}
	}
	if err := s.store.UpdateSessionState(ctx, sess.ID, model.SessionStopped, "stopped", ""); err != nil {
		return err
	}
	s.event(ctx, sess.ID, model.EventStopped, "")
	return nil
}

func (s *Service) DeleteSession(ctx context.Context, id string) error {
	err := s.withSession(ctx, id, true, func(sess *model.Session) error { return s.deleteLocked(ctx, sess) })
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Service) deleteLocked(ctx context.Context, sess *model.Session) error {
	if err := s.store.UpdateSessionState(ctx, sess.ID, model.SessionDeleting, "deleted", ""); err != nil {
		return err
	}
	sess.DesiredState = "deleted"
	info, err := s.docker.InspectContainer(ctx, sess.ContainerName)
	if err != nil && !docker.IsNotFound(err) {
		return s.recordFailure(ctx, sess, model.SessionUnknown, err)
	}
	if err == nil {
		if err := s.verifyContainer(sess, info); err != nil {
			return s.recordFailure(ctx, sess, model.SessionError, err)
		}
		if info.Running {
			if err := s.docker.StopContainer(ctx, info.ID, 10); err != nil {
				return s.recordFailure(ctx, sess, model.SessionError, err)
			}
		}
		if err := s.docker.RemoveContainer(ctx, info.ID, false); err != nil && !docker.IsNotFound(err) {
			return s.recordFailure(ctx, sess, model.SessionError, err)
		}
		_, err = s.docker.InspectContainer(ctx, sess.ContainerName)
		if !docker.IsNotFound(err) {
			if err == nil {
				err = fmt.Errorf("container still exists after deletion")
			}
			return s.recordFailure(ctx, sess, model.SessionUnknown, err)
		}
	}
	if remover, ok := s.docker.(interface {
		RemoveSessionNetwork(context.Context, docker.SessionNetworkOptions) error
	}); ok {
		if err := remover.RemoveSessionNetwork(ctx, s.sessionNetworkOptions(sess)); err != nil {
			return s.recordFailure(ctx, sess, model.SessionError, fmt.Errorf("remove instance network: %w", err))
		}
	}
	if err := s.removeDirectories(sess.ID); err != nil {
		return s.recordFailure(ctx, sess, model.SessionError, err)
	}
	if err := s.store.DeleteSession(ctx, sess.ID); err != nil {
		return err
	}
	s.connectionsMu.Lock()
	delete(s.connections, sess.ID)
	s.connectionsMu.Unlock()
	return nil
}

// Commit deletion before draining in-flight creation/start/file writes, so
// credential revocation is immediate while cleanup remains serialized.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	if err := s.store.MarkUserDeleting(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	unlock, lockErr := s.lockUserContext(ctx, id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	sessions, err := s.store.ListSessionsByUser(ctx, id)
	if err == nil {
		var failures []error
		for _, sess := range sessions {
			if e := s.deleteLocked(ctx, sess); e != nil {
				failures = append(failures, fmt.Errorf("%s: %w", sess.ID, e))
			}
		}
		err = errors.Join(failures...)
	}
	if err == nil {
		err = s.store.DeleteUser(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		_ = s.store.SetUserDeletionError(context.WithoutCancel(ctx), id, err.Error())
	}
	return err
}

func (s *Service) recordFailure(ctx context.Context, sess *model.Session, status model.SessionStatus, cause error) error {
	err := s.store.UpdateSessionState(context.WithoutCancel(ctx), sess.ID, status, sess.DesiredState, cause.Error())
	s.event(context.WithoutCancel(ctx), sess.ID, model.EventError, fmt.Sprintf(`{"error":%q}`, cause.Error()))
	return errors.Join(cause, err)
}

func (s *Service) event(ctx context.Context, id string, kind model.SessionEventType, payload string) {
	_ = s.store.CreateSessionEvent(ctx, &model.SessionEvent{SessionID: id, Type: kind, PayloadJSON: payload})
}

var safeID = regexp.MustCompile(`^sess_[A-Za-z0-9_-]+$`)

func (s *Service) dataRoot(id string) (*os.Root, error) {
	if !safeID.MatchString(id) {
		return nil, fmt.Errorf("unsafe instance id")
	}
	root, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return nil, err
	}
	for _, part := range []string{"sessions", filepath.Join("sessions", id)} {
		fi, err := root.Lstat(part)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			root.Close()
			return nil, err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("unsafe instance directory: %s", part)
		}
	}
	return root, nil
}

func (s *Service) prepareDirectories(sess *model.Session, create bool) error {
	root, err := s.dataRoot(sess.ID)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"profile", "downloads"} {
		path := filepath.Join("sessions", sess.ID, name)
		if create {
			if err := root.MkdirAll(path, 0755); err != nil {
				return err
			}
		}
		fi, err := root.Lstat(path)
		if err != nil {
			return fmt.Errorf("persistent %s unavailable; refusing empty replacement: %w", name, err)
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe persistent directory: %s", path)
		}
		// The manager runs as root in Compose while Chromium runs as the
		// image's fixed UID/GID 1000. Bind mounts hide image-layer ownership.
		// Only initialize directory ownership; never recursively rewrite data.
		if create && os.Geteuid() == 0 {
			if err := root.Chown(path, 1000, 1000); err != nil {
				return fmt.Errorf("prepare browser directory ownership: %w", err)
			}
		}
	}
	return nil
}

func (s *Service) removeDirectories(id string) error {
	root, err := s.dataRoot(id)
	if err != nil {
		return err
	}
	defer root.Close()
	// Root prevents intermediate symlinks escaping the data tree. RemoveAll
	// unlinks symlinks inside profiles rather than following them.
	return root.RemoveAll(filepath.Join("sessions", id))
}

func (s *Service) verifyContainer(sess *model.Session, info *docker.ContainerInfo) error {
	if info.Labels["remotebrowser.managed"] != "true" || info.Labels["remotebrowser.session.id"] != sess.ID {
		return fmt.Errorf("container ownership cannot be verified for %s", sess.ID)
	}
	if v := info.Labels["remotebrowser.user.id"]; v != "" && v != fmt.Sprint(sess.UserID) {
		return fmt.Errorf("container user ownership mismatch")
	}
	if v := info.Labels["remotebrowser.deployment.id"]; v != "" && v != s.deploymentID {
		return fmt.Errorf("container deployment ownership mismatch")
	}
	for destination, name := range map[string]string{"/home/rbuser/profile": "profile", "/home/rbuser/Downloads": "downloads"} {
		expected := filepath.Clean(filepath.Join(s.hostDataDir, "sessions", sess.ID, name))
		actual := info.Mounts[destination]
		if name == "downloads" && actual == "" {
			actual = info.Mounts["/home/rbuser/downloads"]
		}
		if actual == "" || filepath.Clean(actual) != expected {
			return fmt.Errorf("container persistent mount mismatch for %s", name)
		}
	}
	return nil
}

func (s *Service) GetSession(ctx context.Context, id string) (*model.Session, error) {
	sess, err := s.store.GetSession(ctx, id)
	if sess != nil {
		sess.ConnectedCount = s.ConnectedCount(id)
	}
	return sess, err
}
func (s *Service) ListSessions(ctx context.Context, userID int64) ([]*model.Session, error) {
	sessions, err := s.store.ListSessionsByUser(ctx, userID)
	for _, sess := range sessions {
		sess.ConnectedCount = s.ConnectedCount(sess.ID)
	}
	return sessions, err
}
func (s *Service) ValidateSessionToken(sess *model.Session, token string) bool {
	return bcrypt.CompareHashAndPassword([]byte(sess.SessionTokenHash), []byte(token)) == nil
}
func (s *Service) UpdateLastActive(ctx context.Context, id string) error {
	return s.store.UpdateSessionLastActive(ctx, id)
}
func (s *Service) ConnectedCount(id string) int {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	return s.connections[id]
}

var ErrSessionNotRunning = errors.New("instance is not running")

// ConnectionOpened returns nil only after adding exactly one connection.
// Callers must pair ConnectionClosed only with a successful open; failed or
// cancelled opens must never consume another transport's live connection.
func (s *Service) ConnectionOpened(ctx context.Context, id string) error {
	sess, err := s.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if sess == nil {
		return store.ErrNotFound
	}
	if sess.Status != model.SessionRunning {
		return ErrSessionNotRunning
	}
	// Perform all fallible writes before incrementing. An error after the
	// increment would otherwise make it impossible to balance a caller's close.
	if err := s.store.UpdateSessionLastActive(ctx, id); err != nil {
		return err
	}
	s.connectionsMu.Lock()
	s.connections[id]++
	first := s.connections[id] == 1
	s.connectionsMu.Unlock()
	if first {
		s.event(ctx, id, model.EventConnected, "")
	}
	return nil
}
func (s *Service) ConnectionClosed(ctx context.Context, id string) error {
	s.connectionsMu.Lock()
	count := s.connections[id]
	if count > 1 {
		s.connections[id]--
	} else {
		delete(s.connections, id)
	}
	s.connectionsMu.Unlock()
	if count == 0 {
		return nil
	}
	if count == 1 {
		s.event(ctx, id, model.EventDisconnected, "")
	}
	return s.store.UpdateSessionLastActive(ctx, id)
}
func generateID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
func generateToken() string { b := make([]byte, 32); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func allocateUDPPort() (string, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return "", fmt.Errorf("allocate udp port: %w", err)
	}
	defer conn.Close()
	return fmt.Sprint(conn.LocalAddr().(*net.UDPAddr).Port), nil
}
