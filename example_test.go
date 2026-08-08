package gsmail_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/gsmailtest"
	"github.com/gsoultan/gsmail/smtp"
)

// Examples with an Output comment are executed as tests. The rest are
// compile-checked only, because they would need a network or credentials.

func Example() {
	email := gsmail.Email{
		From:    "no-reply@example.com",
		To:      []string{"alice@example.com"},
		Subject: "Welcome",
	}
	if err := email.SetBody("<h1>Hello {{.Name}}</h1>", map[string]string{"Name": "Alice"}); err != nil {
		log.Fatal(err)
	}

	sender := smtp.NewSender("smtp.example.com", 587, "user", "pass", false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := gsmail.Send(ctx, sender, email); err != nil {
		log.Printf("send: %v", err)
	}
}

// SetBody detects whether a template renders HTML and stores the result in
// the matching field, so a provider never has to guess.
func ExampleEmail_SetBody() {
	var html gsmail.Email
	_ = html.SetBody("<p>Hello {{.Name}}</p>", map[string]string{"Name": "Alice"})

	var text gsmail.Email
	_ = text.SetBody("Hello {{.Name}}", map[string]string{"Name": "Alice"})

	fmt.Printf("html: Body=%q HTMLBody=%q\n", html.Body, html.HTMLBody)
	fmt.Printf("text: Body=%q HTMLBody=%q\n", text.Body, text.HTMLBody)
	// Output:
	// html: Body="" HTMLBody="<p>Hello Alice</p>"
	// text: Body="Hello Alice" HTMLBody=""
}

// Gmail and Yahoo require both headers from bulk senders; either alone does
// not satisfy RFC 8058.
func ExampleEmail_SetOneClickUnsubscribe() {
	var email gsmail.Email
	if err := email.SetOneClickUnsubscribe("https://example.com/u?t=abc123"); err != nil {
		log.Fatal(err)
	}

	fmt.Println(email.Headers["List-Unsubscribe"])
	fmt.Println(email.Headers["List-Unsubscribe-Post"])
	fmt.Println(email.HasOneClickUnsubscribe())
	// Output:
	// <https://example.com/u?t=abc123>
	// List-Unsubscribe=One-Click
	// true
}

// An unsubscribe URL carries a token identifying the recipient, so plain http
// is refused.
func ExampleEmail_SetOneClickUnsubscribe_rejectsUnsafeTargets() {
	var email gsmail.Email

	err := email.SetOneClickUnsubscribe("http://example.com/u")
	fmt.Println(errors.Is(err, gsmail.ErrUnsafeUnsubscribeScheme))

	// One-click works by the provider POSTing, so a mailto: alone cannot
	// satisfy the requirement.
	err = email.SetOneClickUnsubscribe("mailto:unsub@example.com")
	fmt.Println(errors.Is(err, gsmail.ErrNoHTTPSUnsubscribe))
	// Output:
	// true
	// true
}

// Parsing a bounce is only useful if something stops the next send.
func ExampleSuppressionInterceptor() {
	list := gsmail.NewMemorySuppressionList()

	// Normally this comes from a verified provider webhook.
	list.Record(&gsmail.Bounce{
		Type:         gsmail.BounceHard,
		EmailAddress: "gone@example.com",
		Reason:       "550 user unknown",
	})

	sender := gsmail.WrapSender(gsmailtest.NewSender(), gsmail.SuppressionInterceptor(list))

	err := sender.Send(context.Background(), gsmail.Email{
		From: "no-reply@example.com",
		To:   []string{"Gone <GONE@Example.COM>"}, // any spelling of the address
		Body: []byte("hello"),
	})

	fmt.Println(errors.Is(err, gsmail.ErrAllRecipientsSuppressed))
	// Output:
	// true
}

// A soft bounce is a full mailbox or a greylisting relay. Suppressing on one
// would permanently cut off a live recipient.
func ExampleMemorySuppressionList_AddBounce() {
	list := gsmail.NewMemorySuppressionList()

	fmt.Println(list.AddBounce(&gsmail.Bounce{Type: gsmail.BounceSoft, EmailAddress: "full@example.com"}))
	fmt.Println(list.AddBounce(&gsmail.Bounce{Type: gsmail.BounceHard, EmailAddress: "gone@example.com"}))
	fmt.Println(list.Len())
	// Output:
	// false
	// true
	// 1
}

// Failover moves on only for failures worth retrying elsewhere. A permanent
// rejection means the message is the problem, not the provider.
func ExampleFailoverSender() {
	primary := gsmailtest.NewSender()
	primary.FailWith(errors.New("ses unreachable"))
	backup := gsmailtest.NewSender()

	sender := gsmail.FailoverSender(primary, backup)
	err := sender.Send(context.Background(), gsmail.Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi"),
	})

	fmt.Println(err, primary.Count(), backup.Count())
	// Output:
	// <nil> 0 1
}

