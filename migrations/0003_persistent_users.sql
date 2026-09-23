ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin','user'));
ALTER TABLE users ADD COLUMN status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','disabled','deleting','delete_failed'));
ALTER TABLE users ADD COLUMN instance_quota INTEGER NOT NULL DEFAULT 0 CHECK(instance_quota >= 0);
ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN auth_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN delivery_status TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE users ADD COLUMN delivery_error TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN updated_at DATETIME;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(email COLLATE NOCASE) WHERE email <> '';
ALTER TABLE sessions ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN desired_state TEXT NOT NULL DEFAULT 'running' CHECK(desired_state IN ('','legacy_expired','running','stopped','deleted'));
ALTER TABLE sessions ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN create_request_key TEXT;
ALTER TABLE sessions ADD COLUMN recovery_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN recovery_next_at DATETIME;
CREATE UNIQUE INDEX idx_sessions_create_request ON sessions(user_id, create_request_key) WHERE create_request_key IS NOT NULL;
CREATE TABLE instance_requests (
 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL,
 session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(user_id, request_key)
);
CREATE TABLE login_sessions (
 token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 auth_version INTEGER NOT NULL, expires_at DATETIME NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_login_sessions_expiry ON login_sessions(expires_at);
CREATE TABLE audit_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, actor_id INTEGER NOT NULL DEFAULT 0,
 target_user_id INTEGER NOT NULL DEFAULT 0, action TEXT NOT NULL, target_id TEXT NOT NULL DEFAULT '',
 result TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE mail_deliveries (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
 kind TEXT NOT NULL, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '',
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
