package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	mailer "github.com/robertji666/RemoteBrowser/internal/mail"
	"github.com/robertji666/RemoteBrowser/internal/model"
	"golang.org/x/crypto/bcrypt"
)

type MailStatus struct {
	Enabled                            bool
	Provider, From, ConfigurationError string
}

func (a *Auth) MailStatus() MailStatus {
	s := MailStatus{Enabled: a.cfg.SMTP.Configured(), Provider: "SMTP", From: a.cfg.SMTP.From}
	if s.Enabled {
		if err := a.sender().Validate(); err != nil {
			s.ConfigurationError = err.Error()
		}
	}
	return s
}
func (a *Auth) sender() mailer.Sender {
	return mailer.Sender{Config: a.cfg.SMTP, PublicURL: a.cfg.PublicURL}
}
func (a *Auth) TestMail(ctx context.Context, to string) error {
	email, err := NormalizeEmail(to)
	if err != nil {
		return err
	}
	return a.sender().SendTest(ctx, email)
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 {
		return errors.New("密码至少需要 12 个字符")
	}
	if len(password) > 72 {
		return errors.New("密码不能超过 72 个 UTF-8 字节")
	}
	return nil
}
func NormalizeEmail(input string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(input))
	if len(email) > 254 || strings.ContainsAny(email, "\r\n") {
		return "", errors.New("请输入有效邮箱地址")
	}
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || !strings.Contains(email, "@") {
		return "", errors.New("请输入有效邮箱地址")
	}
	return email, nil
}
func generatedPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *Auth) admin(ctx context.Context, id int64) (*model.User, error) {
	u, err := a.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil || u.Role != model.RoleAdmin || u.Status != model.UserActive || u.MustChangePassword {
		return nil, errors.New("需要有效的管理员权限")
	}
	return u, nil
}
func (a *Auth) manageableUser(ctx context.Context, actorID, id int64) (*model.User, error) {
	if _, err := a.admin(ctx, actorID); err != nil {
		return nil, err
	}
	u, err := a.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, errors.New("用户不存在")
	}
	if u.Role == model.RoleAdmin || u.Username == "admin" || id == actorID {
		return nil, errors.New("此入口不允许修改内置管理员或当前管理员")
	}
	if u.Status == model.UserDeleting || u.Status == model.UserDeleteFailed {
		return nil, errors.New("用户正在删除，不能修改")
	}
	return u, nil
}

