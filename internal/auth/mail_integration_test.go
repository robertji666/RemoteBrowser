package auth

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	stdmail "net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/config"
)

// Exercise the real account, database and SMTP boundary. The fake server only
// accepts loopback messages and exposes their transient body to this test.
func TestEmailInvitationLoginRetryAndNoPlaintextStorage(t *testing.T) {
	a, admin := testAuth(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	messages := make(chan string, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				c := textproto.NewConn(conn)
				_ = c.PrintfLine("220 localhost test")
				for {
					line, err := c.ReadLine()
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
						_ = c.PrintfLine("250 OK")
					case line == "DATA":
						_ = c.PrintfLine("354 ready")
						body, err := c.ReadDotBytes()
						if err != nil {
							return
						}
						messages <- string(body)
						_ = c.PrintfLine("250 queued")
					case line == "QUIT":
						_ = c.PrintfLine("221 bye")
						return
					default:
						_ = c.PrintfLine("500 unsupported")
					}
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	a.cfg.SMTP = config.SMTPConfig{Host: host, Port: n, From: "sender@example.test", Encryption: "plain", Timeout: time.Second}
	a.cfg.PublicURL = "http://localhost"
	u, err := a.CreateUser(context.Background(), admin.ID, "mail-user@example.test", "Mail Test", "", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	readPassword := func() string {
		t.Helper()
		m, err := stdmail.ReadMessage(strings.NewReader(<-messages))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
		if err != nil {
			t.Fatal(err)
		}
		const marker = "初始临时密码："
		_, tail, found := strings.Cut(string(body), marker)
		if !found {
			t.Fatal("temporary password missing")
		}
		password, _, _ := strings.Cut(tail, "\n")
		return strings.TrimSpace(password)
	}
	firstPassword := readPassword()
	if u.DeliveryStatus != "sent" || len(firstPassword) < 12 {
		t.Fatalf("invalid invitation result: %#v", u)
	}
	firstToken := login(t, a, u.Email, firstPassword)
	u, err = a.ResendWelcome(context.Background(), admin.ID, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondPassword := readPassword()
	if firstPassword == secondPassword {
		t.Fatal("resend reused temporary password")
	}
	if _, err = a.ValidateToken(context.Background(), firstToken); err == nil {
		t.Fatal("resend kept old credential")
	}
	if _, err = a.Login(context.Background(), u.Email, firstPassword); err == nil {
		t.Fatal("resend kept old password")
	}
	login(t, a, u.Email, secondPassword)
	if err = a.ChangePassword(context.Background(), u.ID, secondPassword, "user-permanent-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ResendWelcome(context.Background(), admin.ID, u.ID); err == nil {
		t.Fatal("resend reset completed account")
	}
	var hash string
	if err = a.store.DB().QueryRow("SELECT password_hash FROM users WHERE id=?", u.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, firstPassword) || strings.Contains(hash, secondPassword) {
		t.Fatal("plaintext credential retained in users")
	}
	var leaked int
	if err = a.store.DB().QueryRow("SELECT COUNT(*) FROM mail_deliveries WHERE error LIKE ? OR error LIKE ?", "%"+firstPassword+"%", "%"+secondPassword+"%").Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("plaintext password stored in mail delivery history")
	}
}
