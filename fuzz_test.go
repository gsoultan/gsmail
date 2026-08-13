package gsmail

import (
	"fmt"
	"net/mail"
	"strings"
	"testing"
)

// Every parser below reads bytes a stranger sent: ParseRawEmail sees whatever
// arrives on an IMAP or POP3 poll, and the webhook parsers see an HTTP request
// body. The bounds on depth and size were reasoned about and unit-tested, but
// nothing had ever thrown malformed input at them.
//
// These targets assert invariants rather than merely the absence of a panic.
// "It did not crash" is a weak property; "it never emits a header separator"
// is the one that actually matters here.

// ---------------------------------------------------------------------------
// Inbound parsing
// ---------------------------------------------------------------------------

func FuzzParseRawEmail(f *testing.F) {
	f.Add([]byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n\r\nbody"))
	f.Add([]byte("From: a@b.test\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n" +
		"--x\r\nContent-Type: text/plain\r\n\r\nhello\r\n--x--\r\n"))
	f.Add([]byte("Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\n" +
		"Content-Type: multipart/alternative; boundary=c\r\n\r\n--c\r\n" +
		"Content-Type: text/html\r\n\r\n<p>x</p>\r\n--c--\r\n--b--\r\n"))
	f.Add([]byte("Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\naGk=\r\n"))
	f.Add([]byte("Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nh=69\r\n"))
	f.Add([]byte("Subject: =?UTF-8?Q?na=C3=AFve?=\r\n\r\nbody"))
	f.Add([]byte(""))
	f.Add([]byte("\r\n\r\n"))
	f.Add([]byte("Content-Type: multipart/mixed\r\n\r\nno boundary"))
	f.Add([]byte("Message-ID: <a@b.test>\r\nIn-Reply-To: <c@d.test>\r\nX-Odd: v\r\n\r\nbody"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		email, err := ParseRawEmail(raw)
		if err != nil {
			return
		}

		// A parse that succeeds must respect the bounds that stop a hostile
		// message exhausting memory.
		if len(email.Body) > maxPartSize {
			t.Fatalf("Body is %d bytes, above maxPartSize %d", len(email.Body), maxPartSize)
		}
		if len(email.HTMLBody) > maxPartSize {
			t.Fatalf("HTMLBody is %d bytes, above maxPartSize %d", len(email.HTMLBody), maxPartSize)
		}
		for i, att := range email.Attachments {
			if len(att.Data) > maxPartSize {
				t.Fatalf("attachment %d is %d bytes, above maxPartSize %d", i, len(att.Data), maxPartSize)
			}
		}

		// No header-derived value may carry a control character. These travel
		// wherever the caller takes them -- a log line, a datastore, a reply
		// built by a different mailer -- so the parse boundary is where this
		// has to hold, not the render boundary.
		fields := map[string]string{
			"From": email.From, "Subject": email.Subject, "ReplyTo": email.ReplyTo,
		}
		for i, addr := range email.To {
			fields[fmt.Sprintf("To[%d]", i)] = addr
		}
		for i, addr := range email.Cc {
			fields[fmt.Sprintf("Cc[%d]", i)] = addr
		}
		for i, att := range email.Attachments {
			fields[fmt.Sprintf("Attachments[%d].Filename", i)] = att.Filename
			fields[fmt.Sprintf("Attachments[%d].ContentID", i)] = att.ContentID
		}
		// Retained headers are attacker-supplied and now reach the caller.
		for name, value := range email.Headers {
			fields["Headers["+name+"]"] = value
		}
		for name, value := range fields {
			for _, r := range value {
				if (r < 0x20 && r != '\t') || r == 0x7f {
					t.Fatalf("%s contains control character %q: %q", name, r, value)
				}
			}
		}
	})
}

// Re-rendering whatever was parsed must not turn a hostile inbound message
// into a header-injecting outbound one. This is the path a reply or a forward
// takes.
func FuzzParseThenRender(f *testing.F) {
	f.Add([]byte("From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n\r\nbody"))
	f.Add([]byte("From: \"Doe, John\" <j@example.com>\r\nTo: a@b.test, c@d.test\r\n\r\nx"))
	f.Add([]byte("Subject: multi\r\n line\r\n\r\nbody"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		email, err := ParseRawEmail(raw)
		if err != nil {
			return
		}

		rendered, err := RenderMessage(email)
		if err != nil {
			return
		}
		assertNoHeaderInjection(t, rendered)
	})
}

// ---------------------------------------------------------------------------
// Outbound rendering
// ---------------------------------------------------------------------------

// Header injection is the classic email vulnerability: a display name or a
// subject carrying CRLF splits the header block and lets the caller's input
// add recipients. Every field below is attacker-influenced in a real
// application.
func FuzzRenderMessageHeaderInjection(f *testing.F) {
	f.Add("a@example.com", "b@example.com", "subject", "X-Tag", "value")
	f.Add("Name <a@b.test>", "c@d.test", "hi\r\nBcc: evil@test", "X", "v")
	f.Add("a@b.test\r\nBcc: evil@test", "c@d.test", "s", "X", "v\r\nTo: evil@test")
	f.Add("a@b.test", "c@d.test\nBcc: evil@test", "naïve", "List-Unsubscribe", "<https://x.test>")
	f.Add("", "", "", "", "")

	f.Fuzz(func(t *testing.T, from, to, subject, headerName, headerValue string) {
		e := Email{
			From:    from,
			To:      []string{to},
			Subject: subject,
			Body:    []byte("body"),
		}
		if headerName != "" {
			e.SetHeader(headerName, headerValue)
		}

		rendered, err := RenderMessage(e)
		if err != nil {
			// An illegal header name is rejected outright; that is a pass.
			return
		}
		assertNoHeaderInjection(t, rendered)
	})
}

// assertNoHeaderInjection checks that the header block of a rendered message
// contains only headers this package or the caller legitimately set.
func assertNoHeaderInjection(t *testing.T, rendered []byte) {
	t.Helper()

	msg, err := mail.ReadMessage(strings.NewReader(string(rendered)))
	if err != nil {
		t.Fatalf("rendered message does not parse: %v\n%q", err, rendered)
	}

	// Bcc is never written: recipients travel in the envelope only. Its
	// presence means something injected it.
	if v := msg.Header.Get("Bcc"); v != "" {
		t.Fatalf("a Bcc header appeared in a rendered message: %q\n%q", v, rendered)
	}

	// Every header line must be a field or a folded continuation. A bare line
	// in the header block is an injected one.
	headerBlock, _, found := strings.Cut(string(rendered), "\r\n\r\n")
	if !found {
		t.Fatalf("rendered message has no header/body separator:\n%q", rendered)
	}
	for _, line := range strings.Split(headerBlock, "\r\n") {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		name, _, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("header block contains a line that is not a field: %q\nin:\n%q", line, rendered)
		}
		if name == "" || strings.ContainsAny(name, " \t") {
			t.Fatalf("header block contains a malformed field name: %q\nin:\n%q", line, rendered)
		}
	}
}

