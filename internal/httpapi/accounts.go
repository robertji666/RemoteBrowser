package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/robertji666/RemoteBrowser/internal/auth"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"github.com/robertji666/RemoteBrowser/internal/store"
)

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["User"] = auth.UserFromContext(r.Context())
	data["CSRF"] = auth.CSRFToken(r)
	if data["Notice"] == nil {
		data["Notice"] = r.URL.Query().Get("notice")
	}
	if err := h.view.Render(w, name, data); err != nil {
		slog.Error("render page", "page", name, "error", err)
		http.Error(w, "页面暂时无法显示", http.StatusInternalServerError)
	}
}

func (h *Handler) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u == nil || u.Role != "admin" {
			http.Error(w, "需要管理员权限", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}
func jsonReply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func (h *Handler) actionError(w http.ResponseWriter, r *http.Request, err error) {
	message := actionErrorMessage(err)
	if wantsJSON(r) {
		// Preserve the original machine-facing error and audit input while
		// supplying the same user-facing explanation as the HTML page.
		jsonReply(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "message": message})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	h.render(w, r, "error.html", map[string]any{"Title": "操作未完成", "Icon": "!", "Message": message, "RedirectURL": "/", "ButtonText": "返回实例列表"})
}

func actionErrorMessage(err error) string {
	switch {
	case errors.Is(err, store.ErrQuotaExceeded):
		return "已达到浏览器实例数量上限。停止的实例也占用名额，请删除不再使用的实例，或联系管理员增加配额。"
	case errors.Is(err, store.ErrEmailExists):
		return "该邮箱已存在，请使用其他邮箱，或在用户列表中管理现有账号。"
	case errors.Is(err, store.ErrConflict):
		return "记录已发生变化，请刷新页面后重试。"
	case errors.Is(err, store.ErrUserInactive):
		return "该账号当前不可用（已禁用或正在删除），请联系管理员。"
	case errors.Is(err, store.ErrUserProtected):
		return "管理员账号受保护，不能执行此操作。"
	case errors.Is(err, store.ErrUserHasInstances):
		return "该用户仍有关联的浏览器实例，尚不能完成账号删除。请在用户管理中查看清理进度并重试。"
	case errors.Is(err, store.ErrNotFound):
		return "目标记录不存在或已删除，请刷新页面后重试。"
	default:
		return err.Error()
	}
}
func actionSuccess(w http.ResponseWriter, r *http.Request, path, notice string, v any) {
	if wantsJSON(r) {
		jsonReply(w, http.StatusOK, v)
		return
	}
	http.Redirect(w, r, path+"?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func userData(u *model.User) map[string]any {
	return map[string]any{"id": u.ID, "email": u.Email, "username": u.Username, "displayName": u.DisplayName, "role": u.Role, "status": u.Status, "instanceQuota": u.InstanceQuota, "instanceCount": u.InstanceCount, "mustChangePassword": u.MustChangePassword, "deliveryStatus": u.DeliveryStatus, "deliveryError": u.DeliveryError, "lastError": u.LastError, "createdAt": u.CreatedAt}
}

func (h *Handler) CurrentAccount(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, http.StatusOK, userData(auth.UserFromContext(r.Context())))
}
func (h *Handler) PasswordPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "password.html", map[string]any{"Title": "修改密码"})
}
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("new_password") != r.FormValue("confirm_password") {
		h.actionError(w, r, fmt.Errorf("两次输入的新密码不一致"))
		return
	}
	if err := h.auth.ChangePassword(r.Context(), auth.UserIDFromContext(r.Context()), r.FormValue("current_password"), r.FormValue("new_password")); err != nil {
		h.actionError(w, r, err)
		return
	}
	h.auth.ClearCookie(w, r)
	actionSuccess(w, r, "/login", "密码已更新，请使用新密码登录。", map[string]bool{"ok": true})
}

