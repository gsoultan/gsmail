package gsmail

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"

	"github.com/emersion/go-msgauth/dkim"
)

// The existing DKIM test asserted only that the string "DKIM-Signature:"
// appeared in the output: it called dkim.Verify, swallowed the error into an
// empty block, discarded the result and left its one real assertion commented
// out. A structurally plausible but cryptographically wrong signature would
// have passed it.
//
// That matters more than an unsigned message would. Receivers treat a failed
// DKIM signature as a stronger negative signal than no signature at all, and
// the failure is silent: you learn about it from a deliverability dashboard,
// not from an error.
//
// dkim.VerifyOptions exposes a LookupTXT hook, so the whole thing verifies
// offline against a published key. There was never a reason not to.

// dkimKey is a signing key plus the DNS record that publishes its public half.
type dkimKey struct {
	privatePEM string
	lookupTXT  func(domain string) ([]string, error)
	record     string
}

// newDKIMKey generates a key and the resolver stub that serves it for
// selector._domainkey.domain.
func newDKIMKey(t *testing.T, selector, domain string) dkimKey {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	privatePEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	}))

	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	record := "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pubDER)

	want := fmt.Sprintf("%s._domainkey.%s", selector, domain)
	return dkimKey{
		privatePEM: privatePEM,
		record:     record,
		lookupTXT: func(d string) ([]string, error) {
			if strings.TrimSuffix(d, ".") != want {
				return nil, fmt.Errorf("no TXT record for %q", d)
			}
			return []string{record}, nil
		},
	}
}

// verifyDKIM runs the signed message through a real verifier using the stubbed
// resolver, and returns the verification results.
func verifyDKIM(t *testing.T, signed []byte, key dkimKey) []*dkim.Verification {
	t.Helper()

	results, err := dkim.VerifyWithOptions(strings.NewReader(string(signed)),
		&dkim.VerifyOptions{LookupTXT: key.lookupTXT})
	if err != nil {
		t.Fatalf("verification could not run: %v", err)
	}
	return results
}

func signedMessage(t *testing.T, key dkimKey, selector, domain string, email Email) []byte {
	t.Helper()

	raw, err := RenderMessage(email)
	if err != nil {
		t.Fatalf("RenderMessage: %v", err)
	}
	signed, err := SignDKIM(raw, DKIMOptions{
		Domain:     domain,
		Selector:   selector,
		PrivateKey: key.privatePEM,
	})
	if err != nil {
		t.Fatalf("SignDKIM: %v", err)
	}
	return signed
}

func basicSignedEmail() Email {
	return Email{
		From:    "sender@example.com",
		To:      []string{"recipient@example.org"},
		Subject: "Signed message",
		Body:    []byte("This message carries a DKIM signature."),
	}
}

// The headline: a verifier must actually accept what SignDKIM produces.
func TestDKIMSignatureVerifies(t *testing.T) {
	const selector, domain = "s1", "example.com"
	key := newDKIMKey(t, selector, domain)

	signed := signedMessage(t, key, selector, domain, basicSignedEmail())
	results := verifyDKIM(t, signed, key)

	if len(results) != 1 {
		t.Fatalf("got %d verifications, want 1", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("the signature this package produced does not verify: %v", results[0].Err)
	}
	if results[0].Domain != domain {
		t.Errorf("signature claims domain %q, want %q", results[0].Domain, domain)
	}
}

// Both canonicalisation modes have to produce a verifiable signature. Relaxed
// is the default because it survives the whitespace changes a relay makes;
// simple is stricter and is what breaks first if anything rewrites the message.
func TestDKIMCanonicalizationsVerify(t *testing.T) {
	const selector, domain = "s1", "example.com"

	for _, mode := range []string{"relaxed", "simple", ""} {
		name := mode
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			key := newDKIMKey(t, selector, domain)

			raw, err := RenderMessage(basicSignedEmail())
			if err != nil {
				t.Fatal(err)
			}
			signed, err := SignDKIM(raw, DKIMOptions{
				Domain:                 domain,
				Selector:               selector,
				PrivateKey:             key.privatePEM,
				HeaderCanonicalization: mode,
				BodyCanonicalization:   mode,
			})
			if err != nil {
				t.Fatalf("SignDKIM: %v", err)
			}

			results := verifyDKIM(t, signed, key)
			if len(results) != 1 || results[0].Err != nil {
				t.Fatalf("%s canonicalization does not verify: %+v", name, results)
			}
		})
	}
}

