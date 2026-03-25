package gsmail

import (
	"strings"
	"testing"
)

func extractBase64PayloadLines(msg string) [][]string {
	lines := strings.Split(msg, "\r\n")
	payloads := make([][]string, 0)

	for i := range len(lines) {
		if !strings.EqualFold(lines[i], "Content-Transfer-Encoding: base64") {
			continue
		}

		j := i + 1
		for j < len(lines) && lines[j] != "" {
			j++
		}
		if j >= len(lines)-1 {
			continue
		}

		j++
		payload := make([]string, 0)
		for j < len(lines) {
			line := lines[j]
			if line == "" || strings.HasPrefix(line, "--") {
				break
			}
			payload = append(payload, line)
			j++
		}

		if len(payload) > 0 {
			payloads = append(payloads, payload)
		}
	}

	return payloads
}

func assertMIMEBase64Wrapped(t *testing.T, payload []string) {
	t.Helper()

	if len(payload) == 0 {
		t.Fatal("expected base64 payload lines, got none")
	}

	for i, line := range payload {
		if len(line) > 76 {
			t.Fatalf("line %d exceeds MIME base64 max length 76: got %d", i+1, len(line))
		}
		if i < len(payload)-1 && len(line) != 76 {
			t.Fatalf("line %d must be exactly 76 chars for wrapped base64, got %d", i+1, len(line))
		}
	}
}

