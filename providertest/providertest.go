// Package providertest is a conformance suite for gsmail Sender
// implementations.
//
// Every provider in this module has independently got the same handful of
// decisions wrong: dropping Email.Headers, retrying a permanent 4xx, sending a
// cid: attachment as a plain attachment, swallowing the provider's own error
// text. Those are not hard problems, they are problems that are easy to forget
// on the fourth provider.
//
// Run this suite against a Sender and forgetting stops being possible:
//
//	func TestConformance(t *testing.T) {
//	    providertest.Run(t, providertest.Harness{
//	        Name: "acme",
//	        NewSender: func(t *testing.T, baseURL string) gsmail.Sender { ... },
//	        Decode:    func(t *testing.T, r *http.Request, body []byte) providertest.Sent { ... },
//	    })
//	}
//
// It is exported rather than internal so that a provider living outside this
// module can hold itself to the same contract.
package providertest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
)

// Attachment is a provider-agnostic view of one attachment as it went out.
type Attachment struct {
	Filename string
	// Disposition is "attachment" or "inline". Leave empty if the provider's
	// API has no such concept.
	Disposition string
	ContentID   string
}

// Sent is a provider-agnostic view of one outbound request. A Decode
// implementation fills in whatever its API expresses.
//
// A field left empty is a failure, not a shrug. If the provider's API has no
// way to express one, say so in Harness.Unsupported; otherwise the suite reads
// an empty field as the provider having dropped the value.
type Sent struct {
	From    string
	To      []string
	Cc      []string
	Bcc     []string
	Subject string
	Text    string
	HTML    string

	// Headers holds the custom headers the provider forwarded, keyed by
	// canonical header name (as supplied to Email.SetHeader).
	Headers map[string]string

	Attachments []Attachment
}

// The field names accepted by Harness.Unsupported. Each names the Sent field
// it gates, except FieldDisposition, which names Attachment.Disposition.
//
// There is no constant for Sent.Headers: a provider that cannot carry
// arbitrary headers sets Harness.SkipHeaderChecks, which drops the three
// header cases entirely rather than leaving them half-asserted.
const (
	FieldTo          = "To"
	FieldCc          = "Cc"
	FieldBcc         = "Bcc"
	FieldSubject     = "Subject"
	FieldText        = "Text"
	FieldHTML        = "HTML"
	FieldAttachments = "Attachments"
	FieldDisposition = "Disposition"
)

var knownFields = map[string]bool{
	FieldTo:          true,
	FieldCc:          true,
	FieldBcc:         true,
	FieldSubject:     true,
	FieldText:        true,
	FieldHTML:        true,
	FieldAttachments: true,
	FieldDisposition: true,
}

// unsupported is the set of fields a provider has declared it cannot express.
type unsupported map[string]bool

// expresses reports whether the provider's view carries field, and so whether
// the checks that read it should run. The nil set expresses everything.
func (u unsupported) expresses(field string) bool { return !u[field] }

// newUnsupported validates the declared names and returns them as a set.
//
// An unrecognised name is an error rather than a silent no-op. A free-form
// string that matches nothing would disable a check exactly the way the zero
// value used to, which is the failure this list exists to remove.
func newUnsupported(fields []string) (unsupported, error) {
	u := make(unsupported, len(fields))
	for _, f := range fields {
		if !knownFields[f] {
			return nil, fmt.Errorf("unknown field %q in Harness.Unsupported; use the Field constants", f)
		}
		u[f] = true
	}
	return u, nil
}