// A multipart message with attachments is the case most likely to expose a
// body-canonicalisation mistake, because the body is large and structured.
func TestDKIMVerifiesMultipartMessage(t *testing.T) {
	const selector, domain = "s1", "example.com"
	key := newDKIMKey(t, selector, domain)

	email := basicSignedEmail()
	email.HTMLBody = []byte("<p>This message carries a DKIM signature.</p>")
	email.Attachments = []Attachment{
		{Filename: "report.pdf", ContentType: "application/pdf", Data: []byte("pdf bytes")},
		{Filename: "logo.png", ContentType: "image/png", ContentID: "logo", Data: []byte("png bytes")},
	}
	email.SetHeader("List-Unsubscribe", "<https://example.com/u>")

	signed := signedMessage(t, key, selector, domain, email)
	results := verifyDKIM(t, signed, key)

	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("a multipart signed message does not verify: %+v", results)
	}
}

// Signing a message whose body is not plain ASCII must still verify: the body
// is base64 encoded before signing, so a mistake here would only show up on
// non-English mail.
func TestDKIMVerifiesUnicodeMessage(t *testing.T) {
	const selector, domain = "s1", "example.com"
	key := newDKIMKey(t, selector, domain)

	email := basicSignedEmail()
	email.Subject = "Naïve — 発送のお知らせ ✉"
	email.Body = []byte("Grüße und 日本語のテキスト ✉")

	signed := signedMessage(t, key, selector, domain, email)
	results := verifyDKIM(t, signed, key)

	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("a Unicode signed message does not verify: %+v", results)
	}
}

// A verifier that accepts everything would make the tests above meaningless.
// Tampering with the signed message must be detected.
func TestDKIMDetectsTampering(t *testing.T) {
	const selector, domain = "s1", "example.com"

	tests := map[string]func([]byte) []byte{
		"body modified": func(b []byte) []byte {
			return []byte(strings.Replace(string(b),
				"VGhpcyBtZXNzYWdl", "VGhpcyBtZXNzYWdX", 1)) // flip a base64 char
		},
		"subject modified": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "Signed message", "Hijacked title", 1))
		},
		"from modified": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "sender@example.com", "attacker@evil.test", 1))
		},
	}

	for name, tamper := range tests {
		t.Run(name, func(t *testing.T) {
			key := newDKIMKey(t, selector, domain)
			signed := signedMessage(t, key, selector, domain, basicSignedEmail())

			modified := tamper(signed)
			if string(modified) == string(signed) {
				t.Fatal("the tamper function changed nothing; the test would be vacuous")
			}

			results := verifyDKIM(t, modified, key)
			if len(results) == 1 && results[0].Err == nil {
				t.Fatal("a tampered message verified; the signature is not protecting anything")
			}
		})
	}
}

// A signature made with one key must not verify against another. Without this
// the tests above would pass even if verification ignored the key entirely.
func TestDKIMRejectsWrongKey(t *testing.T) {
	const selector, domain = "s1", "example.com"

	signing := newDKIMKey(t, selector, domain)
	published := newDKIMKey(t, selector, domain) // a different key pair

	signed := signedMessage(t, signing, selector, domain, basicSignedEmail())

	results := verifyDKIM(t, signed, published)
	if len(results) == 1 && results[0].Err == nil {
		t.Fatal("a signature verified against a key that did not produce it")
	}
}

// The signature must cover the headers that matter, or an intermediary can
// rewrite them without breaking it.
func TestDKIMSignsTheImportantHeaders(t *testing.T) {
	const selector, domain = "s1", "example.com"
	key := newDKIMKey(t, selector, domain)

	signed := signedMessage(t, key, selector, domain, basicSignedEmail())
	results := verifyDKIM(t, signed, key)
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("signature does not verify: %+v", results)
	}

	covered := map[string]bool{}
	for _, h := range results[0].HeaderKeys {
		covered[strings.ToLower(h)] = true
	}
	for _, required := range []string{"from", "to", "subject"} {
		if !covered[required] {
			t.Errorf("the signature does not cover %q; an intermediary could rewrite it (covered: %v)",
				required, results[0].HeaderKeys)
		}
	}
}

// Signing is the last step before transmission, so it must not disturb the
// message it signs.
func TestDKIMPreservesTheOriginalMessage(t *testing.T) {
	const selector, domain = "s1", "example.com"
	key := newDKIMKey(t, selector, domain)

	raw, err := RenderMessage(basicSignedEmail())
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignDKIM(raw, DKIMOptions{
		Domain: domain, Selector: selector, PrivateKey: key.privatePEM,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(string(signed), string(raw)) {
		t.Error("signing altered the message body; the DKIM-Signature header should only be prepended")
	}
	if strings.Count(string(signed), "DKIM-Signature:") != 1 {
		t.Errorf("expected exactly one DKIM-Signature header, got %d",
			strings.Count(string(signed), "DKIM-Signature:"))
	}
}
