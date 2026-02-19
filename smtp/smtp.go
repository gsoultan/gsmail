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
	Host               string
	Port               int
	Username           string
	Password           string
	SSL                bool
	InsecureSkipVerify bool
	Pool               *Pool

	// Modern auth
	AuthMethod        gsmail.AuthMethod
	TokenSource       gsmail.TokenSource // provides OAuth2 bearer token when AuthMethod is XOAUTH2 or OAUTHBEARER
	AllowInsecureAuth bool               // allow AUTH without TLS (NOT recommended); default false

	// Deliverability
	DKIMConfig *gsmail.DKIMOptions
}

// NewSender creates a new SMTP provider.
func NewSender(host string, port int, username, password string, ssl bool) *Sender {
	return &Sender{
		Host:               host,
		Port:               port,
		Username:           username,
		Password:           password,
		SSL:                ssl,
		InsecureSkipVerify: false,
	}
}

// UseOAuth configures the sender to use OAuth2 with the specified method and token source.
func (p *Sender) UseOAuth(method gsmail.AuthMethod, ts gsmail.TokenSource) {
	p.AuthMethod = method
	p.TokenSource = ts
}

// Send sends an email using the SMTP configuration.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	addr := net.JoinHostPort(p.Host, fmt.Sprintf("%d", p.Port))

	bufPtr := gsmail.GetBuffer()
	defer gsmail.PutBuffer(bufPtr)

	// Build the email message
	gsmail.BuildMessage(bufPtr, email)

	// DKIM Signing
	if p.DKIMConfig != nil {
		signed, err := gsmail.SignDKIM(*bufPtr, *p.DKIMConfig)
		if err != nil {
			return fmt.Errorf("dkim sign: %w", err)
		}
		*bufPtr = signed
	}

	// Collect all recipients
	recipients := make([]string, 0, len(email.To)+len(email.Cc)+len(email.Bcc))
	recipients = append(recipients, email.To...)
	recipients = append(recipients, email.Cc...)
	recipients = append(recipients, email.Bcc...)

	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		if p.Pool != nil {
			client, err := p.Pool.Get(ctx)
			if err != nil {
				return err
			}
			err = p.sendOnClient(client, email.From, recipients, *bufPtr)
			p.Pool.Put(client, err)
			return err
		}

		// Build auth on demand
		var auth smtp.Auth
		var isOAuth bool
		if p.AuthMethod == gsmail.AuthXOAUTH2 || p.AuthMethod == gsmail.AuthOAUTHBEARER {
			isOAuth = true
			if p.TokenSource == nil {
				return fmt.Errorf("oauth2 token source is nil")
			}
			tok, err := p.TokenSource(ctx)
			if err != nil {
				return fmt.Errorf("token source: %w", err)
			}
			if p.AuthMethod == gsmail.AuthXOAUTH2 {
				auth = gsmail.NewXOAUTH2Auth(p.Username, tok)
			} else {
				auth = gsmail.NewOAuthBearerAuth(p.Username, tok)
			}
		} else if p.Username != "" {
			auth = smtp.PlainAuth("", p.Username, p.Password, p.Host)
		}

		if p.SSL {
			return p.sendWithSSL(ctx, addr, auth, email.From, recipients, *bufPtr)
		}

		return p.sendPlain(ctx, addr, auth, email.From, recipients, *bufPtr, isOAuth)
	})
}

// EnablePool enables the connection pool with the given configuration.
func (p *Sender) EnablePool(config PoolConfig) {
	p.Pool = NewPool(config, func(ctx context.Context) (*smtp.Client, error) {
		addr := net.JoinHostPort(p.Host, fmt.Sprintf("%d", p.Port))
		host, client, err := p.dial(ctx, addr, p.SSL)
		if err != nil {
			return nil, err
		}

		// Handle STARTTLS if not using SSL and it's supported
		tlsOn := p.SSL
		if !p.SSL {
			if ok, _ := client.Extension("STARTTLS"); ok {
				config := &tls.Config{
					ServerName:         host,
					MinVersion:         tls.VersionTLS12,
					InsecureSkipVerify: p.InsecureSkipVerify,
				}
				if err = client.StartTLS(config); err != nil {
					_ = client.Close()
					return nil, fmt.Errorf("starttls: %w", err)
				}
				tlsOn = true
			}
		}

		// Authenticate if configured
		var auth smtp.Auth
		if p.AuthMethod == gsmail.AuthXOAUTH2 || p.AuthMethod == gsmail.AuthOAUTHBEARER {
			if !tlsOn && !p.AllowInsecureAuth {
				_ = client.Close()
				return nil, fmt.Errorf("oauth2 requires TLS; enable SSL/STARTTLS or AllowInsecureAuth for testing")
			}
			if p.TokenSource == nil {
				_ = client.Close()
				return nil, fmt.Errorf("oauth2 token source is nil")
			}
			tok, err := p.TokenSource(ctx)
			if err != nil {
				_ = client.Close()
				return nil, fmt.Errorf("token source: %w", err)
			}
			if p.AuthMethod == gsmail.AuthXOAUTH2 {
				auth = gsmail.NewXOAUTH2Auth(p.Username, tok)
			} else {
				auth = gsmail.NewOAuthBearerAuth(p.Username, tok)
			}
		} else if p.Username != "" {
			auth = smtp.PlainAuth("", p.Username, p.Password, host)
		}

		if auth != nil {
			if ok, _ := client.Extension("AUTH"); !ok {
				_ = client.Close()
				return nil, fmt.Errorf("smtp server does not support AUTH")
			}
			if err := client.Auth(auth); err != nil {
				_ = client.Close()
				return nil, fmt.Errorf("smtp auth: %w", err)
			}
		}

		return client, nil
	})
}

// Close closes the connection pool if it is enabled.
func (p *Sender) Close() error {
	if p.Pool != nil {
		return p.Pool.Close()
	}
	return nil
}

// Ping checks the connection to the SMTP server.
func (p *Sender) Ping(ctx context.Context) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		addr := net.JoinHostPort(p.Host, fmt.Sprintf("%d", p.Port))
		_, client, err := p.dial(ctx, addr, p.SSL)
		if err != nil {
			return err
		}
		defer client.Close()

		if err := client.Noop(); err != nil {
			return fmt.Errorf("smtp noop: %w", err)
		}

		return client.Quit()
	})
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

func (p *Sender) sendPlain(ctx context.Context, addr string, auth smtp.Auth, from string, to []string, msg []byte, requireTLS bool) error {
	host, client, err := p.dial(ctx, addr, false)
	if err != nil {
		return err
	}
	defer client.Close()

	tlsOn := false
	if ok, _ := client.Extension("STARTTLS"); ok {
		config := &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: p.InsecureSkipVerify,
		}
		if err = client.StartTLS(config); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
		tlsOn = true
	}

	if requireTLS && !tlsOn && !p.AllowInsecureAuth {
		return fmt.Errorf("oauth2 requires TLS; enable SSL/STARTTLS or AllowInsecureAuth for testing")
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
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: p.InsecureSkipVerify,
		})
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