// Harness adapts one provider to the suite.
type Harness struct {
	// Name identifies the provider in test output.
	Name string

	// NewSender builds a Sender whose API base URL points at baseURL. Any
	// retry configuration is overridden by the suite.
	NewSender func(t *testing.T, baseURL string) gsmail.Sender

	// Decode converts a captured request into the normalised view.
	Decode func(t *testing.T, r *http.Request, body []byte) Sent

	// Unsupported lists the Sent fields this provider's API cannot express,
	// using the Field constants. The checks that read them are skipped; every
	// other field must arrive.
	//
	// It lives here rather than on Sent because it describes the transport,
	// not one request. A Decode with more than one return path — SES has two,
	// one per content shape — would otherwise have to repeat the list on each,
	// and a branch that forgot it would look like a provider dropping fields.
	//
	// Declare a field only when the upstream API genuinely has no equivalent.
	// Listing one because a check is inconvenient turns the suite into a
	// formality, which is the outcome it was written to prevent.
	Unsupported []string

	// SuccessStatus is the status a happy-path response should carry.
	// Defaults to 200.
	SuccessStatus int

	// SuccessBody is the body a happy-path response should carry.
	SuccessBody string

	// SkipRetryChecks disables the HTTP status-classification cases. Set it
	// for providers whose transport retries on its own (the AWS SDK), where
	// attempt counts are not the provider's to control.
	SkipRetryChecks bool

	// SkipHeaderChecks disables the custom-header cases. Set it only if the
	// upstream API genuinely cannot carry arbitrary headers, and say so in a
	// comment where you set it.
	SkipHeaderChecks bool

	// SkipInlineAttachmentCheck disables the cid: disposition case.
	SkipInlineAttachmentCheck bool
}

func (h Harness) successStatus() int {
	if h.SuccessStatus != 0 {
		return h.SuccessStatus
	}
	return http.StatusOK
}

// fastRetries keeps the suite quick while still exercising the retry path.
var fastRetries = gsmail.RetryConfig{
	MaxRetries:      3,
	InitialInterval: time.Millisecond,
	MaxInterval:     2 * time.Millisecond,
	Multiplier:      2,
}

// server spins up a recording endpoint and a Sender pointed at it.
func (h Harness) server(t *testing.T, handler http.HandlerFunc) gsmail.Sender {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	s := h.NewSender(t, srv.URL)
	s.SetRetryConfig(fastRetries)
	return s
}

// capture runs one successful send and returns what reached the wire.
func (h Harness) capture(t *testing.T, email gsmail.Email) Sent {
	t.Helper()

	var got Sent
	var seen atomic.Bool

	s := h.server(t, func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		got = h.Decode(t, r, body)
		seen.Store(true)
		w.WriteHeader(h.successStatus())
		if h.SuccessBody != "" {
			_, _ = w.Write([]byte(h.SuccessBody))
		}
	})

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !seen.Load() {
		t.Fatal("provider never issued a request")
	}
	return got
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf
		}
	}
}

// basicEmail is the minimum viable message.
func basicEmail() gsmail.Email {
	return gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"a@example.com"},
		Subject: "conformance",
		Body:    []byte("hello"),
	}
}

// Run executes the full conformance suite against one provider.
func Run(t *testing.T, h Harness) {
	t.Helper()

	if h.NewSender == nil || h.Decode == nil {
		t.Fatal("providertest: Harness needs both NewSender and Decode")
	}
	u, err := newUnsupported(h.Unsupported)
	if err != nil {
		t.Fatalf("providertest: %v", err)
	}

	t.Run("DeliversBasicMessage", func(t *testing.T) { testBasic(t, h, u) })
	t.Run("DeliversAllRecipients", func(t *testing.T) { testRecipients(t, h, u) })
	t.Run("RoutesBodiesByField", func(t *testing.T) { testBodyRouting(t, h, u) })

	if !h.SkipHeaderChecks {
		t.Run("ForwardsCustomHeaders", func(t *testing.T) { testCustomHeaders(t, h) })
		t.Run("DropsReservedHeaders", func(t *testing.T) { testReservedHeaders(t, h, u) })
		t.Run("RejectsIllegalHeaderName", func(t *testing.T) { testIllegalHeaderName(t, h) })
	}

	if !h.SkipInlineAttachmentCheck {
		t.Run("MarksContentIDAttachmentInline", func(t *testing.T) { testInlineAttachment(t, h, u) })
	}

	if !h.SkipRetryChecks {
		t.Run("DoesNotRetryPermanentStatus", func(t *testing.T) { testNoRetryOn4xx(t, h) })
		t.Run("RetriesTransientStatus", func(t *testing.T) { testRetryOn5xx(t, h) })
		t.Run("HonoursRetryAfter", func(t *testing.T) { testRetryAfter(t, h) })
		t.Run("SurfacesProviderErrorBody", func(t *testing.T) { testErrorBody(t, h) })
		t.Run("RespectsMaxRetriesZero", func(t *testing.T) { testMaxRetriesZero(t, h) })
	}

	t.Run("RespectsContextCancellation", func(t *testing.T) { testContextCancel(t, h) })
}

