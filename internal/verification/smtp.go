package verification

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type SMTPConfig struct {
	Address, Host, Username, Password, From string
	RequireTLS                              bool
}

type SMTPSender struct{ config SMTPConfig }

func NewSMTPSender(config SMTPConfig) *SMTPSender { return &SMTPSender{config: config} }

func (s *SMTPSender) SendVerification(ctx context.Context, recipient, verificationURL string) error {
	subject := "Cloudlet e-posta adresini doğrula"
	body := "Cloudlet hesabını doğrulamak için aşağıdaki bağlantıyı aç:\r\n\r\n" + verificationURL + "\r\n\r\nBu bağlantı 24 saat geçerlidir."
	return s.send(ctx, recipient, subject, body)
}

func (s *SMTPSender) SendPasswordReset(ctx context.Context, recipient, resetURL string) error {
	subject := "Cloudlet parolanı yenile"
	body := "Cloudlet parolanı yenilemek için aşağıdaki bağlantıyı aç:\r\n\r\n" + resetURL + "\r\n\r\nBu bağlantı 30 dakika geçerlidir. Bu isteği sen yapmadıysan e-postayı yok sayabilirsin."
	return s.send(ctx, recipient, subject, body)
}

func (s *SMTPSender) SendShareNotification(ctx context.Context, recipient, fileName, ownerEmail string) error {
	subject := "Cloudlet dosya paylaşımı"
	body := ownerEmail + " seninle “" + fileName + "” dosyasını Cloudlet üzerinde paylaştı. Cloudlet hesabına giriş yaparak Paylaşılanlar alanından dosyaya erişebilirsin."
	return s.send(ctx, recipient, subject, body)
}

func (s *SMTPSender) send(ctx context.Context, recipient, subject, body string) error {
	message := strings.Join([]string{
		"From: " + s.config.From,
		"To: " + recipient,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", s.config.Address)
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	}
	client, err := smtp.NewClient(connection, s.config.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()
	if supported, _ := client.Extension("STARTTLS"); supported {
		if err = client.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	} else if s.config.RequireTLS {
		return fmt.Errorf("smtp server does not support required STARTTLS")
	}
	if s.config.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
			return fmt.Errorf("smtp authenticate: %w", err)
		}
	}
	if err = client.Mail(s.config.From); err != nil {
		return fmt.Errorf("smtp sender: %w", err)
	}
	if err = client.Rcpt(recipient); err != nil {
		return fmt.Errorf("smtp recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err = writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err = writer.Close(); err != nil {
		return fmt.Errorf("smtp finish message: %w", err)
	}
	if err = client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}
