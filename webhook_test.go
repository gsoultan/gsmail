package gsmail

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// --- Mailgun ------------------------------------------------------------

func mailgunBody(t *testing.T, key, timestamp, token string) []byte {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(timestamp))
	mac.Write([]byte(token))
	body, err := json.Marshal(map[string]any{
		"signature": map[string]string{
			"timestamp": timestamp,
			"token":     token,
			"signature": hex.EncodeToString(mac.Sum(nil)),
		},
		"event-data": map[string]any{"event": "failed", "recipient": "a@b.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestMailgunVerifier(t *testing.T) {
	const key = "signing-key"
	now := strconv.FormatInt(time.Now().Unix(), 10)
	v := MailgunVerifier{SigningKey: key}

	t.Run("accepts a genuine signature", func(t *testing.T) {
		if err := v.Verify(mailgunBody(t, key, now, "tok")); err != nil {
			t.Errorf("valid signature rejected: %v", err)
		}
	})

	t.Run("rejects the wrong key", func(t *testing.T) {
		if err := v.Verify(mailgunBody(t, "other-key", now, "tok")); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("got %v, want ErrSignatureInvalid", err)
		}
	})

	t.Run("rejects a tampered token", func(t *testing.T) {
		body := mailgunBody(t, key, now, "tok")
		tampered := bytes.Replace(body, []byte(`"tok"`), []byte(`"kot"`), 1)
		if err := v.Verify(tampered); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("got %v, want ErrSignatureInvalid", err)
		}
	})

	t.Run("rejects a replayed old timestamp", func(t *testing.T) {
		old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
		if err := v.Verify(mailgunBody(t, key, old, "tok")); !errors.Is(err, ErrSignatureExpired) {
			t.Errorf("got %v, want ErrSignatureExpired", err)
		}
	})

	t.Run("rejects an unsigned payload", func(t *testing.T) {
		if err := v.Verify([]byte(`{"event-data":{"event":"failed"}}`)); !errors.Is(err, ErrSignatureMissing) {
			t.Errorf("got %v, want ErrSignatureMissing", err)
		}
	})

	t.Run("refuses to run without a key", func(t *testing.T) {
		var empty MailgunVerifier
		if err := empty.Verify(mailgunBody(t, key, now, "tok")); !errors.Is(err, ErrSigningKeyMissing) {
			t.Errorf("got %v, want ErrSigningKeyMissing", err)
		}
	})
}

// --- SendGrid -----------------------------------------------------------

func TestSendGridVerifier(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(der)

	body := []byte(`[{"event":"bounce","email":"a@b.test"}]`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	sign := func(ts string, payload []byte) string {
		h := sha256.New()
		h.Write([]byte(ts))
		h.Write(payload)
		sig, err := ecdsa.SignASN1(rand.Reader, priv, h.Sum(nil))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(sig)
	}

	headers := func(ts, sig string) http.Header {
		h := http.Header{}
		h.Set(SendGridTimestampHeader, ts)
		h.Set(SendGridSignatureHeader, sig)
		return h
	}

	t.Run("accepts a genuine signature", func(t *testing.T) {
		v := &SendGridVerifier{PublicKey: pubB64}
		if err := v.Verify(headers(timestamp, sign(timestamp, body)), body); err != nil {
			t.Errorf("valid signature rejected: %v", err)
		}
	})

	t.Run("rejects a tampered body", func(t *testing.T) {
		v := &SendGridVerifier{PublicKey: pubB64}
		sig := sign(timestamp, body)
		evil := []byte(`[{"event":"bounce","email":"victim@b.test"}]`)
		if err := v.Verify(headers(timestamp, sig), evil); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("got %v, want ErrSignatureInvalid", err)
		}
	})

	t.Run("rejects a signature from another key", func(t *testing.T) {
		other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		otherDER, _ := x509.MarshalPKIXPublicKey(&other.PublicKey)
		v := &SendGridVerifier{PublicKey: base64.StdEncoding.EncodeToString(otherDER)}
		if err := v.Verify(headers(timestamp, sign(timestamp, body)), body); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("got %v, want ErrSignatureInvalid", err)
		}
	})

	t.Run("rejects a stale timestamp", func(t *testing.T) {
		v := &SendGridVerifier{PublicKey: pubB64}
		old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
		if err := v.Verify(headers(old, sign(old, body)), body); !errors.Is(err, ErrSignatureExpired) {
			t.Errorf("got %v, want ErrSignatureExpired", err)
		}
	})

	t.Run("rejects missing headers", func(t *testing.T) {
		v := &SendGridVerifier{PublicKey: pubB64}
		if err := v.Verify(http.Header{}, body); !errors.Is(err, ErrSignatureMissing) {
			t.Errorf("got %v, want ErrSignatureMissing", err)
		}
	})
}