func userFilter(r *http.Request) (store.UserFilter, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	return store.UserFilter{Search: strings.TrimSpace(r.URL.Query().Get("q")), Status: r.URL.Query().Get("status"), Limit: 20, Offset: (page - 1) * 20}, page
}
func (h *Handler) UsersPage(w http.ResponseWriter, r *http.Request) {
	f, page := userFilter(r)
	users, total, err := h.auth.Store().ListUsers(r.Context(), f)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	h.render(w, r, "users.html", map[string]any{"Title": "用户管理", "Users": users, "Total": total, "Page": page, "Previous": page - 1, "Next": page + 1, "HasNext": page*20 < total, "Search": f.Search, "FilterStatus": f.Status, "Mail": h.auth.MailStatus(), "DefaultQuota": h.auth.Config().DefaultInstanceQuota})
}
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	f, _ := userFilter(r)
	users, total, err := h.auth.Store().ListUsers(r.Context(), f)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(users))
	for _, u := range users {
		items = append(items, userData(u))
	}
	jsonReply(w, 200, map[string]any{"users": items, "total": total})
}
func (h *Handler) targetUser(r *http.Request) (*model.User, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
	if err != nil || id < 1 {
		return nil, fmt.Errorf("无效用户")
	}
	u, err := h.auth.Store().GetUserByID(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("用户不存在")
	}
	return u, nil
}
func (h *Handler) UserPage(w http.ResponseWriter, r *http.Request) {
	u, err := h.targetUser(r)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	instances, err := h.session.ListSessions(r.Context(), u.ID)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	h.render(w, r, "user-detail.html", map[string]any{"Title": "管理用户", "Target": u, "Sessions": instances, "Mail": h.auth.MailStatus()})
}
func parseQuota(r *http.Request) (int, error) {
	n, err := strconv.Atoi(r.FormValue("quota"))
	if err != nil || n < 0 || n > 100000 {
		return 0, fmt.Errorf("实例上限必须是 0–100000 的整数")
	}
	return n, nil
}
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	quota, err := parseQuota(r)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	u, err := h.auth.CreateUser(r.Context(), auth.UserIDFromContext(r.Context()), r.FormValue("email"), r.FormValue("display_name"), r.FormValue("password"), quota, r.FormValue("manual") == "true")
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	notice := "用户已创建。"
	if u.DeliveryStatus == "failed" {
		notice += "邮件发送失败，可重新发送或手动交付密码。"
	}
	actionSuccess(w, r, fmt.Sprintf("/admin/users/%d", u.ID), notice, userData(u))
}
func (h *Handler) UpdateQuota(w http.ResponseWriter, r *http.Request) {
	u, err := h.targetUser(r)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	q, err := parseQuota(r)
	if err == nil {
		err = h.auth.UpdateUserQuota(r.Context(), auth.UserIDFromContext(r.Context()), u.ID, q)
	}
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	actionSuccess(w, r, fmt.Sprintf("/admin/users/%d", u.ID), "实例配额已更新，现有实例继续保留。", map[string]bool{"ok": true})
}
func (h *Handler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	u, err := h.targetUser(r)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	if r.FormValue("password") != r.FormValue("confirm_password") {
		h.actionError(w, r, fmt.Errorf("两次密码不一致"))
		return
	}
	u, err = h.auth.ResetUserPassword(r.Context(), auth.UserIDFromContext(r.Context()), u.ID, r.FormValue("password"), r.FormValue("manual") == "true")
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	actionSuccess(w, r, fmt.Sprintf("/admin/users/%d", u.ID), "密码已重置，旧凭据已撤销。请检查下方密码交付结果。", userData(u))
}
func (h *Handler) ResendWelcome(w http.ResponseWriter, r *http.Request) {
	u, err := h.targetUser(r)
	if err == nil {
		u, err = h.auth.ResendWelcome(r.Context(), auth.UserIDFromContext(r.Context()), u.ID)
	}
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	actionSuccess(w, r, fmt.Sprintf("/admin/users/%d", u.ID), "已处理开户通知，请检查交付结果。", userData(u))
}
func (h *Handler) UpdateUserStatus(w http.ResponseWriter, r *http.Request) {
	u, err := h.targetUser(r)
	if err == nil {
		err = h.auth.SetUserStatus(r.Context(), auth.UserIDFromContext(r.Context()), u.ID, r.FormValue("status"))
	}
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	actionSuccess(w, r, fmt.Sprintf("/admin/users/%d", u.ID), "用户状态已更新，浏览器实例保持原运行状态。", map[string]bool{"ok": true})
}
func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.targetUser(r)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	if u.Role == "admin" || u.ID == auth.UserIDFromContext(r.Context()) {
		h.auditAction(r, u.ID, "user.delete", strconv.FormatInt(u.ID, 10), "", store.ErrUserProtected)
		h.actionError(w, r, fmt.Errorf("不能删除管理员账号"))
		return
	}
	if strings.ToLower(strings.TrimSpace(r.FormValue("confirm_email"))) != strings.ToLower(u.Email) || r.FormValue("confirm_delete") != "yes" {
		h.auditAction(r, u.ID, "user.delete", strconv.FormatInt(u.ID, 10), "confirmation_required", nil)
		h.actionError(w, r, fmt.Errorf("请填写目标邮箱并确认永久删除用户、全部实例和数据"))
		return
	}
	if err = h.session.DeleteUser(r.Context(), u.ID); err != nil {
		h.auditAction(r, u.ID, "user.delete", strconv.FormatInt(u.ID, 10), "", err)
		h.actionError(w, r, fmt.Errorf("清理尚未完成，请回到用户管理查看进度并重试：%w", err))
		return
	}
	h.auditAction(r, u.ID, "user.delete", strconv.FormatInt(u.ID, 10), "success", nil)
	actionSuccess(w, r, "/admin/users", "用户及所属浏览器实例、数据已删除。", map[string]bool{"ok": true})
}
func (h *Handler) ResourcesPage(w http.ResponseWriter, r *http.Request) {
	instances, err := h.session.ListSessions(r.Context(), 0)
	if err != nil {
		h.actionError(w, r, err)
		return
	}
	orphans, orphanErr := h.session.ListOrphanContainers(r.Context())
	h.render(w, r, "resources.html", map[string]any{"Title": "资源概览", "Sessions": instances, "Orphans": orphans, "OrphanError": orphanErr, "Mail": h.auth.MailStatus()})
}
func (h *Handler) TestMail(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.TestMail(r.Context(), r.FormValue("email")); err != nil {
		h.actionError(w, r, err)
		return
	}
	actionSuccess(w, r, "/admin/resources", "测试邮件已提交发送。", map[string]bool{"ok": true})
}

func requestKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
