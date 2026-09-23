package store

import (
	"context"
	"database/sql"

	"github.com/robertji666/RemoteBrowser/internal/model"
)

func (s *Store) CreateSessionEvent(ctx context.Context, event *model.SessionEvent) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO session_events(session_id,type,payload_json) VALUES(?,?,?)`, event.SessionID, event.Type, event.PayloadJSON)
	if err != nil {
		return err
	}
	event.ID, err = res.LastInsertId()
	return err
}
func (s *Store) CreateSessionFile(ctx context.Context, f *model.SessionFile) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO session_files(session_id,name,size_bytes,content_type) VALUES(?,?,?,?)`, f.SessionID, f.Name, f.SizeBytes, f.ContentType)
	if err != nil {
		return err
	}
	f.ID, err = res.LastInsertId()
	return err
}
func (s *Store) ListSessionFiles(ctx context.Context, sessionID string) ([]*model.SessionFile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,name,size_bytes,COALESCE(content_type,''),created_at FROM session_files WHERE session_id=? ORDER BY created_at DESC,id DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []*model.SessionFile
	for rows.Next() {
		var f model.SessionFile
		if err := rows.Scan(&f.ID, &f.SessionID, &f.Name, &f.SizeBytes, &f.ContentType, &f.CreatedAt); err != nil {
			return nil, err
		}
		files = append(files, &f)
	}
	return files, rows.Err()
}
func (s *Store) CreateAuditEvent(ctx context.Context, e *model.AuditEvent) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(actor_id,target_user_id,action,target_id,result,detail) VALUES(?,?,?,?,?,?)`, e.ActorID, e.TargetUserID, e.Action, e.TargetID, e.Result, e.Detail)
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}
func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]*model.AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor_id,target_user_id,action,target_id,result,detail,created_at FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*model.AuditEvent
	for rows.Next() {
		var e model.AuditEvent
		if err := rows.Scan(&e.ID, &e.ActorID, &e.TargetUserID, &e.Action, &e.TargetID, &e.Result, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, &e)
	}
	return events, rows.Err()
}
func (s *Store) CreateMailDelivery(ctx context.Context, d *model.MailDelivery) error {
	var userID any
	if d.UserID > 0 {
		userID = d.UserID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO mail_deliveries(user_id,kind,status,error) VALUES(?,?,?,?)`, userID, d.Kind, d.Status, d.Error)
	if err != nil {
		return err
	}
	d.ID, err = res.LastInsertId()
	return err
}
func (s *Store) ListMailDeliveries(ctx context.Context, userID int64, limit int) ([]*model.MailDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,kind,status,error,created_at FROM mail_deliveries WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []*model.MailDelivery
	for rows.Next() {
		var d model.MailDelivery
		var id sql.NullInt64
		if err := rows.Scan(&d.ID, &id, &d.Kind, &d.Status, &d.Error, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.UserID = id.Int64
		deliveries = append(deliveries, &d)
	}
	return deliveries, rows.Err()
}
