package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/robertji666/RemoteBrowser/internal/model"
)

func TestNewLockedExcludesManagersAndReleasesOnce(t *testing.T) {
	dir := t.TempDir()
	first, err := NewLocked(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := NewLocked(dir); err == nil {
		second.Close()
		t.Fatal("second manager acquired lock")
	}
	// Administrative operations can use a separate transactional connection.
	cli, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewLocked(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if third, err := NewLocked(dir); err == nil {
		third.Close()
		t.Fatal("repeated Close released another manager's lock")
	}
}

func TestRollbackJournalPreservesDataAcrossReopens(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var userID int64
	for restart := 0; restart < 8; restart++ {
		s, err := NewLocked(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MigrateWithDefaults(ctx, 5+restart); err != nil {
			s.Close()
			t.Fatal(err)
		}
		var journal string
		if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
			s.Close()
			t.Fatal(err)
		}
		if journal != "delete" {
			s.Close()
			t.Fatalf("journal mode=%s", journal)
		}
		if restart == 0 {
			u := testUser(t, s, "persistent@example.com", 2)
			userID = u.ID
			v := testSession(u, "persistent-browser", model.SessionStopped)
			v.DesiredState = "stopped"
			if err := s.CreateSession(ctx, v); err != nil {
				s.Close()
				t.Fatal(err)
			}
		} else {
			u, err := s.GetUserByID(ctx, userID)
			if err != nil || u == nil {
				s.Close()
				t.Fatalf("user missing at restart %d: %+v %v", restart, u, err)
			}
			if u.PasswordHash != "test-hash" || u.InstanceQuota != 2 || u.InstanceCount != 1 {
				s.Close()
				t.Fatalf("user changed after restart: %+v", u)
			}
			v, err := s.GetSession(ctx, "persistent-browser")
			if err != nil || v == nil {
				s.Close()
				t.Fatalf("instance missing: %+v %v", v, err)
			}
			if v.DesiredState != "stopped" || v.UserID != userID || v.ProfileDir != "/isolated/persistent-browser/profile" {
				s.Close()
				t.Fatalf("instance changed: %+v", v)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "manager.db"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
			t.Fatalf("invalid database header after restart %d", restart)
		}
	}
}

func TestCorruptDatabaseRejectedWithoutResetAndLockReleased(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manager.db")
	corrupt := make([]byte, 96*1024)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := NewLocked(dir); err == nil {
		s.Close()
		t.Fatal("zeroed nonempty database was accepted")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, corrupt) {
		t.Fatal("opening corrupt database modified it")
	}
	// The failed open must release its service lock; after restoring a valid
	// database from an independently created fixture, startup can be retried.
	fixture := t.TempDir()
	s, err := New(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(fixture, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, restored, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = NewLocked(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}
