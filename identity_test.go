package gsmail

import (
	"strings"
	"testing"
)

const identityRaw = "Message-ID: <abc123@example.com>\r\n" +
	"From: sender@example.com\r\n" +
	"To: rcpt@example.com\r\n" +
	"Subject: Quarterly report\r\n" +
	"Date: Mon, 02 Jan 2006 15:04:05 -0700\r\n" +
	"In-Reply-To: <parent@example.com>\r\n" +
	"References: <root@example.com> <parent@example.com>\r\n" +
	"List-Unsubscribe: <https://example.com/u>\r\n" +
	"Received: from mx.example.com by mail.example.net\r\n" +
	"DKIM-Signature: v=1; a=rsa-sha256; d=example.com; s=s1; b=AAAA\r\n" +
	"Authentication-Results: mx.example.net; dkim=pass\r\n" +
	"X-Campaign: spring\r\n" +
	"\r\n" +
	"body text\r\n"

// Nothing was retained before, so anything needing a Message-ID had to
// re-parse the raw bytes it had just handed over.
func TestParseRawEmailRetainsHeaders(t *testing.T) {
	email, err := ParseRawEmail([]byte(identityRaw))
	if err != nil {
		t.Fatalf("ParseRawEmail: %v", err)
	}

	for name, want := range map[string]string{
		"Message-ID":       "<abc123@example.com>",
		"In-Reply-To":      "<parent@example.com>",
		"References":       "<root@example.com> <parent@example.com>",
		"List-Unsubscribe": "<https://example.com/u>",
		"X-Campaign":       "spring",
		"Date":             "Mon, 02 Jan 2006 15:04:05 -0700",
	} {
		if got := email.Header(name); got != want {
			t.Errorf("Header(%q) = %q, want %q", name, got, want)
		}
	}

	// Fields the struct already models must not be duplicated into the map.
	for _, modelled := range []string{"From", "To", "Subject", "Content-Type"} {
		if got := email.Header(modelled); got != "" {
			t.Errorf("Header(%q) = %q; it is already a typed field", modelled, got)
		}
	}
}

// net/mail canonicalises "Message-ID" to "Message-Id" and "DKIM-Signature" to
// "Dkim-Signature", so indexing the map with the spelling people expect finds
// nothing. Header exists so nobody has to know that.
func TestHeaderLookupIgnoresCase(t *testing.T) {
	email, err := ParseRawEmail([]byte(identityRaw))
	if err != nil {
		t.Fatal(err)
	}

	if _, direct := email.Headers["Message-ID"]; direct {
		t.Log("map happens to hold the expected spelling; the accessor still must work")
	}
	for _, spelling := range []string{"Message-ID", "message-id", "MESSAGE-ID", "Message-Id"} {
		if got := email.Header(spelling); got != "<abc123@example.com>" {
			t.Errorf("Header(%q) = %q", spelling, got)
		}
	}
	if got := email.Header("dkim-signature"); got == "" {
		t.Error("DKIM-Signature should be readable despite canonicalisation")
	}
}

func TestMessageIDStripsBrackets(t *testing.T) {
	email, err := ParseRawEmail([]byte(identityRaw))
	if err != nil {
		t.Fatal(err)
	}
	if got := email.MessageID(); got != "abc123@example.com" {
		t.Errorf("MessageID() = %q, want the value without angle brackets", got)
	}

	var none Email
	if got := none.MessageID(); got != "" {
		t.Errorf("a message with no Message-ID returned %q", got)
	}
}

func TestMessageIdentityPrefersMessageID(t *testing.T) {
	email, err := ParseRawEmail([]byte(identityRaw))
	if err != nil {
		t.Fatal(err)
	}
	// A UID must not win over a Message-ID: the Message-ID identifies the
	// message anywhere, the UID only inside one mailbox.
	email.UID = 42
	email.Mailbox = "Archive"

	id, source := email.MessageIdentity()
	if source != IdentityMessageID {
		t.Fatalf("source = %q, want %q", source, IdentityMessageID)
	}
	if id != "abc123@example.com" {
		t.Errorf("id = %q", id)
	}
}

