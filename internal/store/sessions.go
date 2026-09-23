package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/model"
)

const sessionColumns = `id,user_id,status,container_name,profile_dir,downloads_dir,session_token_hash,last_active_at,created_at,expired_at,name,desired_state,last_error,COALESCE(create_request_key,''),recovery_attempts,recovery_next_at`

func scanSession(row scanner) (*model.Session, error) {
	var v model.Session
	var active, expired, recovery sql.NullTime
	err := row.Scan(&v.ID, &v.UserID, &v.Status, &v.ContainerName, &v.ProfileDir, &v.DownloadsDir, &v.SessionTokenHash, &active, &v.CreatedAt, &expired, &v.Name, &v.DesiredState, &v.LastError, &v.CreateRequestKey, &v.RecoveryAttempts, &recovery)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if active.Valid {
		v.LastActiveAt = &active.Time
	}
	if expired.Valid {
		v.ExpiredAt = &expired.Time
	}
	if recovery.Valid {
		v.RecoveryNextAt = &recovery.Time
	}
	return &v, nil
}

func insertSession(ctx context.Context, tx *sql.Tx, v *model.Session) error {
	if v.Status == "" {
		v.Status = model.SessionCreating
	}
	if v.DesiredState == "" {
		v.DesiredState = "running"
	}
	if v.Name == "" {
		v.Name = v.ID
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	var key any
	if v.CreateRequestKey != "" {
		key = v.CreateRequestKey
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions
 (id,user_id,status,container_name,profile_dir,downloads_dir,session_token_hash,last_active_at,created_at,expired_at,name,desired_state,last_error,create_request_key,recovery_attempts,recovery_next_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.UserID, v.Status, v.ContainerName, v.ProfileDir, v.DownloadsDir, v.SessionTokenHash, v.LastActiveAt, v.CreatedAt, v.ExpiredAt, v.Name, v.DesiredState, v.LastError, key, v.RecoveryAttempts, v.RecoveryNextAt)
	return err
}

// CreateSession is retained for older callers and enforces the same reservation
// semantics. New callers need the returned existing instance and created flag.
func (s *Store) CreateSession(ctx context.Context, v *model.Session) error {
	result, _, err := s.CreateSessionWithinQuota(ctx, v, v.CreateRequestKey)
	if err == nil {
		*v = *result
	}
	return err
}

// CreateSessionWithinQuota locks the quota, account status, request key and
// placeholder insertion together. Every persisted row occupies a slot until
// resource cleanup succeeds and DeleteSession removes the row.
func (s *Store) CreateSessionWithinQuota(ctx context.Context, v *model.Session, key string) (*model.Session, bool, error) {
	if v == nil || v.ID == "" {
		return nil, false, fmt.Errorf("instance ID is required")
	}
	if key == "" {
		key = v.CreateRequestKey
	}
	var result *model.Session
	created := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var quota int
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT instance_quota,status FROM users WHERE id=?`, v.UserID).Scan(&quota, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != model.UserActive {
			return ErrUserInactive
		}
		if key != "" {
			var existingID sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT session_id FROM instance_requests WHERE user_id=? AND request_key=?`, v.UserID, key).Scan(&existingID)
			if err == nil {
				// Retain the request key after deletion so an old POST cannot
				// silently create a replacement instance.
				if !existingID.Valid {
					return ErrConflict
				}
				result, err = scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=?`, existingID.String))
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, v.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= quota {
			return ErrQuotaExceeded
		}
		v.CreateRequestKey = key
		if err := insertSession(ctx, tx, v); err != nil {
			return err
		}
		if key != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO instance_requests(user_id,request_key,session_id) VALUES(?,?,?)`, v.UserID, key, v.ID); err != nil {
				return err
			}
		}
		result = v
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, created, nil
}

func (s *Store) GetSession(ctx context.Context, id string) (*model.Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=?`, id))
}
func (s *Store) ListSessionsByUser(ctx context.Context, userID int64) ([]*model.Session, error) {
	query := `SELECT ` + sessionColumns + ` FROM sessions`
	var args []any
	if userID > 0 {
		query += ` WHERE user_id=?`
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY created_at DESC,id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*model.Session
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) ListAllSessions(ctx context.Context) ([]*model.Session, error) {
	return s.ListSessionsByUser(ctx, 0)
}

func (s *Store) UpdateSessionStatus(ctx context.Context, id string, status model.SessionStatus) error {
	return affected(s.db.ExecContext(ctx, `UPDATE sessions SET status=? WHERE id=?`, status, id))
}
func (s *Store) UpdateSessionState(ctx context.Context, id string, status model.SessionStatus, desired, lastError string) error {
	if desired == "" {
		return affected(s.db.ExecContext(ctx, `UPDATE sessions SET status=?,last_error=? WHERE id=?`, status, lastError, id))
	}
	return affected(s.db.ExecContext(ctx, `UPDATE sessions SET status=?,desired_state=?,last_error=?,expired_at=NULL WHERE id=?`, status, desired, lastError, id))
}
func (s *Store) UpdateSessionLifecycle(ctx context.Context, id string, status model.SessionStatus, desired, lastError string) error {
	return s.UpdateSessionState(ctx, id, status, desired, lastError)
}
func (s *Store) UpdateSessionRecovery(ctx context.Context, id string, attempts int, nextAt *time.Time) error {
	return affected(s.db.ExecContext(ctx, `UPDATE sessions SET recovery_attempts=?,recovery_next_at=? WHERE id=?`, attempts, nextAt, id))
}
func (s *Store) UpdateSessionRecoveryAttempts(ctx context.Context, id string, attempts int) error {
	return affected(s.db.ExecContext(ctx, `UPDATE sessions SET recovery_attempts=? WHERE id=?`, attempts, id))
}
func (s *Store) UpdateSessionTokenHash(ctx context.Context, id, hash string) error {
	return affected(s.db.ExecContext(ctx, `UPDATE sessions SET session_token_hash=? WHERE id=?`, hash, id))
}
func (s *Store) UpdateSessionLastActive(ctx context.Context, id string) error {
	return affected(s.db.ExecContext(ctx, `UPDATE sessions SET last_active_at=CURRENT_TIMESTAMP WHERE id=?`, id))
}

// UpdateSessionExpired is a compatibility guard: idle expiration is no longer
// permitted, even if an older caller is accidentally retained.
func (s *Store) UpdateSessionExpired(ctx context.Context, id string) error {
	return fmt.Errorf("idle expiration is no longer supported")
}
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}
func (s *Store) CountSessions(ctx context.Context, userID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&count)
	return count, err
}
func (s *Store) CountActiveSessions(ctx context.Context, userID int64) (int, error) {
	return s.CountSessions(ctx, userID)
}
