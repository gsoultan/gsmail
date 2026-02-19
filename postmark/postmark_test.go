package postmark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gsoultan/gsmail"
)

func TestPostmarkSender_Send(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if r.Header.Get("X-Postmark-Server-Token") != "test-token" {
			t.Errorf("Expected X-Postmark-Server-Token test-token, got %s", r.Header.Get("X-Postmark-Server-Token"))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"To": "receiver@example.com", "SubmittedAt": "2021-01-01", "MessageID": "123", "ErrorCode": 0, "Message": "OK"}`))
	}))
	defer server.Close()

	sender := NewSender("test-token")
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
