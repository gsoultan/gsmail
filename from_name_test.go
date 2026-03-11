package gsmail_test

import (
	"bytes"
	"net/mail"
	"testing"

	"github.com/gsoultan/gsmail"
)

func TestFromNameInBuildMessage(t *testing.T) {
	tests := []struct {
		name     string
		from     string
		expected string
	}{
		{
			name:     "Standard email",
			from:     "sender@example.com",
			expected: "sender@example.com",
		},
		{
			name:     "Email with name",
			from:     "John Doe <sender@example.com>",
			expected: "\"John Doe\" <sender@example.com>",
		},
		{
			name:     "Email with special characters in name",
			from:     "Doe, John <sender@example.com>",
			expected: "\"Doe, John\" <sender@example.com>",
		},
		{
			name:     "Already quoted name",
			from:     "\"Doe, John\" <sender@example.com>",
			expected: "\"Doe, John\" <sender@example.com>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			email := gsmail.Email{
				From:    tt.from,
				To:      []string{"receiver@example.com"},
				Subject: "Test Subject",
				Body:    []byte("Test Body"),
			}

			bufPtr := gsmail.GetBuffer()
			defer gsmail.PutBuffer(bufPtr)

			gsmail.BuildMessage(bufPtr, email)
			message := string(*bufPtr)

			msg, err := mail.ReadMessage(bytes.NewReader(*bufPtr))
			if err != nil {
				t.Fatalf("Failed to read message: %v", err)
			}

			from := msg.Header.Get("From")
			if from != tt.expected {
				t.Errorf("Expected From header %q, got %q\nMessage:\n%s", tt.expected, from, message)
			}
		})
	}
}