// --- Postmark -----------------------------------------------------------

func TestPostmarkVerifier(t *testing.T) {
	v := PostmarkVerifier{Username: "hook", Password: "s3cret"}

	basic := func(u, p string) http.Header {
		h := http.Header{}
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u+":"+p)))
		return h
	}

	if err := v.Verify(basic("hook", "s3cret")); err != nil {
		t.Errorf("valid credentials rejected: %v", err)
	}
	if err := v.Verify(basic("hook", "wrong")); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("wrong password: got %v, want ErrSignatureInvalid", err)
	}
	if err := v.Verify(basic("nobody", "s3cret")); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("wrong user: got %v, want ErrSignatureInvalid", err)
	}
	if err := v.Verify(http.Header{}); !errors.Is(err, ErrSignatureMissing) {
		t.Errorf("no header: got %v, want ErrSignatureMissing", err)
	}
	var empty PostmarkVerifier
	if err := empty.Verify(basic("a", "b")); !errors.Is(err, ErrSigningKeyMissing) {
		t.Errorf("unconfigured verifier must not accept anything, got %v", err)
	}
}

// --- SNS / SES ----------------------------------------------------------

// certServer serves a self-signed certificate for whatever URL is requested,
// so the AWS host check can be exercised without touching the network.
type certServer struct {
	pem      []byte
	requests int
}

