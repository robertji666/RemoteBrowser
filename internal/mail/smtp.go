// Package mail sends transient credentials without retaining message bodies.
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/config"
)

type Sender struct {
	Config    config.SMTPConfig
	PublicURL string
}

func (s Sender) Validate() error {
	c := s.Config
	if !c.Configured() {
		return errors.New("未配置邮件服务")
	}
	if c.Host == "" || strings.ContainsAny(c.Host, "\r\n /:") && net.ParseIP(c.Host) == nil {
		return errors.New("RB_SMTP_HOST 缺失或无效")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("RB_SMTP_PORT 必须在 1 到 65535 之间")
	}
	if strings.ContainsAny(c.From, "\r\n") {
		return errors.New("RB_SMTP_FROM 无效")
	}
	if _, err := stdmail.ParseAddress(c.From); err != nil {
		return errors.New("RB_SMTP_FROM 必须是有效邮件地址")
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("SMTP 用户名和密码必须同时配置")
	}
	switch c.Encryption {
	case "tls", "starttls":
	case "plain":
		if c.Host != "localhost" && (net.ParseIP(c.Host) == nil || !net.ParseIP(c.Host).IsLoopback()) {
			return errors.New("SMTP plain 仅允许本机测试地址；部署请使用 tls 或 starttls")
		}
	default:
		return errors.New("RB_SMTP_ENCRYPTION 必须是 tls、starttls 或 plain")
	}
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("邮件服务需要有效的 RB_PUBLIC_URL 登录地址")
	}
	return nil
}

func (s Sender) SendCredentials(ctx context.Context, to, password, kind string) error {
	subject := "RemoteBrowser 账号已创建"
	if kind != "welcome" {
		subject = "RemoteBrowser 临时密码已重置"
	}
	body := fmt.Sprintf("您的 RemoteBrowser 账号：%s\n\n登录地址：%s/login\n初始临时密码：%s\n\n请在首次登录后修改密码。旧密码和旧登录凭据已失效（新建账号除外）。\n请勿转发此邮件。\n", to, strings.TrimRight(s.PublicURL, "/"), password)
	return s.Send(ctx, to, subject, body)
}
func (s Sender) SendTest(ctx context.Context, to string) error {
	return s.Send(ctx, to, "RemoteBrowser 邮件配置测试", "这是一封邮件配置测试消息，不含账号密码。\n")
}

func (s Sender) Send(ctx context.Context, to, subject, body string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if strings.ContainsAny(to, "\r\n") {
		return errors.New("收件邮箱无效")
	}
	addr, err := stdmail.ParseAddress(to)
	if err != nil || addr.Address != to {
		return errors.New("收件邮箱无效")
	}
	c := s.Config
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return safeError("连接 SMTP", err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if c.Encryption == "plain" {
		host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("SMTP 明文连接只能用于本机测试")
		}
	}
	tlsConfig := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.Encryption == "tls" {
		tlsConn := tls.Client(conn, tlsConfig)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			return safeError("SMTP TLS 握手", err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return safeError("SMTP 欢迎消息", err)
	}
	defer client.Close()
	if c.Encryption == "starttls" {
		if err = client.StartTLS(tlsConfig); err != nil {
			return safeError("SMTP STARTTLS", err)
		}
	}
	if c.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return safeError("SMTP 认证", err)
		}
	}
	from, _ := stdmail.ParseAddress(c.From)
	if err = client.Mail(from.Address); err != nil {
		return safeError("SMTP 发件人", err)
	}
	if err = client.Rcpt(addr.Address); err != nil {
		return safeError("SMTP 收件人", err)
	}
	writer, err := client.Data()
	if err != nil {
		return safeError("SMTP DATA", err)
	}
	message := "From: " + from.String() + "\r\nTo: " + addr.String() + "\r\nSubject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(subject)) + "?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	for len(encoded) > 76 {
		message += encoded[:76] + "\r\n"
		encoded = encoded[76:]
	}
	message += encoded + "\r\n"
	if _, err = io.WriteString(writer, message); err != nil {
		return safeError("SMTP 写入", err)
	}
	if err = writer.Close(); err != nil {
		return safeError("SMTP 提交", err)
	}
	// DATA accepted is the delivery boundary. A failed QUIT does not mean the
	// provider rejected the message, and must not encourage duplicate retries.
	_ = client.Quit()
	return nil
}

func safeError(phase string, err error) error {
	var smtpError *textproto.Error
	if errors.As(err, &smtpError) {
		return fmt.Errorf("%s失败（SMTP %d）", phase, smtpError.Code)
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return fmt.Errorf("%s超时", phase)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s已取消", phase)
	}
	// Provider responses are deliberately not returned: a hostile or broken
	// server can echo a password or AUTH payload in its error text.
	return fmt.Errorf("%s失败，请检查网络、证书及邮件配置", phase)
}