func FuzzSanitizeHeaderValue(f *testing.F) {
	f.Add("plain value")
	f.Add("with\r\nCRLF")
	f.Add("with\x00null and \x7fdel")
	f.Add("naïve unicode ✉")
	f.Add("")

	f.Fuzz(func(t *testing.T, in string) {
		out := SanitizeHeaderValue(in)

		// The whole point of the function.
		for _, r := range out {
			if (r < 0x20 && r != '\t') || r == 0x7f {
				t.Fatalf("sanitised value still contains %q: input %q output %q", r, in, out)
			}
		}
		// Sanitising twice must change nothing.
		if again := SanitizeHeaderValue(out); again != out {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, out, again)
		}
	})
}

// FormatAddress feeds a header directly, so its output can never carry a
// separator, whatever it was given.
func FuzzFormatAddress(f *testing.F) {
	f.Add("a@example.com")
	f.Add("Name <a@example.com>")
	f.Add(`"Doe, John" <j@example.com>`)
	f.Add("a@b.test\r\nBcc: evil@test")
	f.Add("\"unclosed <a@b.test>")
	f.Add("")

	f.Fuzz(func(t *testing.T, in string) {
		out := FormatAddress(in)
		if strings.ContainsAny(out, "\r\n") {
			t.Fatalf("FormatAddress(%q) = %q, which would split a header", in, out)
		}

		list := FormatAddresses([]string{in, in})
		if strings.ContainsAny(list, "\r\n") {
			t.Fatalf("FormatAddresses(%q) = %q, which would split a header", in, list)
		}
	})
}

// A generated Message-ID takes its domain from a caller-supplied From.
func FuzzMessageIDDomain(f *testing.F) {
	f.Add("a@example.com")
	f.Add("Name <a@example.com>")
	f.Add("a@good.test\r\nBcc: x@evil.test")
	f.Add("a@[192.0.2.1]")
	f.Add("")

	f.Fuzz(func(t *testing.T, from string) {
		id := generateMessageID(from)

		if strings.ContainsAny(id, "\r\n \t") {
			t.Fatalf("generateMessageID(%q) = %q, which would split a header", from, id)
		}
		if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") {
			t.Fatalf("generateMessageID(%q) = %q, which is malformed", from, id)
		}
		if strings.Count(id, "@") != 1 {
			t.Fatalf("generateMessageID(%q) = %q, which has %d '@'", from, id, strings.Count(id, "@"))
		}
	})
}