func (c *certServer) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(c.pem)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func newSNSFixture(t *testing.T) (*rsa.PrivateKey, *certServer) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sns.us-east-1.amazonaws.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, &certServer{pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func snsPayload(t *testing.T, priv *rsa.PrivateKey, certURL string, mutate func(map[string]string)) []byte {
	t.Helper()
	msg := map[string]string{
		"Type":             "Notification",
		"MessageId":        "msg-1",
		"TopicArn":         "arn:aws:sns:us-east-1:123456789012:ses-bounces",
		"Message":          `{"notificationType":"Bounce"}`,
		"Timestamp":        time.Now().UTC().Format(time.RFC3339),
		"SignatureVersion": "1",
		"SigningCertURL":   certURL,
	}
	if mutate != nil {
		mutate(msg)
	}

	canonical := ""
	for _, k := range []string{"Message", "MessageId", "Subject", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"} {
		if v, ok := msg[k]; ok && v != "" {
			canonical += k + "\n" + v + "\n"
		}
	}

	var hashed []byte
	var alg crypto.Hash
	if msg["SignatureVersion"] == "2" {
		sum := sha256.Sum256([]byte(canonical))
		hashed, alg = sum[:], crypto.SHA256
	} else {
		sum := sha1.Sum([]byte(canonical))
		hashed, alg = sum[:], crypto.SHA1
	}
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, alg, hashed)
	if err != nil {
		t.Fatal(err)
	}
	msg["Signature"] = base64.StdEncoding.EncodeToString(sig)

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSNSVerifier(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	priv, server := newSNSFixture(t)

	newVerifier := func() *SNSVerifier {
		return &SNSVerifier{Client: &http.Client{Transport: server}}
	}

	t.Run("accepts a genuine notification", func(t *testing.T) {
		msg, err := newVerifier().Verify(t.Context(), snsPayload(t, priv, certURL, nil))
		if err != nil {
			t.Fatalf("valid notification rejected: %v", err)
		}
		if msg.Message != `{"notificationType":"Bounce"}` {
			t.Errorf("unexpected inner message: %q", msg.Message)
		}
	})

	t.Run("accepts SignatureVersion 2", func(t *testing.T) {
		body := snsPayload(t, priv, certURL, func(m map[string]string) { m["SignatureVersion"] = "2" })
		if _, err := newVerifier().Verify(t.Context(), body); err != nil {
			t.Errorf("SHA256 notification rejected: %v", err)
		}
	})

	t.Run("rejects a tampered message", func(t *testing.T) {
		body := snsPayload(t, priv, certURL, nil)
		tampered := bytes.Replace(body, []byte("Bounce"), []byte("Cmplnt"), 1)
		if _, err := newVerifier().Verify(t.Context(), tampered); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("got %v, want ErrSignatureInvalid", err)
		}
	})

	// The attacker-controlled SigningCertURL is the classic SNS forgery: sign
	// the payload with your own key and point the URL at your own server.
	t.Run("rejects a non-AWS signing cert url", func(t *testing.T) {
		for _, bad := range []string{
			"https://evil.test/cert.pem",
			"http://sns.us-east-1.amazonaws.com/cert.pem",
			"https://sns.us-east-1.amazonaws.com.evil.test/cert.pem",
			"https://evil.test/sns.us-east-1.amazonaws.com/cert.pem",
			"https://sns.us-east-1.amazonaws.com/cert.txt",
		} {
			body := snsPayload(t, priv, bad, nil)
			_, err := newVerifier().Verify(t.Context(), body)
			if !errors.Is(err, ErrSignatureInvalid) {
				t.Errorf("%s: got %v, want ErrSignatureInvalid", bad, err)
			}
		}
	})

	t.Run("enforces the topic allowlist", func(t *testing.T) {
		v := &SNSVerifier{
			Client:    &http.Client{Transport: server},
			TopicARNs: []string{"arn:aws:sns:us-east-1:123456789012:other-topic"},
		}
		if _, err := v.Verify(t.Context(), snsPayload(t, priv, certURL, nil)); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("got %v, want ErrSignatureInvalid for an unlisted topic", err)
		}
	})

	t.Run("rejects an unsigned payload", func(t *testing.T) {
		if _, err := newVerifier().Verify(t.Context(), []byte(`{"Type":"Notification"}`)); !errors.Is(err, ErrSignatureMissing) {
			t.Errorf("got %v, want ErrSignatureMissing", err)
		}
	})

	t.Run("caches the certificate", func(t *testing.T) {
		v := newVerifier()
		before := server.requests
		for i := 0; i < 3; i++ {
			if _, err := v.Verify(t.Context(), snsPayload(t, priv, certURL, nil)); err != nil {
				t.Fatal(err)
			}
		}
		if got := server.requests - before; got != 1 {
			t.Errorf("fetched the signing cert %d times, want 1", got)
		}
	})
}

// A verified SNS envelope feeds straight into the existing SES parser.
func TestSNSVerifyThenParseSESWebhook(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	priv, server := newSNSFixture(t)

	inner := `{"notificationType":"Bounce","bounce":{"bounceType":"Permanent","bouncedRecipients":[{"emailAddress":"a@b.test","status":"5.1.1"}],"timestamp":"2024-01-01T00:00:00Z"},"mail":{"messageId":"m-1"}}`
	body := snsPayload(t, priv, certURL, func(m map[string]string) { m["Message"] = inner })

	v := &SNSVerifier{Client: &http.Client{Transport: server}}
	msg, err := v.Verify(t.Context(), body)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	event, err := ParseSESWebhook([]byte(msg.Message))
	if err != nil {
		t.Fatalf("ParseSESWebhook: %v", err)
	}
	bounce, ok := event.(*Bounce)
	if !ok {
		t.Fatalf("got %T, want *Bounce", event)
	}
	if bounce.Type != BounceHard || bounce.EmailAddress != "a@b.test" {
		t.Errorf("unexpected bounce: %+v", bounce)
	}
}

