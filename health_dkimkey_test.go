package gsmail

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"strings"
	"testing"
)

// txtResolver serves a fixed set of TXT records through the stubResolver in
// health_test.go, so both files share one fake.
func txtResolver(records map[string][]string) stubResolver {
	return stubResolver{
		txt: func(_ context.Context, name string) ([]string, error) {
			name = strings.TrimSuffix(name, ".")
			if v, ok := records[name]; ok {
				return v, nil
			}
			return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
		},
	}
}

// failingResolver reports a genuine resolver failure rather than NXDOMAIN.
func failingResolver(err error) stubResolver {
	return stubResolver{
		mx:  func(context.Context, string) ([]*net.MX, error) { return nil, err },
		txt: func(context.Context, string) ([]string, error) { return nil, err },
	}
}

func testPrivateKeyPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})), priv
}

func TestDKIMPublicKeyRecord(t *testing.T) {
	keyPEM, priv := testPrivateKeyPEM(t)

	record, err := DKIMPublicKeyRecord(keyPEM)
	if err != nil {
		t.Fatalf("DKIMPublicKeyRecord: %v", err)
	}
	if !strings.HasPrefix(record, "v=DKIM1; k=rsa; p=") {
		t.Errorf("record = %q, want the standard tag prefix", record)
	}

	// The published key must be the public half of the private one.
	wantDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	gotDER, err := x509.ParsePKIXPublicKey(mustDecodeBase64(t, dkimPublishedKey(record)))
	if err != nil {
		t.Fatalf("published key does not parse: %v", err)
	}
	if !priv.PublicKey.Equal(gotDER) {
		t.Error("published key is not the public half of the signing key")
	}
	_ = wantDER

	// A crypto.Signer must give the same answer as its PEM encoding.
	fromSigner, err := DKIMPublicKeyRecord(priv)
	if err != nil {
		t.Fatal(err)
	}
	if fromSigner != record {
		t.Error("a crypto.Signer and its PEM encoding produced different records")
	}
}