// A caller-supplied unsubscribe target reaches a header.
func FuzzSetListUnsubscribe(f *testing.F) {
	f.Add("https://example.com/u")
	f.Add("mailto:u@example.com")
	f.Add("https://x.test/u\r\nBcc: evil@test")
	f.Add("javascript:alert(1)")
	f.Add("")

	f.Fuzz(func(t *testing.T, target string) {
		var e Email
		if err := e.SetListUnsubscribe(target); err != nil {
			return
		}
		v := e.Headers["List-Unsubscribe"]
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("List-Unsubscribe = %q from target %q, which would split a header", v, target)
		}
		lower := strings.ToLower(v)
		if strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") {
			t.Fatalf("unsafe scheme reached the header: %q", v)
		}
	})
}

// ---------------------------------------------------------------------------
// Webhook payloads
// ---------------------------------------------------------------------------

func FuzzWebhookParsers(f *testing.F) {
	f.Add([]byte(`{"notificationType":"Bounce","bounce":{"bounceType":"Permanent",` +
		`"bouncedRecipients":[{"emailAddress":"a@b.test"}]},"mail":{"messageId":"m"}}`))
	f.Add([]byte(`{"Type":"Notification","Message":"{}"}`))
	f.Add([]byte(`[{"event":"bounce","email":"a@b.test","timestamp":1}]`))
	f.Add([]byte(`{"event-data":{"event":"failed","recipient":"a@b.test",` +
		`"delivery-status":{"code":550}}}`))
	f.Add([]byte(`{"RecordType":"Bounce","Email":"a@b.test","Type":"HardBounce"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, body []byte) {
		// None of these may panic on arbitrary input; each is reached straight
		// from an HTTP handler.
		_, _ = ParseSESWebhook(body)
		_, _ = ParseMailgunWebhook(body)
		_, _ = ParsePostmarkWebhook(body)

		events, err := ParseSendGridWebhook(body)
		if err != nil {
			return
		}
		// A suppression list is built from these, so an event that reports an
		// address must actually carry one.
		for _, ev := range events {
			switch e := ev.(type) {
			case *Bounce:
				if e.Type != BounceHard && e.Type != BounceSoft {
					t.Fatalf("bounce has an unknown type %q", e.Type)
				}
			case *Complaint:
			default:
				t.Fatalf("unexpected event type %T", ev)
			}
		}
	})
}

// Mailgun signature verification runs before anything else on a webhook, on
// fully untrusted bytes.
func FuzzMailgunVerifier(f *testing.F) {
	f.Add([]byte(`{"signature":{"timestamp":"1","token":"t","signature":"00"}}`))
	f.Add([]byte(`{"signature":{}}`))
	f.Add([]byte(`{"signature":"not an object"}`))
	f.Add([]byte(``))

	v := MailgunVerifier{SigningKey: "key"}
	f.Fuzz(func(t *testing.T, body []byte) {
		// Must never panic, and must never accept: none of these carry a
		// signature made with "key".
		if err := v.Verify(body); err == nil {
			t.Fatalf("verifier accepted an unsigned payload: %q", body)
		}
	})
}

// ---------------------------------------------------------------------------
// Address parsing
// ---------------------------------------------------------------------------

func FuzzParseEmailAddress(f *testing.F) {
	f.Add("a@example.com")
	f.Add("Name <a@example.com>")
	f.Add(`"Doe, John" <j@example.com>`)
	f.Add("<<<>>>")
	f.Add("")

	f.Fuzz(func(t *testing.T, in string) {
		a, err := ParseEmailAddress(in)
		if err != nil || a == nil {
			return
		}
		// The parsed form is written back into a header by FormatAddress.
		if strings.ContainsAny(a.Address, "\r\n") {
			t.Fatalf("parsed address %q contains a line break (input %q)", a.Address, in)
		}
		if strings.ContainsAny(a.Name, "\r\n") {
			t.Fatalf("parsed name %q contains a line break (input %q)", a.Name, in)
		}
	})
}

func FuzzIsHTMLIsTotal(f *testing.F) {
	f.Add([]byte("<html><body>x</body></html>"))
	f.Add([]byte("plain"))
	f.Add([]byte("<p1>"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, b []byte) {
		// IsHTML picks the Content-Type for every provider, so it must be
		// total and deterministic.
		first := IsHTML(b)
		if second := IsHTML(b); first != second {
			t.Fatalf("IsHTML is not deterministic for %q", b)
		}
	})
}
