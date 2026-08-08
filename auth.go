package gsmail

import (
	"context"
	"errors"
	"fmt"
	"net/smtp"

	"github.com/emersion/go-sasl"
)

// AuthMethod represents the authentication method.
type AuthMethod string

// TokenSource returns a fresh access token (e.g., OAuth2 bearer token).
// Implementations should be safe for concurrent use and may refresh/rotate tokens.
type TokenSource func(ctx context.Context) (string, error)

const (
	// AuthPlain represents the standard PLAIN authentication (username/password).
	AuthPlain AuthMethod = "PLAIN"
	// AuthXOAUTH2 represents the XOAUTH2 authentication (used by Gmail, Outlook).
	AuthXOAUTH2 AuthMethod = "XOAUTH2"
	// AuthOAUTHBEARER represents the OAUTHBEARER authentication (RFC 7628).
	AuthOAUTHBEARER AuthMethod = "OAUTHBEARER"
)

// ErrInsecureAuth is returned when credentials would be sent over an
// unencrypted connection.
var ErrInsecureAuth = errors.New("gsmail: refusing to send credentials over an unencrypted connection")

// SMTPAuth wraps sasl.Client to implement net/smtp.Auth.
// This allows using SASL mechanisms from github.com/emersion/go-sasl with net/smtp.
type SMTPAuth struct {
	client sasl.Client

	// allowInsecure permits authentication over a plaintext connection.
	// It exists for tests and for local relays that cannot offer TLS.
	allowInsecure bool
}

// AllowInsecure permits this authenticator to run over a connection without
// TLS. Only use it against a trusted local relay or in tests.
func (a *SMTPAuth) AllowInsecure() *SMTPAuth {
	a.allowInsecure = true
	return a
}

// Start begins an authentication with a server.
//
// Unlike the bare sasl.Client, it refuses to hand over credentials on a
// connection without TLS. net/smtp.PlainAuth makes the same check; without it
// a STARTTLS-stripping man in the middle would collect the bearer token.
func (a *SMTPAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server != nil && !server.TLS && !a.allowInsecure && !isLocalhost(server.Name) {
		return "", nil, ErrInsecureAuth
	}
	return a.client.Start()
}

// isLocalhost reports whether the server is on the loopback interface, where
// an unencrypted authentication cannot be intercepted from the network. This
// mirrors the exemption net/smtp.PlainAuth makes.
func isLocalhost(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}

// Next continues the authentication.
func (a *SMTPAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return a.client.Next(fromServer)
}

// NewXOAUTH2Auth returns a net/smtp.Auth that implements the XOAUTH2 mechanism.
// It refuses to authenticate over a connection without TLS.
func NewXOAUTH2Auth(username, token string) smtp.Auth {
	return newXOAUTH2Auth(username, token, false)
}

// NewXOAUTH2AuthInsecure is NewXOAUTH2Auth without the TLS requirement.
// Only use it against a trusted local relay or in tests.
func NewXOAUTH2AuthInsecure(username, token string) smtp.Auth {
	return newXOAUTH2Auth(username, token, true)
}

func newXOAUTH2Auth(username, token string, allowInsecure bool) smtp.Auth {
	return &SMTPAuth{
		client: &xoauth2Client{
			Username: username,
			Token:    token,
		},
		allowInsecure: allowInsecure,
	}
}

// NewOAuthBearerAuth returns a net/smtp.Auth that implements the OAUTHBEARER
// mechanism. It refuses to authenticate over a connection without TLS.
func NewOAuthBearerAuth(username, token string) smtp.Auth {
	return newOAuthBearerAuth(username, token, false)
}

// NewOAuthBearerAuthInsecure is NewOAuthBearerAuth without the TLS
// requirement. Only use it against a trusted local relay or in tests.
func NewOAuthBearerAuthInsecure(username, token string) smtp.Auth {
	return newOAuthBearerAuth(username, token, true)
}

func newOAuthBearerAuth(username, token string, allowInsecure bool) smtp.Auth {
	return &SMTPAuth{
		client: sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
			Username: username,
			Token:    token,
		}),
		allowInsecure: allowInsecure,
	}
}

// NewXOAUTH2Client exposes a SASL client for XOAUTH2 (useful for IMAP AUTH).
func NewXOAUTH2Client(username, token string) sasl.Client {
	return &xoauth2Client{Username: username, Token: token}
}

// xoauth2Client implements sasl.Client for XOAUTH2.
type xoauth2Client struct {
	Username string
	Token    string
}

func (c *xoauth2Client) Start() (string, []byte, error) {
	ir := []byte(fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", c.Username, c.Token))
	return "XOAUTH2", ir, nil
}

func (c *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	return nil, nil
}