func TestCheckDKIMKeyMatches(t *testing.T) {
	keyPEM, _ := testPrivateKeyPEM(t)
	record, err := DKIMPublicKeyRecord(keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	h := HealthChecker{Resolver: txtResolver(map[string][]string{"s1._domainkey.example.com": {record}})}

	res := h.CheckDKIMKey(context.Background(), "example.com", "s1", keyPEM)
	if !res.Found {
		t.Fatal("record should have been found")
	}
	if !res.Valid {
		t.Fatalf("a matching key reported invalid: %+v", res)
	}
}

// The case the check exists for: a key rotated in one place and not the other.
// CheckDKIM alone reports a perfectly valid record, so nothing catches it.
func TestCheckDKIMKeyDetectsRotationDrift(t *testing.T) {
	signingPEM, _ := testPrivateKeyPEM(t)
	publishedPEM, _ := testPrivateKeyPEM(t) // a different key

	publishedRecord, err := DKIMPublicKeyRecord(publishedPEM)
	if err != nil {
		t.Fatal(err)
	}

	h := HealthChecker{Resolver: txtResolver(map[string][]string{"s1._domainkey.example.com": {publishedRecord}})}

	// CheckDKIM is satisfied: the record is well formed and has a p= tag.
	if plain := h.CheckDKIM(context.Background(), "example.com", "s1"); !plain.Valid {
		t.Fatalf("CheckDKIM should consider the record valid on its own: %+v", plain)
	}

	// CheckDKIMKey is not: it is not the key we sign with.
	res := h.CheckDKIMKey(context.Background(), "example.com", "s1", signingPEM)
	if res.Valid {
		t.Fatal("a record published for a different key reported valid")
	}
	if !strings.Contains(res.Details, "does NOT match") {
		t.Errorf("Details = %q, want it to name the mismatch", res.Details)
	}
}

// Published records are commonly split across quoted strings and rejoined with
// whitespace, which is not part of the key.
func TestCheckDKIMKeyIgnoresWhitespaceInThePublishedKey(t *testing.T) {
	keyPEM, _ := testPrivateKeyPEM(t)
	record, err := DKIMPublicKeyRecord(keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	p := dkimPublishedKey(record)
	split := "v=DKIM1; k=rsa; p=" + p[:60] + " " + p[60:120] + "\t" + p[120:]

	h := HealthChecker{Resolver: txtResolver(map[string][]string{"s1._domainkey.example.com": {split}})}

	res := h.CheckDKIMKey(context.Background(), "example.com", "s1", keyPEM)
	if !res.Valid {
		t.Fatalf("a key split across strings should still match: %+v", res)
	}
}

func TestCheckDKIMKeyOnRevokedRecord(t *testing.T) {
	keyPEM, _ := testPrivateKeyPEM(t)

	// An empty p= tag is how a key is revoked.
	h := HealthChecker{Resolver: txtResolver(map[string][]string{
		"s1._domainkey.example.com": {"v=DKIM1; k=rsa; p="},
	})}

	res := h.CheckDKIMKey(context.Background(), "example.com", "s1", keyPEM)
	if res.Valid {
		t.Error("a revoked key must not report valid")
	}
}

func TestCheckDKIMKeyWhenNoRecordIsPublished(t *testing.T) {
	keyPEM, _ := testPrivateKeyPEM(t)
	h := HealthChecker{Resolver: txtResolver(nil)}

	res := h.CheckDKIMKey(context.Background(), "example.com", "s1", keyPEM)
	if res.Found {
		t.Error("no record was published, so Found should be false")
	}
	if res.Valid {
		t.Error("a missing record cannot be valid")
	}
}

func TestCheckDKIMKeyReportsABadPrivateKey(t *testing.T) {
	keyPEM, _ := testPrivateKeyPEM(t)
	record, _ := DKIMPublicKeyRecord(keyPEM)

	h := HealthChecker{Resolver: txtResolver(map[string][]string{"s1._domainkey.example.com": {record}})}

	res := h.CheckDKIMKey(context.Background(), "example.com", "s1", "not a key")
	if res.Error == "" {
		t.Error("an unparseable private key should be reported, not silently treated as a mismatch")
	}
	if res.Valid {
		t.Error("an unparseable key cannot yield a valid verdict")
	}
}

// A domain that does not exist must read the same way from every check.
// It used to report Found:false from SPF and DMARC but an Error from MX, which
// looks like an outage rather than an unconfigured domain.
func TestMissingDomainIsConsistentAcrossChecks(t *testing.T) {
	h := HealthChecker{Resolver: txtResolver(nil)} // every lookup is NXDOMAIN

	health, err := h.CheckDomainHealth(context.Background(), "nonexistent.test", []string{"s1"})
	if err != nil {
		t.Fatalf("CheckDomainHealth: %v", err)
	}

	for name, res := range map[string]HealthResult{
		"MX": health.MX, "SPF": health.SPF, "DMARC": health.DMARC, "DKIM": health.DKIM["s1"],
	} {
		if res.Error != "" {
			t.Errorf("%s reported an error for a nonexistent domain: %q", name, res.Error)
		}
		if res.Found {
			t.Errorf("%s reported Found for a nonexistent domain", name)
		}
		if res.Details == "" {
			t.Errorf("%s gave no explanation for the missing record", name)
		}
	}
}

// A resolver that is genuinely broken must still surface as an error, or a DNS
// outage would read as "no records configured".
func TestResolverFailureIsStillAnError(t *testing.T) {
	h := HealthChecker{Resolver: failingResolver(fmt.Errorf("resolver unreachable"))}

	if res := h.CheckMX(context.Background(), "example.com"); res.Error == "" {
		t.Error("a resolver failure must report an error, not a missing record")
	}
	if res := h.CheckSPF(context.Background(), "example.com"); res.Error == "" {
		t.Error("a resolver failure must report an error, not a missing record")
	}
}

func TestCheckMXReportsHosts(t *testing.T) {
	h := HealthChecker{Resolver: stubResolver{
		mx: func(context.Context, string) ([]*net.MX, error) {
			return []*net.MX{
				{Host: "mx1.example.com.", Pref: 10},
				{Host: "mx2.example.com.", Pref: 20},
			}, nil
		},
	}}

	res := h.CheckMX(context.Background(), "example.com")
	if !res.Found || !res.Valid {
		t.Fatalf("expected a valid result: %+v", res)
	}
	for _, want := range []string{"mx1.example.com. (10)", "mx2.example.com. (20)"} {
		if !strings.Contains(res.Record, want) {
			t.Errorf("Record = %q, want it to contain %q", res.Record, want)
		}
	}
}

func mustDecodeBase64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return b
}
