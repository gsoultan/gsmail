package gsmail

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
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

	// Cryptographic verification lives in dkim_verify_test.go, which publishes
	// the public key through a stubbed resolver and runs a real verifier over
	// the result. Checking for the header alone, as this test used to, would
	// pass for a signature that no receiver would accept.
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