func (a *Auth) CreateUser(ctx context.Context, actorID int64, email, displayName, password string, quota int, manual bool) (*model.User, error) {
	if _, err := a.admin(ctx, actorID); err != nil {
		return nil, err
	}
	if !a.allow(fmt.Sprintf("create:%d", actorID), 30, time.Minute) {
		return nil, ErrRateLimited
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if quota < 0 {
		return nil, errors.New("实例配额不能为负数")
	}
	if utf8.RuneCountInString(displayName) > 100 {
		return nil, errors.New("显示名称不能超过 100 个字符")
	}
	useEmail := a.cfg.SMTP.Configured() && !manual
	if useEmail {
		password, err = generatedPassword()
		if err != nil {
			return nil, err
		}
	}
	if err = ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	u := &model.User{Username: email, Email: email, DisplayName: strings.TrimSpace(displayName), Role: model.RoleUser, Status: model.UserActive, PasswordHash: string(hash), InstanceQuota: quota, AuthVersion: 1, MustChangePassword: true, DeliveryStatus: "manual"}
	if useEmail {
		u.DeliveryStatus = "pending"
	}
	if err = a.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	a.audit(ctx, actorID, u.ID, "user.create", "success")
	if useEmail {
		if err = a.deliver(ctx, u, password, "welcome"); err != nil {
			return u, fmt.Errorf("账号已创建，但邮件交付状态保存失败：%w", err)
		}
	}
	return a.store.GetUserByID(ctx, u.ID)
}

func (a *Auth) ResetUserPassword(ctx context.Context, actorID, id int64, password string, manual bool) (*model.User, error) {
	return a.resetUserPassword(ctx, actorID, id, password, manual, false)
}
func (a *Auth) resetUserPassword(ctx context.Context, actorID, id int64, password string, manual, requireInitial bool) (*model.User, error) {
	u, err := a.manageableUser(ctx, actorID, id)
	if err != nil {
		return nil, err
	}
	if !a.allow(fmt.Sprintf("reset:%d", actorID), 20, time.Minute) {
		return nil, ErrRateLimited
	}
	if u.Status != model.UserActive {
		return nil, errors.New("请先启用用户再重置密码")
	}
	if requireInitial && !u.MustChangePassword {
		return nil, errors.New("用户已完成首次改密，请使用明确的重置密码操作")
	}
	useEmail := a.cfg.SMTP.Configured() && !manual
	if password == "" && useEmail {
		password, err = generatedPassword()
		if err != nil {
			return nil, err
		}
	}
	if err = ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	if err = a.store.CompareAndSwapUserPassword(ctx, id, u.AuthVersion, string(hash), true); err != nil {
		return nil, err
	}
	a.audit(ctx, actorID, id, "user.password.reset", "success")
	if useEmail {
		if err = a.store.UpdateUserDelivery(ctx, id, "pending", ""); err != nil {
			return u, err
		}
		if err = a.deliver(ctx, u, password, "reset"); err != nil {
			return u, fmt.Errorf("密码已重置，但邮件交付状态保存失败：%w", err)
		}
	} else if err = a.store.UpdateUserDelivery(ctx, id, "manual", ""); err != nil {
		return nil, err
	}
	return a.store.GetUserByID(ctx, id)
}
func (a *Auth) ResendWelcome(ctx context.Context, actorID, id int64) (*model.User, error) {
	if !a.cfg.SMTP.Configured() {
		return nil, errors.New("未配置邮件服务，请手工设置临时密码")
	}
	return a.resetUserPassword(ctx, actorID, id, "", false, true)
}
func (a *Auth) ChangePassword(ctx context.Context, id int64, current, password string) error {
	if !a.allow(fmt.Sprintf("password:%d", id), 10, time.Minute) {
		return ErrRateLimited
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	u, err := a.store.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	if u == nil || u.Status != model.UserActive {
		return ErrCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)) != nil {
		return errors.New("当前密码错误")
	}
	if SecureCompare(current, password) {
		return errors.New("新密码不能与当前密码相同")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err = a.store.CompareAndSwapUserPassword(ctx, id, u.AuthVersion, string(hash), false); err != nil {
		return err
	}
	a.audit(ctx, id, id, "user.password.change", "success")
	return nil
}
func (a *Auth) ResetAdminPassword(ctx context.Context, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	u, err := a.store.GetUserByUsername(ctx, "admin")
	if err != nil {
		return err
	}
	if u == nil {
		return errors.New("管理员尚未初始化")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err = a.store.UpdateUserPassword(ctx, u.ID, string(hash), false); err != nil {
		return err
	}
	a.audit(ctx, 0, u.ID, "admin.password.cli-reset", "success")
	return nil
}
func (a *Auth) SetUserStatus(ctx context.Context, actorID, id int64, status string) error {
	if _, err := a.manageableUser(ctx, actorID, id); err != nil {
		return err
	}
	if status != model.UserActive && status != model.UserDisabled {
		return errors.New("无效的用户状态")
	}
	if err := a.store.SetUserStatus(ctx, id, status); err != nil {
		return err
	}
	a.audit(ctx, actorID, id, "user.status."+status, "success")
	return nil
}
func (a *Auth) UpdateUserQuota(ctx context.Context, actorID, id int64, quota int) error {
	if _, err := a.admin(ctx, actorID); err != nil {
		return err
	}
	if quota < 0 {
		return errors.New("实例配额不能为负数")
	}
	if err := a.store.UpdateUserQuota(ctx, id, quota); err != nil {
		return err
	}
	a.audit(ctx, actorID, id, "user.quota", "success")
	return nil
}
func (a *Auth) deliver(ctx context.Context, u *model.User, password, kind string) error {
	status, errorText := "sent", ""
	if err := a.sender().SendCredentials(ctx, u.Email, password, kind); err != nil {
		status, errorText = "failed", err.Error()
	}
	// Finish recording a failed/cancelled delivery even if the administrator
	// navigated away; no password or message body enters persistent storage.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.store.UpdateUserDelivery(recordCtx, u.ID, status, errorText); err != nil {
		return err
	}
	return a.store.CreateMailDelivery(recordCtx, &model.MailDelivery{UserID: u.ID, Kind: kind, Status: status, Error: errorText})
}
func (a *Auth) audit(ctx context.Context, actorID, targetID int64, action, result string) {
	_ = a.store.CreateAuditEvent(ctx, &model.AuditEvent{ActorID: actorID, TargetUserID: targetID, Action: action, Result: result})
}