// Pacing sends avoids provoking a 429, which is better than reacting to one:
// providers count rejected requests against you.
func ExampleRateLimitedSender() {
	inner := gsmailtest.NewSender()
	sender := gsmail.RateLimitedSender(inner, gsmail.NewTokenBucket(time.Second/50, 10))

	for range 3 {
		_ = sender.Send(context.Background(), gsmail.Email{
			From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi"),
		})
	}
	fmt.Println(inner.Count())
	// Output:
	// 3
}

// TokenSource is called on every send and every retry, so an uncached one is a
// round trip to the identity provider per message.
func ExampleCachingTokenSource() {
	var refreshes int
	ts := gsmail.CachingTokenSource(func(context.Context) (string, time.Time, error) {
		refreshes++
		return "ya29.token", time.Now().Add(time.Hour), nil
	}, time.Minute)

	for range 100 {
		if _, err := ts(context.Background()); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println(refreshes)
	// Output:
	// 1
}

// A suppression list keyed by the raw string would miss a bounce recorded
// under a different spelling of the same mailbox.
func ExampleNormalizeAddress() {
	for _, in := range []string{
		"alice@example.com",
		"Alice <Alice@Example.COM>",
		`"Doe, John" <J.Doe@Example.com>`,
	} {
		fmt.Println(gsmail.NormalizeAddress(in))
	}
	// Output:
	// alice@example.com
	// alice@example.com
	// j.doe@example.com
}

// IsHTML decides the Content-Type. It requires a real tag, so prose that
// merely contains an angle bracket is not misread as markup.
func ExampleIsHTML() {
	fmt.Println(gsmail.IsHTML([]byte("<p>Hello</p>")))
	fmt.Println(gsmail.IsHTML([]byte("Please review <p1> pricing.")))
	// Output:
	// true
	// false
}

// An address containing a line break cannot appear in a header, so it is
// dropped rather than escaped.
func ExampleFormatAddresses() {
	fmt.Println(gsmail.FormatAddresses([]string{
		"alice@example.com",
		"Bob Smith <bob@example.com>",
		"evil@example.com\r\nBcc: attacker@evil.test",
	}))
	// Output:
	// alice@example.com, "Bob Smith" <bob@example.com>
}

// RenderMessage produces the complete RFC 5322 message a Sender transmits.
func ExampleRenderMessage() {
	email := gsmail.Email{
		From:    "no-reply@example.com",
		To:      []string{"alice@example.com"},
		Subject: "Receipt",
		Body:    []byte("Thanks for your order."),
	}
	email.SetHeader("X-Order-ID", "12345")

	raw, err := gsmail.RenderMessage(email)
	if err != nil {
		log.Fatal(err)
	}
	_ = raw // Date and Message-ID are generated, so the bytes vary per call.
}

// WithMessage renders into a pooled buffer for hot paths. The slice is only
// valid until the callback returns.
func ExampleWithMessage() {
	email := gsmail.Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi"),
	}

	err := gsmail.WithMessage(email, func(msg []byte) error {
		fmt.Println(len(msg) > 0)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	// Output:
	// true
}

// Retry gives up immediately on a permanent failure rather than repeating a
// rejection the server has already made.
func ExampleRetry() {
	attempts := 0
	err := gsmail.Retry(context.Background(), gsmail.RetryConfig{
		MaxRetries:      5,
		InitialInterval: time.Millisecond,
	}, func() error {
		attempts++
		return gsmail.NonRetryable(errors.New("recipient does not exist"))
	})

	fmt.Println(attempts, err != nil)
	// Output:
	// 1 true
}

// Verify a webhook before parsing it: the Parse functions accept
// unauthenticated input, and a forged bounce suppresses a real customer.
func ExampleSNSVerifier() {
	verifier := &gsmail.SNSVerifier{
		// Always set this. A valid signature proves the message came from SNS,
		// not that it came from your topic.
		TopicARNs: []string{"arn:aws:sns:us-east-1:123456789012:ses-events"},
	}

	http.HandleFunc("/webhooks/ses", func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0)
		// ... read r.Body into body ...

		msg, err := verifier.Verify(r.Context(), body)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		event, err := gsmail.ParseSESWebhook([]byte(msg.Message))
		if err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		_ = event // feed it to a Suppressor
	})
}

// Interceptors wrap a sender, so behaviour composes and applies to every
// provider identically. The first is the outermost.
func ExampleWrapSender() {
	sender := gsmail.WrapSender(gsmailtest.NewSender(),
		gsmail.RecoveryInterceptor(),
		gsmail.LoggerInterceptor(log.Printf),
	)

	_ = sender.Send(context.Background(), gsmail.Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi"),
	})
}
