package sendgrid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gsoultan/gsmail"
)

func TestSendGridSender_Send(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if r.URL.Path != "/v3/mail/send" {
			t.Errorf("Expected path /v3/mail/send, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Expected Authorization Bearer test-key, got %s", r.Header.Get("Authorization"))
		}

		var req sendgridRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}

		if req.From.Email != "sender@example.com" {
			t.Errorf("Expected From sender@example.com, got %s", req.From.Email)
		}

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sender := NewSender("test-key")
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
