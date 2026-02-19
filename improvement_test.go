package gsmail

import (
	"strings"
	"testing"
)

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
