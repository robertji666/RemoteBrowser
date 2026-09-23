package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/model"
)

type UserFilter struct {
	Search, Status string
	Limit, Offset  int
}

const userColumns = `u.id, u.username, u.email, u.display_name, u.role, u.status,
u.password_hash, u.instance_quota, u.must_change_password, u.auth_version,
u.delivery_status, u.delivery_error, u.last_error, u.created_at, u.updated_at,
(SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.id)`

func scanUser(row scanner) (*model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.Status, &u.PasswordHash,
		&u.InstanceQuota, &u.MustChangePassword, &u.AuthVersion, &u.DeliveryStatus, &u.DeliveryError,
		&u.LastError, &u.CreatedAt, &u.UpdatedAt, &u.InstanceCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) CreateUser(ctx context.Context, u *model.User) error {
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	u.Username = strings.TrimSpace(u.Username)
	if u.Role == "" {
		u.Role = model.RoleUser
		if u.Username == "admin" {
			u.Role = model.RoleAdmin
		}
	}
	if u.Role != model.RoleAdmin && u.Role != model.RoleUser {
		return fmt.Errorf("invalid user role")
	}
	if u.Email == "" && strings.Contains(u.Username, "@") {
		u.Email = strings.ToLower(u.Username)
	}
	if u.Email != "" {
		a, err := mail.ParseAddress(u.Email)
		if err != nil || a.Address != u.Email {
			return fmt.Errorf("invalid email address")
		}
	} else if u.Role != model.RoleAdmin {
		return fmt.Errorf("email is required")
	}
	if u.Role == model.RoleUser {
		u.Username = u.Email
	}
	if u.Username == "" {
		u.Username = u.Email
	}
	if u.Username == "" || u.PasswordHash == "" {
		return fmt.Errorf("username and password hash are required")
	}
	if u.InstanceQuota < 0 {
		return fmt.Errorf("instance quota must be nonnegative")
	}
	if u.Status == "" {
		u.Status = model.UserActive
	}
	if u.Status != model.UserActive && u.Status != model.UserDisabled {
		return fmt.Errorf("invalid initial user status")
	}
	if u.DeliveryStatus == "" {
		u.DeliveryStatus = "manual"
	}
	u.AuthVersion = 1
	u.CreatedAt = time.Now().UTC()
	u.UpdatedAt = u.CreatedAt
	res, err := s.db.ExecContext(ctx, `INSERT INTO users
 (username,email,display_name,role,status,password_hash,instance_quota,must_change_password,auth_version,delivery_status,delivery_error,last_error,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.Username, u.Email, u.DisplayName, u.Role, u.Status, u.PasswordHash, u.InstanceQuota, u.MustChangePassword, u.AuthVersion, u.DeliveryStatus, u.DeliveryError, u.LastError, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w: login identifier is already registered", ErrEmailExists)
		}
		return err
	}
	u.ID, err = res.LastInsertId()
	return err
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id=?`, id))
}
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE u.username=?`, strings.TrimSpace(username)))
}
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE u.email=? COLLATE NOCASE AND u.email<>''`, strings.ToLower(strings.TrimSpace(email))))
}
func (s *Store) GetUserByLogin(ctx context.Context, login string) (*model.User, error) {
	if strings.EqualFold(strings.TrimSpace(login), "admin") {
		return s.GetUserByUsername(ctx, "admin")
	}
	return s.GetUserByEmail(ctx, login)
}

func (s *Store) ListUsers(ctx context.Context, f UserFilter) ([]*model.User, int, error) {
	if f.Limit <= 0 {
		f.Limit = 25
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := ` WHERE 1=1`
	args := []any{}
	if search := strings.TrimSpace(f.Search); search != "" {
		// Search is literal substring, not a SQL LIKE pattern.
		search = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
		where += ` AND (u.email LIKE ? ESCAPE '\' OR u.username LIKE ? ESCAPE '\' OR u.display_name LIKE ? ESCAPE '\')`
		args = append(args, "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}
	if f.Status != "" {
		where += ` AND u.status=?`
		args = append(args, f.Status)
	}
	var total int
	var users []*model.User
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u`+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+userColumns+` FROM users u`+where+` ORDER BY u.id DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			u, err := scanUser(rows)
			if err != nil {
				return err
			}
			users = append(users, u)
		}
		return rows.Err()
	})
	return users, total, err
}

