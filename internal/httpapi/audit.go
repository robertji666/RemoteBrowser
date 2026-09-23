package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/auth"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
)

// Audit identifiers and fixed result codes survive deletion of the account or
// instance. Never include forms, emails, file names, browser content or raw
// provider/runtime error strings in this administrative history.
func (h *Handler) auditAction(r *http.Request, targetUserID int64, action, targetID, result string, cause error) {
	detail := ""
	if cause != nil {
		result = "failed"
		switch {
		case errors.Is(cause, store.ErrQuotaExceeded):
			detail = "quota_exceeded"
		case errors.Is(cause, store.ErrUserInactive):
			detail = "user_inactive"
		case errors.Is(cause, store.ErrUserProtected):
			detail = "protected_user"
		case errors.Is(cause, store.ErrNotFound):
			detail = "not_found"
		case errors.Is(cause, context.Canceled):
			detail = "request_cancelled"
		case errors.Is(cause, context.DeadlineExceeded):
			detail = "timeout"
		default:
			detail = "operation_failed"
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	err := h.auth.Store().CreateAuditEvent(ctx, &model.AuditEvent{ActorID: auth.UserIDFromContext(r.Context()), TargetUserID: targetUserID, Action: action, TargetID: targetID, Result: result, Detail: detail})
	if err != nil {
		slog.Error("administrative audit could not be persisted", "action", action, "target_id", targetID)
	}
}
