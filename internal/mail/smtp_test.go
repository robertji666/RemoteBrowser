package mail

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	stdmail "net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/robertji666/RemoteBrowser/internal/config"
)

type receivedMessage struct{ Recipient, Body string }

func fakeSMTP(t *testing.T, reject bool) (config.SMTPConfig, <-chan receivedMessage) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	messages := make(chan receivedMessage, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 localhost test SMTP\r\n")
				recipient := ""
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250 localhost\r\n")
					case strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "MAIL FROM"):
						fmt.Fprint(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "RCPT TO"):
						if reject {
							fmt.Fprint(conn, "550 secret-password-should-not-leak\r\n")
						} else {
							recipient = strings.TrimSpace(line)
							fmt.Fprint(conn, "250 OK\r\n")
						}
					case strings.HasPrefix(line, "DATA"):
						fmt.Fprint(conn, "354 send body\r\n")
						var body strings.Builder
						for {
							text, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if text == ".\r\n" {
								break
							}
							body.WriteString(text)
						}
						messages <- receivedMessage{recipient, body.String()}
						fmt.Fprint(conn, "250 queued\r\n")
					case strings.HasPrefix(line, "QUIT"):
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "500 unknown\r\n")
					}
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	return config.SMTPConfig{Host: host, Port: n, From: "RemoteBrowser <sender@example.test>", Encryption: "plain", Timeout: time.Second}, messages
}
func TestSMTPDeliverCredentialAndTestMessages(t *testing.T) {
	cfg, messages := fakeSMTP(t, false)
	s := Sender{Config: cfg, PublicURL: "https://browser.example.test"}
	if err := s.SendCredentials(context.Background(), "recipient@example.test", "random-temporary-password", "welcome"); err != nil {
		t.Fatal(err)
	}
	got := <-messages
	if got.Recipient != "RCPT TO:<recipient@example.test>" {
		t.Fatalf("wrong recipient %q", got.Recipient)
	}
	m, err := stdmail.ReadMessage(strings.NewReader(got.Body))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"recipient@example.test", "random-temporary-password", "https://browser.example.test/login", "首次登录"} {
		if !strings.Contains(string(body), expected) {
			t.Errorf("missing %q in credential message", expected)
		}
	}
	if err = s.SendTest(context.Background(), "test@example.test"); err != nil {
		t.Fatal(err)
	}
	if !(strings.Contains((<-messages).Recipient, "test@example.test")) {
		t.Fatal("test sent to wrong recipient")
	}
}
func TestSMTPRejectSanitizesProviderError(t *testing.T) {
	cfg, _ := fakeSMTP(t, true)
	err := (Sender{Config: cfg, PublicURL: "http://localhost"}).SendTest(context.Background(), "recipient@example.test")
	if err == nil || !strings.Contains(err.Error(), "550") || strings.Contains(err.Error(), "secret-password") {
		t.Fatalf("error not safely actionable: %v", err)
	}
}
func TestSMTPRequiresTLSAndValidConfiguration(t *testing.T) {
	base := Sender{Config: config.SMTPConfig{Host: "smtp.example.test", Port: 587, From: "sender@example.test", Encryption: "starttls"}, PublicURL: "https://browser.example.test"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Sender){
		"remote plain":       func(s *Sender) { s.Config.Encryption = "plain" },
		"missing public url": func(s *Sender) { s.PublicURL = "" },
		"injected sender":    func(s *Sender) { s.Config.From = "sender@example.test\r\nBcc: secret@example.test" },
		"missing password":   func(s *Sender) { s.Config.Username = "user" },
		"bad port":           func(s *Sender) { s.Config.Port = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			change(&s)
			if s.Validate() == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	cfg, _ := fakeSMTP(t, false)
	cfg.Encryption = "starttls"
	if err := (Sender{Config: cfg, PublicURL: "http://localhost"}).SendTest(context.Background(), "recipient@example.test"); err == nil {
		t.Fatal("STARTTLS silently downgraded to cleartext")
	}
}
func TestSMTPTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, c)
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(port)
	s := Sender{Config: config.SMTPConfig{Host: host, Port: n, From: "sender@example.test", Encryption: "plain", Timeout: 30 * time.Millisecond}, PublicURL: "http://localhost"}
	started := time.Now()
	err = s.SendTest(context.Background(), "recipient@example.test")
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("send was not bounded: %v", err)
	}
	<-done
}