func testBasic(t *testing.T, h Harness, u unsupported) {
	got := h.capture(t, basicEmail())

	if u.expresses(FieldSubject) && got.Subject != "conformance" {
		t.Errorf("Subject = %q, want %q", got.Subject, "conformance")
	}
	if u.expresses(FieldText) && got.Text != "hello" {
		t.Errorf("Text body = %q, want %q", got.Text, "hello")
	}
	if u.expresses(FieldTo) && (len(got.To) != 1 || got.To[0] != "a@example.com") {
		t.Errorf("To = %v, want [a@example.com]", got.To)
	}
}

// bccAddress is the Bcc recipient used by recipientEmail. Named so the
// fixture and the assertions cannot drift apart.
const bccAddress = "d@example.com"

// recipientEmail exercises To, Cc and Bcc together.
func recipientEmail() gsmail.Email {
	e := basicEmail()
	e.To = []string{"a@example.com", "b@example.com"}
	e.Cc = []string{"c@example.com"}
	e.Bcc = []string{bccAddress}
	return e
}

// Cc and Bcc are quietly dropped surprisingly often.
func testRecipients(t *testing.T, h Harness, u unsupported) {
	assertRecipients(t, h.capture(t, recipientEmail()), u)
}

func assertRecipients(t *testing.T, got Sent, u unsupported) {
	t.Helper()
	if u.expresses(FieldTo) && len(got.To) != 2 {
		t.Errorf("To = %v, want 2 recipients", got.To)
	}
	if u.expresses(FieldCc) && len(got.Cc) != 1 {
		t.Errorf("Cc = %v, want 1 recipient", got.Cc)
	}
	if u.expresses(FieldBcc) && len(got.Bcc) != 1 {
		t.Errorf("Bcc = %v, want 1 recipient", got.Bcc)
	}
}

// List-Unsubscribe is required of bulk senders by Gmail and Yahoo. Every
// API-backed provider in this module dropped it at some point.
func testCustomHeaders(t *testing.T, h Harness) {
	assertCustomHeaders(t, h.capture(t, customHeaderEmail()))
}

// bodyRoutingEmail pairs a plaintext body that merely contains an angle
// bracket with a genuine HTML body.
func bodyRoutingEmail() gsmail.Email {
	e := basicEmail()
	e.Body = []byte("Please review <p1> pricing before Friday.")
	e.HTMLBody = []byte("<p>Please review pricing.</p>")
	return e
}

// Body is text/plain and HTMLBody is text/html. Providers used to sniff Body
// for markup, and the heuristic misread ordinary prose: "<p1>" matched "<p",
// the message went out as text/html, and the recipient's renderer swallowed
// the pseudo-tag so the sentence lost a word.
func testBodyRouting(t *testing.T, h Harness, u unsupported) {
	got := h.capture(t, bodyRoutingEmail())
	assertBodyRouting(t, got, u)
}

func assertBodyRouting(t *testing.T, got Sent, u unsupported) {
	t.Helper()

	if u.expresses(FieldText) && !strings.Contains(got.Text, "<p1>") {
		t.Errorf("text/plain body = %q, want the Body field verbatim", got.Text)
	}
	if u.expresses(FieldHTML) && !strings.Contains(got.HTML, "<p>Please review") {
		t.Errorf("text/html body = %q, want the HTMLBody field", got.HTML)
	}
	if strings.Contains(got.HTML, "<p1>") {
		t.Errorf("the plaintext Body was sent as HTML: %q", got.HTML)
	}
}