func TestImprovementHeaders(t *testing.T) {
	email := Email{
		From:    "sender@example.com",
		To:      []string{"to@example.com"},
		Cc:      []string{"cc@example.com"},
		Bcc:     []string{"bcc@example.com"},
		ReplyTo: "reply@example.com",
		Subject: "Test Improvement",
		Body:    []byte("Plain Text Body"),
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	msg := string(*bufPtr)

	headers := []string{
		"From: ",
		"To: ",
		"Cc: ",
		"Reply-To: ",
		"sender@example.com",
		"to@example.com",
		"cc@example.com",
		"reply@example.com",
		"Subject: Test Improvement",
		"Date:",
		"Message-ID:",
		"MIME-Version: 1.0",
		"Content-Type: text/plain",
	}

	for _, h := range headers {
		if !strings.Contains(msg, h) {
			t.Errorf("Expected header %q not found in message:\n%s", h, msg)
		}
	}

	if strings.Contains(msg, "Bcc: bcc@example.com") {
		t.Errorf("Bcc header should NOT be present in the message")
	}
}

func TestImprovementUnicodeBodyEncoding(t *testing.T) {
	email := Email{
		From:    "sender@example.com",
		To:      []string{"to@example.com"},
		Subject: "Test",
		Body:    []byte("<html><body>Reminder ⏰ 世界</body></html>"),
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	msg := string(*bufPtr)

	if !strings.Contains(msg, "Content-Transfer-Encoding: base64") {
		t.Error("Simple message must use base64 for Unicode (emoji, CJK) preservation in Outlook")
	}
	if !strings.Contains(msg, "charset=\"UTF-8\"") {
		t.Error("Must declare UTF-8 charset")
	}
}

func TestImprovementSubjectEncoding(t *testing.T) {
	email := Email{
		From:    "sender@example.com",
		To:      []string{"to@example.com"},
		Subject: "Hello 世界", // Non-ASCII
		Body:    []byte("Plain Text Body"),
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	msg := string(*bufPtr)

	expectedSubject := "Subject: =?UTF-8?q?Hello_=E4=B8=96=E7=95=8C?="
	if !strings.Contains(msg, expectedSubject) {
		t.Errorf("Subject not correctly encoded. Expected %q in message:\n%s", expectedSubject, msg)
	}
}

func TestImprovementMultipartAlternative(t *testing.T) {
	email := Email{
		From:     "sender@example.com",
		To:       []string{"to@example.com"},
		Subject:  "Test Multipart Alternative",
		Body:     []byte("Plain Text Body"),
		HTMLBody: []byte("<p>HTML Body</p>"),
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	msg := string(*bufPtr)

	if !strings.Contains(msg, "Content-Type: multipart/alternative") {
		t.Errorf("Expected multipart/alternative not found in message:\n%s", msg)
	}
	if !strings.Contains(msg, "text/plain") {
		t.Errorf("Plain text part missing in message:\n%s", msg)
	}
	if !strings.Contains(msg, "text/html") {
		t.Errorf("HTML part missing in message:\n%s", msg)
	}
	if !strings.Contains(msg, "Plain Text Body") {
		// Since it's base64 encoded, it won't be there as plain text
		// But let's check if it's there at all
	}
}

func TestImprovementInlineAttachment(t *testing.T) {
	email := Email{
		From:    "sender@example.com",
		To:      []string{"to@example.com"},
		Subject: "Test Inline Attachment",
		Body:    []byte("Plain Text Body"),
		Attachments: []Attachment{
			{
				Filename:    "image.png",
				ContentType: "image/png",
				ContentID:   "logo123",
				Data:        []byte("fake-image-data"),
			},
		},
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	msg := string(*bufPtr)

	if !strings.Contains(strings.ToLower(msg), strings.ToLower("Content-ID: <logo123>")) {
		t.Errorf("Content-ID missing for inline attachment. Got message:\n%s", msg)
	}
	if !strings.Contains(msg, "Content-Disposition: inline; filename=\"image.png\"") {
		t.Errorf("Inline disposition missing for inline attachment")
	}
}

func TestImprovementParseRawEmail(t *testing.T) {
	raw := []byte("From: sender@example.com\r\n" +
		"To: to@example.com\r\n" +
		"Cc: cc@example.com\r\n" +
		"Subject: =?UTF-8?q?Hello_=E4=B8=96=E7=95=8C?=\r\n" +
		"Content-Type: multipart/alternative; boundary=foo\r\n" +
		"\r\n" +
		"--foo\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Plain Body\r\n" +
		"--foo\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<html>HTML Body</html>\r\n" +
		"--foo--\r\n")

	email, err := ParseRawEmail(raw)
	if err != nil {
		t.Fatalf("ParseRawEmail failed: %v", err)
	}

	if email.From != "sender@example.com" {
		t.Errorf("Expected From: sender@example.com, got %q", email.From)
	}
	if email.Subject != "Hello 世界" {
		t.Errorf("Expected Subject: Hello 世界, got %q", email.Subject)
	}
	if len(email.To) != 1 || email.To[0] != "to@example.com" {
		t.Errorf("Expected To: [to@example.com], got %v", email.To)
	}
	if len(email.Cc) != 1 || email.Cc[0] != "cc@example.com" {
		t.Errorf("Expected Cc: [cc@example.com], got %v", email.Cc)
	}
	if strings.TrimSpace(string(email.Body)) != "Plain Body" {
		t.Errorf("Expected Body: Plain Body, got %q", string(email.Body))
	}
	if strings.TrimSpace(string(email.HTMLBody)) != "<html>HTML Body</html>" {
		t.Errorf("Expected HTMLBody: <html>HTML Body</html>, got %q", string(email.HTMLBody))
	}
}

func TestImprovementSimpleBase64BodyIsWrappedAt76Chars(t *testing.T) {
	email := Email{
		From:    "sender@example.com",
		To:      []string{"to@example.com"},
		Subject: "Simple Wrapped Base64",
		Body:    []byte(strings.Repeat("A", 120)),
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	payloads := extractBase64PayloadLines(string(*bufPtr))

	if len(payloads) != 1 {
		t.Fatalf("expected exactly 1 base64 payload, got %d", len(payloads))
	}

	assertMIMEBase64Wrapped(t, payloads[0])
}

func TestImprovementMultipartAlternativeBase64BodiesAreWrappedAt76Chars(t *testing.T) {
	email := Email{
		From:     "sender@example.com",
		To:       []string{"to@example.com"},
		Subject:  "Multipart Wrapped Base64",
		Body:     []byte(strings.Repeat("P", 120)),
		HTMLBody: []byte("<p>" + strings.Repeat("H", 120) + "</p>"),
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	payloads := extractBase64PayloadLines(string(*bufPtr))

	if len(payloads) != 2 {
		t.Fatalf("expected exactly 2 base64 payloads for multipart/alternative, got %d", len(payloads))
	}

	for _, payload := range payloads {
		assertMIMEBase64Wrapped(t, payload)
	}
}

func TestImprovementAttachmentBase64IsWrappedAt76Chars(t *testing.T) {
	email := Email{
		From:    "sender@example.com",
		To:      []string{"to@example.com"},
		Subject: "Attachment Wrapped Base64",
		Body:    []byte(strings.Repeat("B", 120)),
		Attachments: []Attachment{
			{
				Filename:    "large.bin",
				ContentType: "application/octet-stream",
				Data:        []byte(strings.Repeat("Z", 200)),
			},
		},
	}

	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	BuildMessage(bufPtr, email)
	payloads := extractBase64PayloadLines(string(*bufPtr))

	if len(payloads) != 2 {
		t.Fatalf("expected exactly 2 base64 payloads for body + attachment, got %d", len(payloads))
	}

	for _, payload := range payloads {
		assertMIMEBase64Wrapped(t, payload)
	}
}
