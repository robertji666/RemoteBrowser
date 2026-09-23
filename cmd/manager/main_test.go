package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func managerTestEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("RB_DATA_DIR", dir)
	t.Setenv("RB_SESSION_HOST_DATA_DIR", dir)
	t.Setenv("RB_COOKIE_SECRET", strings.Repeat("s", 32))
	t.Setenv("RB_DEFAULT_INSTANCE_QUOTA", "5")
	t.Setenv("RB_ADMIN_PASSWORD", "")
	t.Setenv("RB_RESET_ADMIN_PASSWORD", "")
	t.Setenv("RB_SMTP_TIMEOUT", "")
	t.Setenv("RB_PUBLISH_SESSION_TCP_PORTS", "false")
}

func TestCLIResetWorksWhileManagerOwnsDirectory(t *testing.T) {
	for _, source := range []string{"environment", "stdin"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			managerTestEnv(t, dir)
			s, err := store.NewLocked(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			oldHash, err := bcrypt.GenerateFromPassword([]byte("old-admin-password"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			u := &model.User{Username: "admin", PasswordHash: string(oldHash), InstanceQuota: 3}
			if err := s.CreateUser(context.Background(), u); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateLoginSession(context.Background(), "old-login", u.ID, u.AuthVersion, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			password := "new-admin-password"
			input := ""
			if source == "environment" {
				t.Setenv("RB_RESET_ADMIN_PASSWORD", password)
			} else {
				input = password + "\n"
			}
			if err := run([]string{"-reset-admin-password"}, strings.NewReader(input)); err != nil {
				t.Fatal(err)
			}
			updated, err := s.GetUserByID(context.Background(), u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte(password)); err != nil {
				t.Fatalf("reset password unusable: %v", err)
			}
			if updated.AuthVersion != u.AuthVersion+1 {
				t.Fatal("reset did not revoke credential version")
			}
			if valid, err := s.LoginSessionValid(context.Background(), "old-login", u.ID, u.AuthVersion); err != nil || valid {
				t.Fatalf("old login survived: %v %v", valid, err)
			}
			if other, err := store.NewLocked(dir); err == nil {
				other.Close()
				t.Fatal("CLI reset released the running manager's lock")
			}
		})
	}
}

func TestFailedMigrationClosesDatabaseAndReleasesManagerLock(t *testing.T) {
	dir := t.TempDir()
	managerTestEnv(t, dir)
	s, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO schema_migrations(version) VALUES(99)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-migrate-only"}, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("unexpected failed migration: %v", err)
	}
	s, err = store.NewLocked(dir)
	if err != nil {
		t.Fatalf("failed run retained database lock: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.DB().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 99 {
		t.Fatalf("failed migration modified version: %d %v", version, err)
	}
}

func TestAdministratorInitFailureClosesDatabase(t *testing.T) {
	dir := t.TempDir()
	managerTestEnv(t, dir)
	if err := run(nil, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "first deployment requires") {
		t.Fatalf("unexpected missing-password error: %v", err)
	}
	s, err := store.NewLocked(dir)
	if err != nil {
		t.Fatalf("initialization failure retained lock: %v", err)
	}
	defer s.Close()
}

func TestBackgroundShutdownCancelsBeforeWaiting(t *testing.T) {
	appCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	workerStopped := false
	err := shutdownBackground(context.Background(), cancel, func(context.Context) error {
		if appCtx.Err() == nil {
			t.Fatal("workers were not cancelled before wait")
		}
		workerStopped = true
		close(done)
		return nil
	}, done)
	if err != nil || !workerStopped {
		t.Fatalf("shutdown result: %v", err)
	}
}

func TestBackgroundShutdownDoesNotReportSafeDatabaseCloseOnTimeout(t *testing.T) {
	for _, target := range []string{"worker", "maintenance"} {
		t.Run(target, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			neverDone := make(chan struct{})
			err := shutdownBackground(ctx, func() {}, func(ctx context.Context) error {
				if target == "worker" {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}, neverDone)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unfinished %s reported safe database close: %v", target, err)
			}
		})
	}
}