// The canonical string is reproduced from the worked example in AWS's own
// documentation, not from this package's implementation, so this test is an
// external check rather than a restatement of the code under test.
//
// Source: "Verifying the signature of an Amazon SNS message when using HTTP
// query-based requests", docs.aws.amazon.com/sns/latest/dg/
// sns-verify-signature-of-message-verify-message-signature.html
func TestSNSCanonicalStringMatchesAWSDocumentedExample(t *testing.T) {
	raw := map[string]any{
		"Type":             "Notification",
		"MessageId":        "4d4dc071-ddbf-465d-bba8-08f81c89da64",
		"TopicArn":         "arn:aws:sns:us-east-2:123456789012:s4-MySNSTopic-1G1WEFCOXTC0P",
		"Subject":          "My subject",
		"Message":          "My Test Message",
		"Timestamp":        "2019-01-31T04:37:04.321Z",
		"SignatureVersion": "1",
		"Signature":        "ignored",
		"SigningCertURL":   "ignored",
		"UnsubscribeURL":   "ignored",
	}

	// AWS's reference validator emits "{key}\n{value}\n" for every present
	// field, including the last. The prose says otherwise; the prose is wrong
	// (its shell example gets the final newline from `echo`).
	want := "Message\nMy Test Message\n" +
		"MessageId\n4d4dc071-ddbf-465d-bba8-08f81c89da64\n" +
		"Subject\nMy subject\n" +
		"Timestamp\n2019-01-31T04:37:04.321Z\n" +
		"TopicArn\narn:aws:sns:us-east-2:123456789012:s4-MySNSTopic-1G1WEFCOXTC0P\n" +
		"Type\nNotification\n"

	if got := string(snsCanonicalString(raw)); got != want {
		t.Errorf("canonical string mismatch\n got %q\nwant %q", got, want)
	}
}

// Fields not in the signable set must not leak into the string to sign, and
// absent optional fields must be skipped entirely.
func TestSNSCanonicalStringSkipsAbsentAndUnsignedFields(t *testing.T) {
	raw := map[string]any{
		"Type":           "Notification",
		"MessageId":      "m-1",
		"TopicArn":       "arn:topic",
		"Message":        "hello",
		"Timestamp":      "2024-01-01T00:00:00.000Z",
		"UnsubscribeURL": "https://example.test/unsub", // not signable
		"Signature":      "sig",                        // not signable
	}
	want := "Message\nhello\nMessageId\nm-1\nTimestamp\n2024-01-01T00:00:00.000Z\n" +
		"TopicArn\narn:topic\nType\nNotification\n"

	if got := string(snsCanonicalString(raw)); got != want {
		t.Errorf("canonical string mismatch\n got %q\nwant %q", got, want)
	}
}

// A SubscriptionConfirmation carrying a Subject must still verify. A
// per-message-type field list would silently drop it and reject the message.
func TestSNSSubscriptionConfirmationWithSubject(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	priv, server := newSNSFixture(t)

	body := snsPayload(t, priv, certURL, func(m map[string]string) {
		m["Type"] = "SubscriptionConfirmation"
		m["Token"] = "tok-123"
		m["SubscribeURL"] = "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription"
		m["Subject"] = "a subject on a confirmation"
	})

	v := &SNSVerifier{Client: &http.Client{Transport: server}}
	if _, err := v.Verify(t.Context(), body); err != nil {
		t.Errorf("SubscriptionConfirmation with a Subject was rejected: %v", err)
	}
}

// An unrecognised SignatureVersion must be refused outright rather than
// quietly falling back to SHA-1.
func TestSNSRejectsUnknownSignatureVersion(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	priv, server := newSNSFixture(t)

	for _, version := range []string{"3", "0", "", "sha256"} {
		body := snsPayload(t, priv, certURL, func(m map[string]string) {
			m["SignatureVersion"] = version
		})
		v := &SNSVerifier{Client: &http.Client{Transport: server}}
		if _, err := v.Verify(t.Context(), body); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("SignatureVersion %q: got %v, want ErrSignatureInvalid", version, err)
		}
	}
}

// An unrecognised message Type must be refused.
func TestSNSRejectsUnknownType(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	priv, server := newSNSFixture(t)

	body := snsPayload(t, priv, certURL, func(m map[string]string) {
		m["Type"] = "SomethingElse"
	})
	v := &SNSVerifier{Client: &http.Client{Transport: server}}
	if _, err := v.Verify(t.Context(), body); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("got %v, want ErrSignatureInvalid", err)
	}
}
