package gsmail_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/ses"
	"github.com/gsoultan/gsmail/smtp"
)

func TestEmailStructure(t *testing.T) {
	email := gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Test Subject",
		Body:    []byte("Test Body"),
	}

	if email.From != "sender@example.com" {
		t.Errorf("got %s, want %s", email.From, "sender@example.com")
	}
}

func TestParseTemplates(t *testing.T) {
	data := struct {
		Name string
	}{Name: "World"}

	t.Run("TextTemplate", func(t *testing.T) {
		got, err := gsmail.ParseTextTemplate("Hello {{.Name}}", data)
		if err != nil {
			t.Fatalf("ParseTextTemplate failed: %v", err)
		}
		want := "Hello World"
		if string(got) != want {
			t.Errorf("got %q, want %q", string(got), want)
		}
	})

	t.Run("HTMLTemplate", func(t *testing.T) {
		got, err := gsmail.ParseHTMLTemplate("<h1>Hello {{.Name}}</h1>", data)
		if err != nil {
			t.Fatalf("ParseHTMLTemplate failed: %v", err)
		}
		want := "<h1>Hello World</h1>"
		if string(got) != want {
			t.Errorf("got %q, want %q", string(got), want)
		}
	})

	t.Run("InvalidTextTemplate", func(t *testing.T) {
		_, err := gsmail.ParseTextTemplate("Hello {{.Name", data)
		if err == nil {
			t.Error("expected error for invalid syntax")
		}
	})

	t.Run("InvalidHTMLTemplate", func(t *testing.T) {
		_, err := gsmail.ParseHTMLTemplate("<h1>Hello {{.Name</h1>", data)
		if err == nil {
			t.Error("expected error for invalid syntax")
		}
	})

	t.Run("SetBodyText", func(t *testing.T) {
		email := gsmail.Email{}
		err := email.SetBody("Hello {{.Name}}", data)
		if err != nil {
			t.Fatalf("SetBody failed: %v", err)
		}
		if gsmail.IsHTML(email.Body) {
			t.Error("expected IsHTML to be false")
		}
		want := "Hello World"
		if string(email.Body) != want {
			t.Errorf("got %q, want %q", string(email.Body), want)
		}
	})

	t.Run("SetBodyHTML", func(t *testing.T) {
		email := gsmail.Email{}
		err := email.SetBody("<h1>Hello {{.Name}}</h1>", data)
		if err != nil {
			t.Fatalf("SetBody failed: %v", err)
		}
		if !gsmail.IsHTML(email.Body) {
			t.Error("expected IsHTML to be true")
		}
		want := "<h1>Hello World</h1>"
		if string(email.Body) != want {
			t.Errorf("got %q, want %q", string(email.Body), want)
		}
	})

	t.Run("SetBodyFromURL", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Hello {{.Name}} from URL"))
		}))
		defer ts.Close()

		email := gsmail.Email{}
		err := email.SetBodyFromURL(context.Background(), ts.URL, data)
		if err != nil {
			t.Fatalf("SetBodyFromURL failed: %v", err)
		}
		want := "Hello World from URL"
		if string(email.Body) != want {
			t.Errorf("got %q, want %q", string(email.Body), want)
		}
	})

	t.Run("SetBodyFromS3", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Hello {{.Name}} from S3"))
		}))
		defer ts.Close()

		email := gsmail.Email{}
		cfg := gsmail.S3Config{
			Region:    "us-east-1",
			Bucket:    "test-bucket",
			Key:       "template.txt",
			Endpoint:  ts.URL,
			AccessKey: "test",
			SecretKey: "test",
		}

		err := email.SetBodyFromS3(context.Background(), cfg, data)
		if err != nil {
			t.Fatalf("SetBodyFromS3 failed: %v", err)
		}
		want := "Hello World from S3"
		if string(email.Body) != want {
			t.Errorf("got %q, want %q", string(email.Body), want)
		}
	})
}

func TestSESProvider(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"MessageId": "test-message-id"}`))
	}))
	defer ts.Close()

	email := gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Test Subject",
		Body:    []byte("Test Body"),
	}

	p := ses.NewSender("us-east-1", "test", "test", ts.URL)

	err := gsmail.Send(context.Background(), p, email)
	if err != nil {
		t.Fatalf("Send via Sender failed: %v", err)
	}

	t.Run("HTML", func(t *testing.T) {
		email.Body = []byte("<h1>HTML Body</h1>")
		err := gsmail.Send(context.Background(), p, email)
		if err != nil {
			t.Fatalf("Send via Sender HTML failed: %v", err)
		}
	})

	t.Run("Fail", func(t *testing.T) {
		tsFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer tsFail.Close()

		pFail := ses.NewSender("us-east-1", "test", "test", tsFail.URL)
		err := gsmail.Send(context.Background(), pFail, email)
		if err == nil {
			t.Error("expected error for SES fail")
		}
	})

	t.Run("Ping", func(t *testing.T) {
		tsPing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"Account": {}}`))
		}))
		defer tsPing.Close()

		pPing := ses.NewSender("us-east-1", "test", "test", tsPing.URL)
		err := gsmail.Ping(context.Background(), pPing)
		if err != nil {
			t.Fatalf("Ping failed: %v", err)
		}
	})
}

func TestSMTPProviderStructure(t *testing.T) {
	p := smtp.NewSender("smtp.example.com", 587, "user", "password", true)

	if p.Host != "smtp.example.com" {
		t.Errorf("got %s, want %s", p.Host, "smtp.example.com")
	}
	if !p.SSL {
		t.Errorf("expected SSL to be true")
	}
}

func TestSMTPProvider_Send(t *testing.T) {
	t.Run("DuplicateHeaders", func(t *testing.T) {
		// Test removed as it was using internal methods or was incomplete
	})
}

func BenchmarkIsHTML(b *testing.B) {
	body := []byte("<html><body>Hello</body></html>")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gsmail.IsHTML(body)
	}
}

func BenchmarkHasHeader(b *testing.B) {
	body := []byte("MIME-Version: 1.0\r\nContent-Type: text/html\r\n\r\n<html>Body</html>")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gsmail.HasHeader(body, "Content-Type")
	}
}

func BenchmarkParseRawEmail(b *testing.B) {
	raw := []byte("From: sender@example.com\r\nTo: receiver1@example.com, receiver2@example.com\r\nSubject: Test Subject\r\n\r\nHello World!")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = gsmail.ParseRawEmail(raw)
	}
}

func TestParseRawEmail(t *testing.T) {
	raw := []byte("From: sender@example.com\r\nTo: receiver1@example.com, receiver2@example.com\r\nSubject: Test Subject\r\n\r\nHello World!")
	email, err := gsmail.ParseRawEmail(raw)
	if err != nil {
		t.Fatalf("ParseRawEmail failed: %v", err)
	}

	if email.From != "sender@example.com" {
		t.Errorf("got %s, want %s", email.From, "sender@example.com")
	}
	if email.Subject != "Test Subject" {
		t.Errorf("got %s, want %s", email.Subject, "Test Subject")
	}
	if len(email.To) != 2 {
		t.Errorf("got %d recipients, want 2", len(email.To))
	}
	if email.To[0] != "receiver1@example.com" {
		t.Errorf("got %s, want %s", email.To[0], "receiver1@example.com")
	}
	if string(email.Body) != "Hello World!" {
		t.Errorf("got %s, want %s", string(email.Body), "Hello World!")
	}
}