// customHeaderEmail carries the headers a bulk sender cannot do without.
func customHeaderEmail() gsmail.Email {
	e := basicEmail()
	e.SetHeader("List-Unsubscribe", "<https://x.test/u>")
	e.SetHeader("X-Campaign", "spring")
	return e
}

func assertCustomHeaders(t *testing.T, got Sent) {
	t.Helper()
	if got.Headers == nil {
		t.Fatal("provider forwarded no custom headers; List-Unsubscribe is mandatory for bulk mail")
	}
	if got.Headers["List-Unsubscribe"] != "<https://x.test/u>" {
		t.Errorf("List-Unsubscribe = %q, want %q", got.Headers["List-Unsubscribe"], "<https://x.test/u>")
	}
	if got.Headers["X-Campaign"] != "spring" {
		t.Errorf("X-Campaign = %q, want %q", got.Headers["X-Campaign"], "spring")
	}
}

// Headers the provider generates itself must not be duplicated or overridden
// through Email.Headers.
func testReservedHeaders(t *testing.T, h Harness, u unsupported) {
	assertReservedHeaders(t, h.capture(t, reservedHeaderEmail()), u)
}

// reservedHeaderEmail tries to override headers the transport generates.
func reservedHeaderEmail() gsmail.Email {
	e := basicEmail()
	e.SetHeader("Subject", "hijacked")
	e.SetHeader("From", "attacker@evil.test")
	e.SetHeader("X-Kept", "yes")
	return e
}

func assertReservedHeaders(t *testing.T, got Sent, u unsupported) {
	t.Helper()
	for _, reserved := range []string{"Subject", "From", "To", "Cc", "Bcc"} {
		if _, leaked := got.Headers[reserved]; leaked {
			t.Errorf("reserved header %q was forwarded to the API", reserved)
		}
	}
	if got.Headers["X-Kept"] != "yes" {
		t.Error("filtering reserved headers must not drop the legitimate ones")
	}
	if u.expresses(FieldSubject) && got.Subject != "conformance" {
		t.Errorf("Subject was overridden through Headers: %q", got.Subject)
	}
}

// An illegal header name must fail before anything is sent, and must be
// permanent: retrying will not make the name legal.
func testIllegalHeaderName(t *testing.T, h Harness) {
	var calls atomic.Int32
	s := h.server(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(h.successStatus())
	})

	e := basicEmail()
	e.SetHeader("X-Bad\r\nBcc", "attacker@evil.test")

	err := s.Send(context.Background(), e)
	if err == nil {
		t.Fatal("expected an error for a header name containing CRLF")
	}
	if gsmail.IsRetryable(err) {
		t.Error("an illegal header name is permanent, not retryable")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("provider issued %d request(s) despite an invalid header", n)
	}
}

// An attachment with a Content-ID is referenced from the HTML by cid:. Sending
// it as a plain attachment leaves the image broken in the body and duplicated
// at the bottom of the message.
func testInlineAttachment(t *testing.T, h Harness, u unsupported) {
	assertInlineAttachment(t, h.capture(t, inlineAttachmentEmail()), u)
}

// inlineAttachmentEmail pairs a cid:-referenced image with a plain attachment.
func inlineAttachmentEmail() gsmail.Email {
	e := basicEmail()
	e.HTMLBody = []byte(`<img src="cid:logo">`)
	e.Attachments = []gsmail.Attachment{
		{Filename: "logo.png", ContentType: "image/png", ContentID: "logo", Data: []byte("x")},
		{Filename: "doc.pdf", ContentType: "application/pdf", Data: []byte("y")},
	}
	return e
}

