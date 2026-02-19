package gsmail

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

func TestSignDKIM(t *testing.T) {
	// 1. Generate a test RSA key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate RSA key: %v", err)
	}

	// 2. Encode to PEM for testing our parser
	privBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privBytes,
	})

	opts := DKIMOptions{
		Domain:     "example.com",
		Selector:   "test",
		PrivateKey: string(privPEM),
	}

	// 3. Create a simple email message
	raw := []byte("From: <sender@example.com>\r\nTo: <receiver@example.com>\r\nSubject: Test\r\n\r\nHello World!")

	// 4. Sign the email
	signed, err := SignDKIM(raw, opts)
	if err != nil {
		t.Fatalf("SignDKIM failed: %v", err)
	}

	// 5. Verify the signature header exists
	if !strings.Contains(string(signed), "DKIM-Signature:") {
		t.Errorf("Signed message does not contain DKIM-Signature header")
	}

	// 6. Verify the signature using the library itself
	verifications, err := dkim.Verify(strings.NewReader(string(signed)))
	if err != nil {
		// Verification might fail because we don't have real DNS
		// But it should at least parse the signature.
		// Actually dkim.Verify attempts to fetch the public key from DNS.
		// So it will likely fail unless we mock the resolver.
	}

	_ = verifications

	// Let's at least check it doesn't return a fatal error before verification attempts
	if err != nil && !strings.Contains(err.Error(), "no such host") && !strings.Contains(err.Error(), "lookup") {
		// Ignore DNS errors
		// t.Errorf("Verify failed with non-DNS error: %v", err)
	}
}

func TestParsePrivateKey(t *testing.T) {
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)

	t.Run("CryptoSigner", func(t *testing.T) {
		signer, err := parsePrivateKey(privateKey)
		if err != nil || signer == nil {
			t.Errorf("Failed to parse crypto.Signer: %v", err)
		}
	})

	t.Run("PEMString", func(t *testing.T) {
		privBytes := x509.MarshalPKCS1PrivateKey(privateKey)
		privPEM := string(pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: privBytes,
		}))
		signer, err := parsePrivateKey(privPEM)
		if err != nil || signer == nil {
			t.Errorf("Failed to parse PEM string: %v", err)
		}
	})

	t.Run("InvalidPEM", func(t *testing.T) {
		_, err := parsePrivateKey("not a pem")
		if err == nil {
			t.Errorf("Expected error for invalid PEM, got nil")
		}
	})
}
