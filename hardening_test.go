package gsmail

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Content-Disposition ------------------------------------------------

// mime.FormatMediaType emits a bare token for simple names and only the
// extended form for non-ASCII ones. Older Outlook mishandles both and falls
// back to ATT00001.dat, so formatDisposition always quotes and always supplies
// an ASCII companion.
func TestFormatDispositionAlwaysQuotesAndProvidesASCIIFallback(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		filename string
		want     string
	}{
		{"simple name is quoted", "attachment", "image.png", `attachment; filename="image.png"`},
		{"token-safe name is quoted", "inline", "faktura_2024.pdf", `inline; filename="faktura_2024.pdf"`},
		{"spaces are quoted", "attachment", "my report.pdf", `attachment; filename="my report.pdf"`},
		{"quotes are escaped", "attachment", `a"b.txt`, `attachment; filename="a\"b.txt"`},
		{"backslash is escaped", "attachment", `a\b.txt`, `attachment; filename="a\\b.txt"`},
		{"non-ascii gets both forms", "attachment", "naïve.txt",
			`attachment; filename="na_ve.txt"; filename*=utf-8''na%C3%AFve.txt`},
		{"empty name yields bare kind", "attachment", "", "attachment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatDisposition(tt.kind, tt.filename); got != tt.want {
				t.Errorf("formatDisposition(%q, %q)\n got %s\nwant %s", tt.kind, tt.filename, got, tt.want)
			}
		})
	}
}

func TestAttachmentDispositionInRenderedMessage(t *testing.T) {
	raw, err := RenderMessage(Email{
		From: "a@example.com", To: []string{"b@example.com"},
		Body: []byte("hi"),
		Attachments: []Attachment{
			{Filename: "image.png", ContentType: "image/png", ContentID: "logo123", Data: []byte("x")},
			{Filename: "报告.pdf", ContentType: "application/pdf", Data: []byte("y")},
		},
	})
	if err != nil {
		t.Fatalf("RenderMessage: %v", err)
	}
	msg := string(raw)

	if !strings.Contains(msg, `Content-Disposition: inline; filename="image.png"`) {
		t.Error("inline attachment lost its quoted filename")
	}
	if !strings.Contains(msg, `Content-ID: <logo123>`) {
		t.Error("Content-ID missing")
	}
	if !strings.Contains(msg, `filename*=utf-8''%E6%8A%A5%E5%91%8A.pdf`) {
		t.Error("non-ASCII filename lost its RFC 2231 form")
	}
	if !strings.Contains(msg, `filename="__.pdf"`) {
		t.Error("non-ASCII filename lost its ASCII fallback")
	}
}

// --- MSO helpers --------------------------------------------------------

// --- Custom headers -----------------------------------------------------

func TestCustomHeadersFiltersReservedAndValidatesNames(t *testing.T) {
	got, err := CustomHeaders(map[string]string{
		"List-Unsubscribe": "<https://x.test/u>",
		"X-Campaign":       "spring",
		"Subject":          "should be dropped",
		"From":             "should be dropped",
		"content-type":     "should be dropped",
	})
	if err != nil {
		t.Fatalf("CustomHeaders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 headers to survive, got %v", got)
	}
	if got["List-Unsubscribe"] != "<https://x.test/u>" {
		t.Errorf("List-Unsubscribe mangled: %q", got["List-Unsubscribe"])
	}

	if _, err := CustomHeaders(map[string]string{"Bad Name": "v"}); err == nil {
		t.Error("expected an error for a header name containing a space")
	}
	if _, err := CustomHeaders(map[string]string{"X-Injected: evil\r\nBcc": "v"}); err == nil {
		t.Error("expected an error for a header name containing a colon and CRLF")
	}
}

func TestCustomHeadersEncodesAndSanitisesValues(t *testing.T) {
	got, err := CustomHeaders(map[string]string{
		"X-Note":    "line\r\nBcc: attacker@evil.test",
		"X-Unicode": "naïve",
	})
	if err != nil {
		t.Fatalf("CustomHeaders: %v", err)
	}
	if strings.ContainsAny(got["X-Note"], "\r\n") {
		t.Errorf("CRLF survived sanitisation: %q", got["X-Note"])
	}
	if !strings.HasPrefix(got["X-Unicode"], "=?UTF-8?") {
		t.Errorf("non-ASCII value was not RFC 2047 encoded: %q", got["X-Unicode"])
	}
}

func TestBuildMessageEmitsCustomHeaders(t *testing.T) {
	e := Email{From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi")}
	e.SetHeader("List-Unsubscribe", "<https://x.test/u>")

	raw, err := RenderMessage(e)
	if err != nil {
		t.Fatalf("RenderMessage: %v", err)
	}
	if !strings.Contains(string(raw), "List-Unsubscribe: <https://x.test/u>") {
		t.Errorf("List-Unsubscribe missing from rendered message:\n%s", raw)
	}
}

// --- Remote template size ----------------------------------------------

func TestSetBodyFromURLRejectsOversizedTemplate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stream more than the cap without ever finishing.
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < (MaxTemplateSize>>20)+2; i++ {
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer srv.Close()

	var e Email
	err := e.SetBodyFromURL(t.Context(), srv.URL, nil)
	if !errors.Is(err, ErrTemplateTooLarge) {
		t.Fatalf("got %v, want ErrTemplateTooLarge", err)
	}
	if IsRetryable(err) {
		t.Error("an oversized template is permanent; retrying will not shrink it")
	}
}

// --- HTTPError classification ------------------------------------------

func TestHTTPErrorRetryClassification(t *testing.T) {
	tests := []struct {
		status int
		retry  bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusUnprocessableEntity, false},
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		e := &HTTPError{Provider: "test", StatusCode: tt.status}
		if got := IsRetryable(e); got != tt.retry {
			t.Errorf("status %d: IsRetryable = %v, want %v", tt.status, got, tt.retry)
		}
	}
}

func TestCheckLimitRejectsNonPositive(t *testing.T) {
	for _, limit := range []int{0, -1, -100} {
		err := CheckLimit(limit)
		if !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("CheckLimit(%d) = %v, want ErrInvalidLimit", limit, err)
		}
		if IsRetryable(err) {
			t.Errorf("CheckLimit(%d) should be permanent", limit)
		}
	}
	if err := CheckLimit(1); err != nil {
		t.Errorf("CheckLimit(1) = %v, want nil", err)
	}
}
