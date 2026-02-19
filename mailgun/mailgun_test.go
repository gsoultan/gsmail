package mailgun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gsoultan/gsmail"
)

func TestMailgunSender_Send(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		// Basic auth "api:test-key"
		user, pass, _ := r.BasicAuth()
		if user != "api" || pass != "test-key" {
			t.Errorf("Expected basic auth api:test-key, got %s:%s", user, pass)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message": "Queued. Thank you."}`))
	}))
	defer server.Close()

	sender := NewSender("example.com", "test-key")
	sender.BaseURL = server.URL
	sender.Client = server.Client()

	email := gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Test",
		Body:    []byte("Hello"),
	}

	err := sender.Send(context.Background(), email)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
}
