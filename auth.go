package gsmail

import (
	"context"
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

// SMTPAuth wraps sasl.Client to implement net/smtp.Auth.
// This allows using SASL mechanisms from github.com/emersion/go-sasl with net/smtp.
type SMTPAuth struct {
	client sasl.Client
}

// Start begins an authentication with a server.
func (a *SMTPAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return a.client.Start()
}

// Next continues the authentication.
func (a *SMTPAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return a.client.Next(fromServer)
}

// NewXOAUTH2Auth returns a net/smtp.Auth that implements the XOAUTH2 mechanism.
func NewXOAUTH2Auth(username, token string) smtp.Auth {
	return &SMTPAuth{
		client: &xoauth2Client{
			Username: username,
			Token:    token,
		},
	}
}

// NewOAuthBearerAuth returns a net/smtp.Auth that implements the OAUTHBEARER mechanism.
func NewOAuthBearerAuth(username, token string) smtp.Auth {
	return &SMTPAuth{
		client: sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
			Username: username,
			Token:    token,
		}),
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