func TestMessageIdentityFallsBackToUID(t *testing.T) {
	email := Email{UID: 42, Mailbox: "Archive", Subject: "x"}

	id, source := email.MessageIdentity()
	if source != IdentityUID {
		t.Fatalf("source = %q, want %q", source, IdentityUID)
	}
	if id != "Archive/42" {
		t.Errorf("id = %q, want the mailbox and UID", id)
	}

	// The same UID in another mailbox is a different message.
	other := Email{UID: 42, Mailbox: "INBOX", Subject: "x"}
	otherID, _ := other.MessageIdentity()
	if otherID == id {
		t.Error("the same UID in two mailboxes produced one identity")
	}

	// An unset mailbox defaults to INBOX rather than producing a bare number.
	noMailbox := Email{UID: 42, Subject: "x"}
	if got, _ := noMailbox.MessageIdentity(); got != "INBOX/42" {
		t.Errorf("id = %q, want INBOX/42", got)
	}
}

func TestMessageIdentityFallsBackToContent(t *testing.T) {
	email := Email{
		From:    "a@example.com",
		To:      []string{"b@example.com"},
		Subject: "no identifiers here",
		Body:    []byte("body"),
	}

	id, source := email.MessageIdentity()
	if source != IdentityContent {
		t.Fatalf("source = %q, want %q", source, IdentityContent)
	}
	if len(id) != 64 {
		t.Errorf("id = %q, want a hex sha256", id)
	}

	// Stable across calls, and for an equal message.
	again, _ := email.MessageIdentity()
	if again != id {
		t.Error("the content digest is not stable across calls")
	}
	copyOf := email
	copyOf.Body = []byte("body")
	if other, _ := copyOf.MessageIdentity(); other != id {
		t.Error("an identical message produced a different digest")
	}
}

// Every field is length-prefixed, so moving bytes across a field boundary must
// change the digest. Without that, subject "ab" + body "c" would hash the same
// as subject "a" + body "bc".
func TestContentDigestIsFieldSensitive(t *testing.T) {
	a := Email{From: "x@example.com", Subject: "ab", Body: []byte("c")}
	b := Email{From: "x@example.com", Subject: "a", Body: []byte("bc")}

	idA, _ := a.MessageIdentity()
	idB, _ := b.MessageIdentity()
	if idA == idB {
		t.Error("field boundaries are not part of the digest")
	}
}

func TestContentDigestNoticesEveryPart(t *testing.T) {
	base := Email{
		From: "a@example.com", To: []string{"b@example.com"},
		Subject: "s", Body: []byte("body"), HTMLBody: []byte("<p>body</p>"),
		Attachments: []Attachment{{Filename: "a.txt", Data: []byte("data")}},
	}
	baseID, _ := base.MessageIdentity()

	mutations := map[string]func(*Email){
		"From":        func(e *Email) { e.From = "z@example.com" },
		"Subject":     func(e *Email) { e.Subject = "different" },
		"To":          func(e *Email) { e.To = []string{"z@example.com"} },
		"Body":        func(e *Email) { e.Body = []byte("changed") },
		"HTMLBody":    func(e *Email) { e.HTMLBody = []byte("<p>changed</p>") },
		"attachment":  func(e *Email) { e.Attachments[0].Data = []byte("changed") },
		"filename":    func(e *Email) { e.Attachments[0].Filename = "b.txt" },
		"Date header": func(e *Email) { e.SetHeader("Date", "Tue, 03 Jan 2006 00:00:00 -0700") },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.To = append([]string(nil), base.To...)
			changed.Attachments = []Attachment{{Filename: "a.txt", Data: []byte("data")}}
			changed.Headers = nil
			mutate(&changed)

			if got, _ := changed.MessageIdentity(); got == baseID {
				t.Errorf("changing %s did not change the digest", name)
			}
		})
	}
}

