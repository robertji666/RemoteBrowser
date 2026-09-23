package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound         = errors.New("record not found")
	ErrConflict         = errors.New("record changed; reload and retry")
	ErrQuotaExceeded    = errors.New("browser instance quota exceeded")
	ErrUserInactive     = errors.New("user is not active")
	ErrUserProtected    = errors.New("administrator account is protected")
	ErrEmailExists      = errors.New("email already exists")
	ErrUserHasInstances = errors.New("user still owns browser instances")
)

type Store struct {
	db        *sql.DB
	lockFile  *os.File
	closeOnce sync.Once
	closeErr  error
}

func New(dataDir string) (*Store, error) {
	return open(dataDir, nil)
}

// NewLocked prevents two long-running managers from independently reconciling
// the same instances. Short-lived administrative commands use New and rely on
// SQLite transactions rather than acquiring this service-ownership lock.
func NewLocked(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dataDir, "manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open manager lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("another manager is already using data directory: %w", err)
	}
	s, err := open(dataDir, lock)
	if err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	return s, nil
}

func open(dataDir string, lock *os.File) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dbPath, err := filepath.Abs(filepath.Join(dataDir, "manager.db"))
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: dbPath}
	// Use rollback journaling for this single-manager database. Its behavior on
	// the deployment filesystem must still be validated with restart tests.
	db, err := sql.Open("sqlite", u.String()+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(delete)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// One connection avoids deferred-lock upgrades within this process; the
	// immediate transaction and busy timeout also serialize independent managers.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA quick_check(1)").Scan(&integrity); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("check database integrity: %w", err)
	}
	if integrity != "ok" {
		_ = db.Close()
		return nil, fmt.Errorf("database integrity check failed: %s", integrity)
	}
	return &Store{db: db, lockFile: lock}, nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
		if s.lockFile != nil {
			s.closeErr = errors.Join(s.closeErr, syscall.Flock(int(s.lockFile.Fd()), syscall.LOCK_UN), s.lockFile.Close())
		}
	})
	return s.closeErr
}
func (s *Store) DB() *sql.DB { return s.db }

type scanner interface{ Scan(dest ...any) error }

func affected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
