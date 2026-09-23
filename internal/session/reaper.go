package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/docker"
	"github.com/robertji666/RemoteBrowser/internal/model"
)

const maxRecoveryAttempts = 3

// StartReaper is a compatibility alias. No time-based expiry remains.
func (s *Service) StartReaper(ctx context.Context) { s.StartMaintenance(ctx) }

func (s *Service) StartMaintenance(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("instance reconciliation incomplete", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) ReconcileOnce(ctx context.Context) error {
	var failures []error
	users, err := s.store.ListDeletingUsers(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := s.DeleteUser(ctx, u.ID); err != nil {
			failures = append(failures, err)
		}
	}
	sessions, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, sess := range sessions {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.withSession(ctx, sess.ID, false, func(current *model.Session) error { return s.reconcileLocked(ctx, current) })
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", sess.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) reconcileLocked(ctx context.Context, sess *model.Session) error {
	u, err := s.store.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("instance owner missing")
	}
	if u.Status == "deleting" || u.Status == "delete_failed" {
		return nil
	}
	if sess.DesiredState == "deleted" || sess.Status == model.SessionDeleting {
		return s.deleteLocked(ctx, sess)
	}
	// Preserve the legacy meaning before an outage can change the visible
	// status to unknown. This marker survives manager restarts during migration.
	if sess.DesiredState == "" {
		desired := "running"
		switch sess.Status {
		case model.SessionExpired:
			desired = "legacy_expired"
		case model.SessionStopped, model.SessionStopping:
			desired = "stopped"
		}
		sess.DesiredState = desired
		if err := s.store.UpdateSessionState(ctx, sess.ID, sess.Status, desired, ""); err != nil {
			return err
		}
	}
	info, inspectErr := s.docker.InspectContainer(ctx, sess.ContainerName)
	if inspectErr != nil && !docker.IsNotFound(inspectErr) {
		return s.recordFailure(ctx, sess, model.SessionUnknown, inspectErr)
	}
	if inspectErr == nil {
		if err := s.verifyContainer(sess, info); err != nil {
			return s.recordFailure(ctx, sess, model.SessionError, err)
		}
		if err := s.isolateContainerNetwork(ctx, sess, info); err != nil {
			return s.recordFailure(ctx, sess, model.SessionError, err)
		}
		if updater, ok := s.docker.(interface {
			SetPersistentRestartPolicy(context.Context, string) error
		}); ok && info.RestartPolicy != "unless-stopped" {
			if err := updater.SetPersistentRestartPolicy(ctx, info.ID); err != nil {
				return s.recordFailure(ctx, sess, model.SessionError, err)
			}
		}
	}
	if err := s.prepareDirectories(sess, false); err != nil {
		return s.recordFailure(ctx, sess, model.SessionError, err)
	}
	// Legacy expired records keep data and identity. A still-running legacy
	// container is recognized as running; otherwise expired becomes stopped.
	if sess.DesiredState == "legacy_expired" {
		desired := "stopped"
		if info != nil && info.Running {
			desired = "running"
		}
		sess.DesiredState = desired
		if err := s.store.UpdateSessionState(ctx, sess.ID, sess.Status, desired, ""); err != nil {
			return err
		}
	}
	if sess.DesiredState == "stopped" {
		if info == nil {
			return s.recordFailure(ctx, sess, model.SessionError, fmt.Errorf("container missing; persistent data retained, use Start to recreate"))
		}
		return s.stopLocked(ctx, sess)
	}
	if sess.DesiredState != "running" {
		return s.recordFailure(ctx, sess, model.SessionError, fmt.Errorf("unrecognized desired state"))
	}
	if info != nil && info.Running && (info.Health == "healthy" || info.Health == "") {
		if err := s.store.UpdateSessionState(ctx, sess.ID, model.SessionRunning, "running", ""); err != nil {
			return err
		}
		if sess.RecoveryAttempts != 0 {
			return s.store.UpdateSessionRecovery(ctx, sess.ID, 0, nil)
		}
		return nil
	}
	if sess.RecoveryAttempts >= maxRecoveryAttempts {
		// Docker's unless-stopped policy otherwise keeps retrying a crashing
		// process after the manager's recovery budget has been exhausted.
		// Stop that loop while retaining desired=running for an explicit retry.
		if info != nil && (info.Running || info.State == "restarting") {
			if err := s.docker.StopContainer(ctx, info.ID, 10); err != nil {
				return s.recordFailure(ctx, sess, model.SessionError, err)
			}
			return s.recordFailure(ctx, sess, model.SessionError, fmt.Errorf("automatic recovery exhausted; use Start to retry"))
		}
		if sess.LastError == "" {
			return s.recordFailure(ctx, sess, model.SessionError, fmt.Errorf("automatic recovery exhausted; use Start to retry"))
		}
		return nil
	}
	if sess.RecoveryNextAt != nil && time.Now().Before(*sess.RecoveryNextAt) {
		return nil
	}
	next := time.Now().Add(time.Duration(sess.RecoveryAttempts+1) * 30 * time.Second)
	if err := s.store.UpdateSessionRecovery(ctx, sess.ID, sess.RecoveryAttempts+1, &next); err != nil {
		return err
	}
	if info != nil && info.Running && info.Health == "unhealthy" {
		if err := s.docker.StopContainer(ctx, info.ID, 10); err != nil {
			return s.recordFailure(ctx, sess, model.SessionError, err)
		}
	}
	return s.startLocked(ctx, sess)
}

// Orphans are report-only. Ownership is never inferred from a similar name.
func (s *Service) ListOrphanContainers(ctx context.Context) ([]*docker.ContainerInfo, error) {
	sessions, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]*model.Session)
	for _, sess := range sessions {
		known[sess.ID] = sess
	}
	ids, err := s.docker.ListManagedContainers(ctx)
	if err != nil {
		return nil, err
	}
	var orphans []*docker.ContainerInfo
	for _, id := range ids {
		info, err := s.docker.InspectContainer(ctx, id)
		if docker.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if deployment := info.Labels["remotebrowser.deployment.id"]; deployment != "" && deployment != s.deploymentID {
			continue
		}
		sess := known[info.Labels["remotebrowser.session.id"]]
		if sess == nil || s.verifyContainer(sess, info) != nil {
			orphans = append(orphans, info)
		}
	}
	return orphans, nil
}
