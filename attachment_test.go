package gsmail_test

import (
	"bytes"
	"testing"

	"github.com/gsoultan/gsmail"
)

func TestAttachmentSendingAndReceiving(t *testing.T) {
	email := gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Test with Attachments",
		Body:    []byte("This is the body."),
		Attachments: []gsmail.Attachment{
			{
				Filename:    "test.txt",
				ContentType: "text/plain",
				Data:        []byte("Hello attachment 1"),
			},
			{
				Filename:    "image.png",
				ContentType: "image/png",
				Data:        []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR..."),
			},
		},
	}

	// 1. Test Building the message (SMTP/SES raw)
	bufPtr := gsmail.GetBuffer()
	defer gsmail.PutBuffer(bufPtr)

	gsmail.BuildMessage(bufPtr, email)

	raw := *bufPtr
	if len(raw) == 0 {
		t.Fatal("built message is empty")
	}

	// 2. Test Parsing the message (IMAP/POP3)
	parsed, err := gsmail.ParseRawEmail(raw)
	if err != nil {
		t.Fatalf("failed to parse raw email: %v", err)
	}

	if parsed.Subject != email.Subject {
		t.Errorf("got subject %q, want %q", parsed.Subject, email.Subject)
	}

	if !bytes.Contains(parsed.Body, email.Body) {
		t.Errorf("parsed body does not contain expected text. got: %s", string(parsed.Body))
	}

	if len(parsed.Attachments) != 2 {
		t.Fatalf("got %d attachments, want 2", len(parsed.Attachments))
	}

	if parsed.Attachments[0].Filename != "test.txt" {
		t.Errorf("got filename %q, want %q", parsed.Attachments[0].Filename, "test.txt")
	}

	if string(parsed.Attachments[0].Data) != "Hello attachment 1" {
		t.Errorf("got attachment data %q, want %q", string(parsed.Attachments[0].Data), "Hello attachment 1")
	}

	if parsed.Attachments[1].Filename != "image.png" {
		t.Errorf("got filename %q, want %q", parsed.Attachments[1].Filename, "image.png")
	}
}

func TestParseRawEmailSimple(t *testing.T) {
	raw := []byte("From: test@example.com\r\nTo: dest@example.com\r\nSubject: Simple\r\n\r\nHello Body")
	email, err := gsmail.ParseRawEmail(raw)
	if err != nil {
		t.Fatalf("failed to parse simple email: %v", err)
	}

	if email.From != "test@example.com" {
		t.Errorf("got %s, want %s", email.From, "test@example.com")
	}

	if string(email.Body) != "Hello Body" {
		t.Errorf("got %s, want %s", string(email.Body), "Hello Body")
	}
}

func BenchmarkParseRawEmailMultipart(b *testing.B) {
	email := gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Bench",
		Body:    []byte("This is the body."),
		Attachments: []gsmail.Attachment{
			{Filename: "a.txt", Data: []byte("Some data")},
			{Filename: "b.txt", Data: []byte("More data")},
		},
	}
	bufPtr := gsmail.GetBuffer()
	gsmail.BuildMessage(bufPtr, email)
	raw := *bufPtr

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gsmail.ParseRawEmail(raw)
	}
}