func assertInlineAttachment(t *testing.T, got Sent, u unsupported) {
	t.Helper()
	if !u.expresses(FieldAttachments) {
		return
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("got %d attachments, want 2", len(got.Attachments))
	}
	if !u.expresses(FieldDisposition) {
		return
	}

	for _, att := range got.Attachments {
		switch att.Filename {
		case "logo.png":
			if att.Disposition != "inline" {
				t.Errorf("attachment with a Content-ID has disposition %q, want inline", att.Disposition)
			}
		case "doc.pdf":
			if att.Disposition != "attachment" {
				t.Errorf("plain attachment has disposition %q, want attachment", att.Disposition)
			}
		}
	}
}

func testNoRetryOn4xx(t *testing.T, h Harness) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusUnprocessableEntity,
	} {
		var calls atomic.Int32
		s := h.server(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"permanent failure"}`))
		})

		err := s.Send(context.Background(), basicEmail())
		if err == nil {
			t.Errorf("status %d: expected an error", status)
			continue
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("status %d: sent %d times, want 1 (a permanent failure must not be retried)", status, n)
		}

		var httpErr *gsmail.HTTPError
		if !errors.As(err, &httpErr) {
			t.Errorf("status %d: got %T, want a *gsmail.HTTPError so callers can classify it", status, err)
			continue
		}
		if httpErr.StatusCode != status {
			t.Errorf("status %d: HTTPError.StatusCode = %d", status, httpErr.StatusCode)
		}
	}
}

func testRetryOn5xx(t *testing.T, h Harness) {
	for _, status := range []int{
		http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		var calls atomic.Int32
		s := h.server(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
		})

		if err := s.Send(context.Background(), basicEmail()); err == nil {
			t.Errorf("status %d: expected an error", status)
		}
		if n := calls.Load(); n != int32(fastRetries.MaxRetries+1) {
			t.Errorf("status %d: sent %d times, want %d (transient failures must be retried)",
				status, n, fastRetries.MaxRetries+1)
		}
	}
}

// A 429 carrying Retry-After must wait for the interval the server asked for,
// not the client's own backoff.
func testRetryAfter(t *testing.T, h Harness) {
	var calls atomic.Int32
	s := h.server(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(h.successStatus())
		if h.SuccessBody != "" {
			_, _ = w.Write([]byte(h.SuccessBody))
		}
	})

	start := time.Now()
	if err := s.Send(context.Background(), basicEmail()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The configured backoff is 1ms; only Retry-After explains ~1s.
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("waited %v; the server's Retry-After of 1s was ignored", elapsed)
	}
}

// "status 422" tells an operator nothing. The provider's own explanation has
// to survive into the error.
func testErrorBody(t *testing.T, h Harness) {
	const detail = "recipient address is on the suppression list"

	s := h.server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"ErrorCode":406,"Message":"` + detail + `"}`))
	})

	err := s.Send(context.Background(), basicEmail())
	if err == nil {
		t.Fatal("expected an error")
	}

	var httpErr *gsmail.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("got %T, want *gsmail.HTTPError", err)
	}
	if httpErr.Body == "" {
		t.Fatal("the provider's response body was discarded; the caller cannot tell what went wrong")
	}
	if !contains(httpErr.Body, detail) {
		t.Errorf("error body %q does not carry the provider's explanation", httpErr.Body)
	}
}

// MaxRetries of 0 means "try once", not "use the default".
func testMaxRetriesZero(t *testing.T, h Harness) {
	srv := httptest.NewServer(nil)
	srv.Close() // closed immediately: every attempt fails at the transport

	var calls atomic.Int32
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer live.Close()

	s := h.NewSender(t, live.URL)
	s.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0})

	if err := s.Send(context.Background(), basicEmail()); err == nil {
		t.Fatal("expected an error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("sent %d times with MaxRetries=0, want exactly 1", n)
	}
}

// A cancelled context must stop the send promptly rather than running the full
// backoff schedule.
func testContextCancel(t *testing.T, h Harness) {
	s := h.server(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.Send(ctx, basicEmail())
	if err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want an error wrapping context.Canceled", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