func TestMessageIdentityOfEmptyEmail(t *testing.T) {
	var e Email
	id, source := e.MessageIdentity()
	if source != IdentityNone || id != "" {
		t.Errorf("got (%q, %q), want an empty identity", id, source)
	}
}

// Trace headers describe how a message travelled. Keeping them lets an inbound
// message be inspected; re-emitting them on a new message would forge its
// provenance.
func TestTraceHeadersAreRetainedButNeverRendered(t *testing.T) {
	email, err := ParseRawEmail([]byte(identityRaw))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"Received", "DKIM-Signature", "Authentication-Results"} {
		if email.Header(name) == "" {
			t.Errorf("%s should be retained for inspection", name)
		}
	}

	raw, err := RenderMessage(email)
	if err != nil {
		t.Fatalf("RenderMessage: %v", err)
	}
	headerBlock, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for _, name := range []string{"Received:", "DKIM-Signature:", "Authentication-Results:"} {
		if strings.Contains(headerBlock, name) {
			t.Errorf("%s was written onto a new message:\n%s", name, headerBlock)
		}
	}

	// Headers that legitimately survive a reply must still be there.
	for _, name := range []string{"In-Reply-To:", "References:", "X-Campaign:"} {
		if !strings.Contains(headerBlock, name) {
			t.Errorf("%s should survive rendering:\n%s", name, headerBlock)
		}
	}
}

// The same rule has to hold for the providers that build a message through a
// vendor API rather than through BuildMessage.
func TestCustomHeadersDropsTraceHeaders(t *testing.T) {
	got, err := CustomHeaders(map[string]string{
		"Received":       "from mx.example.com",
		"DKIM-Signature": "v=1; b=AAAA",
		"X-Campaign":     "spring",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := got["Received"]; leaked {
		t.Error("Received reached a provider API")
	}
	if _, leaked := got["DKIM-Signature"]; leaked {
		t.Error("DKIM-Signature reached a provider API")
	}
	if got["X-Campaign"] != "spring" {
		t.Error("a legitimate header was dropped alongside the trace headers")
	}
}

// A re-rendered message must not inherit the original's Message-ID: it is a
// new message and needs its own.
func TestRenderedReplyGetsANewMessageID(t *testing.T) {
	email, err := ParseRawEmail([]byte(identityRaw))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := RenderMessage(email)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "abc123@example.com") {
		t.Error("the rendered message reused the original Message-ID")
	}
	if strings.Count(string(raw), "Message-ID:") != 1 {
		t.Errorf("expected exactly one Message-ID, got %d",
			strings.Count(string(raw), "Message-ID:"))
	}
}

// A hostile message must not make the header map the expensive part of holding
// a message.
func TestParsedHeadersAreBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@example.com\r\n")
	for i := 0; i < maxParsedHeaders*3; i++ {
		b.WriteString("X-Filler-")
		b.WriteString(strings.Repeat("a", 3))
		b.WriteString(strconvItoa(i))
		b.WriteString(": v\r\n")
	}
	b.WriteString("\r\nbody")

	email, err := ParseRawEmail([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(email.Headers) > maxParsedHeaders {
		t.Errorf("retained %d headers, cap is %d", len(email.Headers), maxParsedHeaders)
	}
}

func TestOversizedHeaderValueIsDropped(t *testing.T) {
	raw := "From: a@example.com\r\nX-Huge: " + strings.Repeat("a", maxHeaderValueLen+1) +
		"\r\nX-Small: ok\r\n\r\nbody"

	email, err := ParseRawEmail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if email.Header("X-Huge") != "" {
		t.Error("an oversized header value should be dropped rather than truncated")
	}
	if email.Header("X-Small") != "ok" {
		t.Error("dropping the oversized value removed the others too")
	}
}

func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
