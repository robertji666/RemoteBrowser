package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/robertji666/RemoteBrowser/migrations"
)

// Migrate remains available for older callers. Deployments should pass their
// former RB_MAX_SESSIONS value to MigrateWithDefaults exactly once on upgrade.
func (s *Store) Migrate() error { return s.MigrateWithDefaults(context.Background(), 3) }

func (s *Store) MigrateWithDefaults(ctx context.Context, defaultQuota int) error {
	if defaultQuota < 0 {
		return fmt.Errorf("default instance quota must be nonnegative")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
			return err
		}
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
			return err
		}
		if version > 3 {
			return fmt.Errorf("database schema version %d is newer than supported version 3", version)
		}
		if version < 1 {
			if _, err := tx.ExecContext(ctx, baseSchema); err != nil {
				return fmt.Errorf("migration 1: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES(1)`); err != nil {
				return err
			}
		}
		if version < 2 {
			if _, err := tx.ExecContext(ctx, filesSchema); err != nil {
				return fmt.Errorf("migration 2: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES(2)`); err != nil {
				return err
			}
		}
		if version < 3 {
			// Preserve old IDs, hashes, ownership and paths. Runtime reconciliation
			// verifies containers before replacing disconnected/expired states.
			if _, err := tx.ExecContext(ctx, migrations.PersistentUsersSchema); err != nil {
				return fmt.Errorf("migration 3: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET instance_quota = ?, role = CASE WHEN username = 'admin' THEN 'admin' ELSE 'user' END, email = CASE WHEN instr(username, '@') > 0 THEN lower(trim(username)) ELSE '' END, updated_at = created_at`, defaultQuota); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET name = id, desired_state = CASE WHEN status = 'expired' THEN 'legacy_expired' WHEN status IN ('stopped','stopping') THEN 'stopped' ELSE 'running' END`); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES(3)`); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "PRAGMA user_version = 3")
		return err
	})
}

const baseSchema = `
CREATE TABLE IF NOT EXISTS users (
 id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE,
 password_hash TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sessions (
 id TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id),
 status TEXT NOT NULL DEFAULT 'creating', container_name TEXT NOT NULL,
 profile_dir TEXT NOT NULL, downloads_dir TEXT NOT NULL, session_token_hash TEXT NOT NULL,
 last_active_at DATETIME, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, expired_at DATETIME
);
CREATE TABLE IF NOT EXISTS session_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 type TEXT NOT NULL, payload_json TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_session_events_session_id ON session_events(session_id);
`

const filesSchema = `
CREATE TABLE IF NOT EXISTS session_files (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 name TEXT NOT NULL, size_bytes INTEGER NOT NULL DEFAULT 0, content_type TEXT,
 created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_session_files_session_id ON session_files(session_id);
`
