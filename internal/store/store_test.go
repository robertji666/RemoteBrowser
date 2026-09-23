package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.MigrateWithDefaults(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	return s
}
func testUser(t *testing.T, s *Store, email string, quota int) *model.User {
	t.Helper()
	u := &model.User{Email: email, PasswordHash: "test-hash", InstanceQuota: quota}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}
func testSession(u *model.User, id string, status model.SessionStatus) *model.Session {
	return &model.Session{ID: id, UserID: u.ID, Status: status, ContainerName: "rb-" + id, ProfileDir: "/isolated/" + id + "/profile", DownloadsDir: "/isolated/" + id + "/downloads", SessionTokenHash: "hash"}
}

func TestUpgradeLegacyDatabasePreservesIdentityAndOwnership(t *testing.T) {
	ctx := context.Background()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(baseSchema + filesSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO users(id,username,password_hash) VALUES(42,'admin','original-hash'),(43,'old@example.com','user-hash');
 INSERT INTO sessions(id,user_id,status,container_name,profile_dir,downloads_dir,session_token_hash) VALUES
 ('old-expired',42,'expired','old-container','/keep/profile','/keep/downloads','token'),
 ('old-disconnected',42,'disconnected','old-running','/keep/other-profile','/keep/other-downloads','other-token');`); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateWithDefaults(ctx, 1); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUserByID(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 42 || u.PasswordHash != "original-hash" || u.Role != model.RoleAdmin || u.InstanceQuota != 1 || u.InstanceCount != 2 || u.MustChangePassword {
		t.Fatalf("unexpected migrated admin: %+v", u)
	}
	legacy, err := s.GetSession(ctx, "old-expired")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Status != model.SessionExpired || legacy.DesiredState != "legacy_expired" || legacy.ProfileDir != "/keep/profile" || legacy.UserID != 42 {
		t.Fatalf("legacy resources changed: %+v", legacy)
	}
	running, err := s.GetSession(ctx, "old-disconnected")
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != model.SessionDisconnected || running.DesiredState != "running" {
		t.Fatalf("disconnected migration: %+v", running)
	}
	if _, _, err := s.CreateSessionWithinQuota(ctx, testSession(u, "over-limit", model.SessionCreating), "new"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("over-limit legacy user: %v", err)
	}
	if err := s.UpdateUserQuota(ctx, 42, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUserPassword(ctx, 42, "new-hash", false); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateWithDefaults(ctx, 99); err != nil {
		t.Fatal(err)
	}
	u, err = s.GetUserByID(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if u.InstanceQuota != 7 || u.PasswordHash != "new-hash" || u.AuthVersion != 2 {
		t.Fatalf("migration reapplied defaults: %+v", u)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("migration count=%d", count)
	}
	old, err := s.GetUserByEmail(ctx, "OLD@example.com")
	if err != nil || old == nil || old.ID != 43 {
		t.Fatalf("old email mapping: %+v %v", old, err)
	}
}

func TestQuotaConcurrentAcrossDatabaseConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	a, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Migrate(); err != nil {
		t.Fatal(err)
	}
	b, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	u := testUser(t, a, "concurrent@example.com", 1)
	var success atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st := a
			if i%2 == 1 {
				st = b
			}
			_, created, err := st.CreateSessionWithinQuota(ctx, testSession(u, fmt.Sprintf("concurrent-%d", i), model.SessionCreating), fmt.Sprint(i))
			if err == nil && created {
				success.Add(1)
			} else if !errors.Is(err, ErrQuotaExceeded) {
				errs <- fmt.Errorf("created=%v error=%v", created, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	count, err := a.CountSessions(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if success.Load() != 1 || count != 1 {
		t.Fatalf("success=%d rows=%d", success.Load(), count)
	}
}

func TestCreationIdempotencyAndCompletedDeletion(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := testUser(t, s, "idempotent@example.com", 1)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	var createdCount atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, created, err := s.CreateSessionWithinQuota(ctx, testSession(u, fmt.Sprintf("request-%d", i), model.SessionCreating), "one-click")
			if err != nil {
				errs <- err
				return
			}
			if v == nil {
				errs <- errors.New("nil result")
			}
			if created {
				createdCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if createdCount.Load() != 1 {
		t.Fatalf("new reservations=%d", createdCount.Load())
	}
	rows, err := s.ListSessionsByUser(ctx, u.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d error=%v", len(rows), err)
	}
	if err := s.DeleteSession(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateSessionWithinQuota(ctx, testSession(u, "resurrect", model.SessionCreating), "one-click"); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted request was replayed: %v", err)
	}
	if _, _, err := s.CreateSessionWithinQuota(ctx, testSession(u, "fresh", model.SessionCreating), "fresh-click"); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaCountsAllStatesAndLoweringRetainsInstances(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := testUser(t, s, "quota@example.com", 3)
	for i, status := range []model.SessionStatus{model.SessionRunning, model.SessionStopped, model.SessionError} {
		if err := s.CreateSession(ctx, testSession(u, fmt.Sprint(i), status)); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.InstanceCount != 3 {
		t.Fatalf("instance count=%d", u.InstanceCount)
	}
	if err := s.UpdateUserQuota(ctx, u.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateSessionWithinQuota(ctx, testSession(u, "extra", model.SessionCreating), "extra"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected quota rejection: %v", err)
	}
	rows, err := s.ListSessionsByUser(ctx, u.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("quota reduction removed resources: %d %v", len(rows), err)
	}
	if err := s.UpdateUserQuota(ctx, u.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUserQuota(ctx, u.ID, -1); err == nil {
		t.Fatal("negative quota accepted")
	}
	if err := s.UpdateUserQuota(ctx, u.ID, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, testSession(u, "extra", model.SessionCreating)); err != nil {
		t.Fatal(err)
	}
}

func TestUserDeletionFreezesBeforeResourcesAndPreservesAudit(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := testUser(t, s, "delete@example.com", 2)
	other := testUser(t, s, "other@example.com", 1)
	v := testSession(u, "delete-owned", model.SessionRunning)
	if err := s.CreateSession(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, testSession(other, "other-owned", model.SessionRunning)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLoginSession(ctx, "login", u.ID, u.AuthVersion, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMailDelivery(ctx, &model.MailDelivery{UserID: u.ID, Kind: "welcome", Status: "sent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAuditEvent(ctx, &model.AuditEvent{ActorID: 99, TargetUserID: u.ID, Action: "delete_user", Result: "started"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); !errors.Is(err, ErrUserInactive) {
		t.Fatalf("active user deleted: %v", err)
	}
	if err := s.MarkUserDeleting(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if valid, err := s.LoginSessionValid(ctx, "login", u.ID, u.AuthVersion); err != nil || valid {
		t.Fatalf("deleting user's login remained valid: %v %v", valid, err)
	}
	if err := s.DeleteUser(ctx, u.ID); !errors.Is(err, ErrUserHasInstances) {
		t.Fatalf("deleted before resource cleanup: %v", err)
	}
	if err := s.CreateSession(ctx, testSession(u, "new", model.SessionCreating)); !errors.Is(err, ErrUserInactive) {
		t.Fatalf("deleting user created instance: %v", err)
	}
	if err := s.UpdateUserPassword(ctx, u.ID, "new", false); err == nil {
		t.Fatal("deleting user changed password")
	}
	if err := s.SetUserDeletionError(ctx, u.ID, "Docker unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserStatus(ctx, u.ID, model.UserActive); !errors.Is(err, ErrUserInactive) {
		t.Fatalf("delete_failed user reactivated: %v", err)
	}
	frozen, err := s.GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != model.UserDeleteFailed || frozen.InstanceCount != 1 {
		t.Fatalf("failure lost records: %+v", frozen)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, u.ID); err == nil {
		t.Fatal("foreign key allowed orphaning session")
	}
	if err := s.DeleteSession(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	replacement := testUser(t, s, u.Email, 1)
	if replacement.ID == u.ID {
		t.Fatal("deleted ID reused")
	}
	if count, err := s.CountSessions(ctx, replacement.ID); err != nil || count != 0 {
		t.Fatalf("replacement inherited instances: %d %v", count, err)
	}
	if count, err := s.CountSessions(ctx, other.ID); err != nil || count != 1 {
		t.Fatalf("other owner affected: %d %v", count, err)
	}
	audit, err := s.ListAuditEvents(ctx, 10)
	if err != nil || len(audit) != 1 || audit[0].TargetUserID != u.ID {
		t.Fatalf("audit lost: %+v %v", audit, err)
	}
	var mailOwner sql.NullInt64
	if err := s.db.QueryRow(`SELECT user_id FROM mail_deliveries`).Scan(&mailOwner); err != nil {
		t.Fatal(err)
	}
	if mailOwner.Valid {
		t.Fatal("deleted email association retained")
	}
}

func TestUserUniquenessFilteringAndAdminProtection(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := testUser(t, s, "  NAME@Example.COM ", 1)
	if u.Email != "name@example.com" || u.Username != u.Email {
		t.Fatalf("not normalized: %+v", u)
	}
	if err := s.CreateUser(ctx, &model.User{Email: "Name@EXAMPLE.com", PasswordHash: "hash"}); !errors.Is(err, ErrEmailExists) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	for _, invalid := range []string{"", "missing-at", "Person <person@example.com>", "a@example.com\r\nBcc: b@example.com"} {
		if err := s.CreateUser(ctx, &model.User{Email: invalid, PasswordHash: "hash"}); err == nil {
			t.Errorf("invalid email accepted: %q", invalid)
		}
	}
	testUser(t, s, "percent%test@example.com", 0)
	testUser(t, s, "third@example.com", 0)
	users, total, err := s.ListUsers(ctx, UserFilter{Search: "%", Limit: 1})
	if err != nil || total != 1 || len(users) != 1 || users[0].Email != "percent%test@example.com" {
		t.Fatalf("literal search: %+v %d %v", users, total, err)
	}
	if err := s.SetUserStatus(ctx, u.ID, model.UserDisabled); err != nil {
		t.Fatal(err)
	}
	users, total, err = s.ListUsers(ctx, UserFilter{Status: model.UserDisabled, Limit: 5})
	if err != nil || len(users) != 1 || total != 1 {
		t.Fatalf("status filter: %d %d %v", len(users), total, err)
	}
	_, total, err = s.ListUsers(ctx, UserFilter{Limit: 1, Offset: 1})
	if err != nil || total != 3 {
		t.Fatalf("pagination total=%d %v", total, err)
	}
	admin := &model.User{Username: "admin", PasswordHash: "hash", InstanceQuota: 3}
	if err := s.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() error{func() error { return s.MarkUserDeleting(ctx, admin.ID) }, func() error { return s.DeleteUser(ctx, admin.ID) }, func() error { return s.SetUserStatus(ctx, admin.ID, model.UserDisabled) }} {
		if err := operation(); !errors.Is(err, ErrUserProtected) {
			t.Fatalf("administrator protection: %v", err)
		}
	}
}

func TestLoginRevocationCASAndExpiration(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := testUser(t, s, "login@example.com", 1)
	if err := s.CreateLoginSession(ctx, "first", u.ID, u.AuthVersion, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.LoginSessionValid(ctx, "first", u.ID, u.AuthVersion); err != nil || !ok {
		t.Fatalf("fresh login: %v %v", ok, err)
	}
	if err := s.UpdateUserPassword(ctx, u.ID, "admin-reset", true); err != nil {
		t.Fatal(err)
	}
	if err := s.CompareAndUpdateUserPassword(ctx, u.ID, u.AuthVersion, "stale-change", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS overwrote reset: %v", err)
	}
	if ok, err := s.LoginSessionValid(ctx, "first", u.ID, u.AuthVersion); err != nil || ok {
		t.Fatalf("reset failed to revoke: %v %v", ok, err)
	}
	u, err := s.GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.PasswordHash != "admin-reset" || u.AuthVersion != 2 || !u.MustChangePassword {
		t.Fatalf("reset metadata: %+v", u)
	}
	if err := s.CreateLoginSession(ctx, "second", u.ID, u.AuthVersion, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeLoginSession(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.LoginSessionValid(ctx, "second", u.ID, u.AuthVersion); err != nil || ok {
		t.Fatalf("logout failed: %v %v", ok, err)
	}
	if err := s.CreateLoginSession(ctx, "expired", u.ID, u.AuthVersion, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.LoginSessionValid(ctx, "expired", u.ID, u.AuthVersion); err != nil || ok {
		t.Fatalf("expired login allowed: %v %v", ok, err)
	}
	if err := s.SetUserStatus(ctx, u.ID, model.UserDisabled); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLoginSession(ctx, "disabled", u.ID, u.AuthVersion+1, time.Now().Add(time.Hour)); !errors.Is(err, ErrUserInactive) {
		t.Fatalf("disabled login created: %v", err)
	}
}

func TestAccountDeletionRacingReservationsRetainsAllAcceptedOwnership(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := testUser(t, s, "freeze-race@example.com", 40)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, created, err := s.CreateSessionWithinQuota(ctx, testSession(u, fmt.Sprintf("freeze-%d", i), model.SessionCreating), fmt.Sprint(i))
			if err == nil && created {
				accepted.Add(1)
			} else if !errors.Is(err, ErrUserInactive) {
				errs <- fmt.Errorf("reservation: %v", err)
			}
		}(i)
	}
	close(start)
	if err := s.MarkUserDeleting(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := s.CreateSessionWithinQuota(ctx, testSession(u, fmt.Sprintf("late-%d", i), model.SessionCreating), fmt.Sprintf("late-%d", i)); !errors.Is(err, ErrUserInactive) {
			t.Fatalf("reservation after freeze: %v", err)
		}
	}
	frozen, err := s.GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != model.UserDeleting || frozen.InstanceCount != int(accepted.Load()) {
		t.Fatalf("lost ownership after race: %+v accepted=%d", frozen, accepted.Load())
	}
}

func TestMigrationFailureRollsBackSchemaAndData(t *testing.T) {
	ctx := context.Background()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(baseSchema + filesSchema); err != nil {
		t.Fatal(err)
	}
	// Old usernames were case sensitive; the new canonical email collision
	// must stop upgrade atomically rather than lose an account.
	if _, err := s.db.Exec(`INSERT INTO users(username,password_hash) VALUES('same@example.com','first'),('SAME@example.com','second')`); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateWithDefaults(ctx, 3); err == nil {
		t.Fatal("ambiguous legacy emails migrated")
	}
	var hashes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE password_hash IN ('first','second')`).Scan(&hashes); err != nil || hashes != 2 {
		t.Fatalf("migration changed original accounts: %d %v", hashes, err)
	}
	var addedColumn int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='email'`).Scan(&addedColumn); err != nil || addedColumn != 0 {
		t.Fatalf("partial migration left columns: %d %v", addedColumn, err)
	}
	if _, err := s.db.Exec(`UPDATE users SET username='other@example.com' WHERE username='SAME@example.com'`); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateWithDefaults(ctx, 3); err != nil {
		t.Fatalf("migration cannot retry after correcting legacy collision: %v", err)
	}
}