func (s *Store) ListDeletingUsers(ctx context.Context) ([]*model.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users u WHERE u.status IN ('deleting','delete_failed') ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UpdateUserQuota(ctx context.Context, id int64, quota int) error {
	if quota < 0 {
		return fmt.Errorf("instance quota must be nonnegative")
	}
	return affected(s.db.ExecContext(ctx, `UPDATE users SET instance_quota=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('active','disabled')`, quota, id))
}

func (s *Store) SetUserStatus(ctx context.Context, id int64, status string) error {
	if status != model.UserActive && status != model.UserDisabled {
		return fmt.Errorf("use deletion workflow for deleting users")
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE id=?`, id))
		if err != nil {
			return err
		}
		if u == nil {
			return ErrNotFound
		}
		if u.Role == model.RoleAdmin {
			return ErrUserProtected
		}
		if u.Status == model.UserDeleting || u.Status == model.UserDeleteFailed {
			return ErrUserInactive
		}
		if u.Status == status {
			return nil
		}
		return affected(tx.ExecContext(ctx, `UPDATE users SET status=?,auth_version=auth_version+1,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, id))
	})
}

func (s *Store) UpdateUserPassword(ctx context.Context, id int64, hash string, mustChange bool) error {
	if hash == "" {
		return fmt.Errorf("password hash is required")
	}
	return affected(s.db.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=?,auth_version=auth_version+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='active'`, hash, mustChange, id))
}
func (s *Store) CompareAndUpdateUserPassword(ctx context.Context, id, expectedVersion int64, hash string, mustChange bool) error {
	if hash == "" {
		return fmt.Errorf("password hash is required")
	}
	err := affected(s.db.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=?,auth_version=auth_version+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND auth_version=? AND status='active'`, hash, mustChange, id, expectedVersion))
	if errors.Is(err, ErrNotFound) {
		return ErrConflict
	}
	return err
}
func (s *Store) CompareAndSwapUserPassword(ctx context.Context, id, version int64, hash string, mustChange bool) error {
	return s.CompareAndUpdateUserPassword(ctx, id, version, hash, mustChange)
}

func (s *Store) MarkUserDeleting(ctx context.Context, id int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE id=?`, id))
		if err != nil {
			return err
		}
		if u == nil {
			return ErrNotFound
		}
		if u.Role == model.RoleAdmin || u.Username == "admin" {
			return ErrUserProtected
		}
		if u.Status == model.UserDeleting {
			return nil
		}
		return affected(tx.ExecContext(ctx, `UPDATE users SET status='deleting',last_error='',auth_version=auth_version+1,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id))
	})
}
func (s *Store) SetUserDeletionError(ctx context.Context, id int64, msg string) error {
	return affected(s.db.ExecContext(ctx, `UPDATE users SET status='delete_failed',last_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('deleting','delete_failed')`, msg, id))
}
func (s *Store) MarkUserDeleteFailed(ctx context.Context, id int64, msg string) error {
	return s.SetUserDeletionError(ctx, id, msg)
}

// DeleteUser is deliberately not a cascading resource deletion: the caller
// must finish container and directory cleanup and remove every session first.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u WHERE id=?`, id))
		if err != nil {
			return err
		}
		if u == nil {
			return nil
		}
		if u.Role == model.RoleAdmin || u.Username == "admin" {
			return ErrUserProtected
		}
		if u.Status != model.UserDeleting && u.Status != model.UserDeleteFailed {
			return ErrUserInactive
		}
		if u.InstanceCount > 0 {
			return ErrUserHasInstances
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id))
	})
}

func (s *Store) UpdateDelivery(ctx context.Context, id int64, status, errorText string) error {
	return affected(s.db.ExecContext(ctx, `UPDATE users SET delivery_status=?,delivery_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, errorText, id))
}
func (s *Store) UpdateUserDelivery(ctx context.Context, id int64, status, errorText string) error {
	return s.UpdateDelivery(ctx, id, status, errorText)
}

func (s *Store) CreateLoginSession(ctx context.Context, hash string, userID, version int64, expiresAt time.Time) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_sessions WHERE expires_at<=?`, time.Now().UTC()); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO login_sessions(token_hash,user_id,auth_version,expires_at) SELECT ?,id,auth_version,? FROM users WHERE id=? AND auth_version=? AND status='active'`, hash, expiresAt.UTC(), userID, version)
		if err = affected(result, err); errors.Is(err, ErrNotFound) {
			return ErrUserInactive
		}
		return err
	})
}
func (s *Store) LoginSessionValid(ctx context.Context, hash string, userID, version int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM login_sessions l JOIN users u ON u.id=l.user_id WHERE l.token_hash=? AND l.user_id=? AND l.auth_version=? AND u.auth_version=l.auth_version AND u.status='active' AND l.expires_at>?)`, hash, userID, version, time.Now().UTC()).Scan(&exists)
	return exists, err
}
func (s *Store) RevokeLoginSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_sessions WHERE token_hash=?`, hash)
	return err
}
