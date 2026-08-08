package smtp_test

import (
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/providertest"
	"github.com/gsoultan/gsmail/smtp"
)

func TestConformance(t *testing.T) {
	providertest.RunSMTP(t, providertest.SMTPHarness{
		Name: "smtp",
		NewSender: func(t *testing.T, host string, port int) gsmail.Sender {
			// No credentials, so the sender takes its unauthenticated plain
			// path against the suite's fake server.
			return smtp.NewSender(host, port, "", "", false)
		},
	})
}

// The same contract must hold when the connection pool is in play. Pooled
// sends reuse a connection across messages, which is exactly where a
// half-reset session or a stale RSET would corrupt the next message.
func TestConformanceWithPool(t *testing.T) {
	providertest.RunSMTP(t, providertest.SMTPHarness{
		Name: "smtp+pool",
		NewSender: func(t *testing.T, host string, port int) gsmail.Sender {
			s := smtp.NewSender(host, port, "", "", false)
			s.EnablePool(smtp.PoolConfig{MaxIdle: 2, MaxOpen: 2, Wait: true})
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	})
}
