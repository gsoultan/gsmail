package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"

	"github.com/gsoultan/gsmail"
)

// Sender represents the SMTP server configuration and implements the Sender interface.
type Sender struct {
	gsmail.BaseProvider
	Host     string
	Port     int
	Username string
	Password string
	SSL      bool
}

// NewSender creates a new SMTP provider.
func NewSender(host string, port int, username, password string, ssl bool) *Sender {
	return &Sender{
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		SSL:      ssl,
	}
}

// Send sends an email using the SMTP configuration.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	addr := net.JoinHostPort(p.Host, fmt.Sprintf("%d", p.Port))
	auth := smtp.PlainAuth("", p.Username, p.Password, p.Host)

	bufPtr := gsmail.GetBuffer()
	defer gsmail.PutBuffer(bufPtr)

	// Build the email message
	gsmail.BuildMessage(bufPtr, email)

	if p.SSL {
		return p.sendWithSSL(ctx, addr, auth, email.From, email.To, *bufPtr)
	}

	return p.sendPlain(ctx, addr, auth, email.From, email.To, *bufPtr)
}

func (p *Sender) sendOnClient(client *smtp.Client, from string, to []string, msg []byte) error {
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}

	for _, t := range to {
		if err := client.Rcpt(t); err != nil {
			return fmt.Errorf("smtp rcpt to %s: %w", t, err)
		}
	}

	return p.writeData(client, msg)
}

func (p *Sender) writeData(client *smtp.Client, msg []byte) error {
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}

	if _, err = w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("write message: %w", err)
	}

	if err := w.Close(); err != nil {
		return fmt.Errorf("close data writer: %w", err)
	}

	return nil
}

func (p *Sender) authenticateAndSend(client *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if ok, _ := client.Extension("AUTH"); !ok {
			return fmt.Errorf("smtp server does not support AUTH")
		}
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	return p.sendOnClient(client, from, to, msg)
}

func (p *Sender) sendPlain(ctx context.Context, addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host, client, err := p.dial(ctx, addr, false)
	if err != nil {
		return err
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		config := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if err = client.StartTLS(config); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if err = p.authenticateAndSend(client, auth, from, to, msg); err != nil {
		return err
	}

	_ = client.Quit()
	return nil
}

func (p *Sender) dial(ctx context.Context, addr string, useSSL bool) (string, *smtp.Client, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", nil, fmt.Errorf("split host port: %w", err)
	}

	d := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", nil, fmt.Errorf("dial: %w", err)
	}

	if useSSL {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = tlsConn.Close()
			return "", nil, fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return "", nil, fmt.Errorf("new smtp client: %w", err)
	}

	return host, client, nil
}

func (p *Sender) sendWithSSL(ctx context.Context, addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	_, client, err := p.dial(ctx, addr, true)
	if err != nil {
		return err
	}
	defer client.Close()

	if err = p.authenticateAndSend(client, auth, from, to, msg); err != nil {
		return err
	}

	_ = client.Quit()
	return nil
}
