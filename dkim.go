package gsmail

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/emersion/go-msgauth/dkim"
)

// DKIMOptions holds the configuration for DKIM signing.
type DKIMOptions struct {
	Domain   string
	Selector string
	// PrivateKey can be a PEM-encoded string, []byte or a crypto.Signer (e.g., *rsa.PrivateKey)
	PrivateKey             any
	HeaderCanonicalization string // "simple" or "relaxed" (default: "relaxed")
	BodyCanonicalization   string // "simple" or "relaxed" (default: "relaxed")
}

// SignDKIM signs the raw email bytes with the provided DKIM options.
func SignDKIM(raw []byte, opts DKIMOptions) ([]byte, error) {
	if opts.Domain == "" || opts.Selector == "" || opts.PrivateKey == nil {
		return nil, fmt.Errorf("dkim: Domain, Selector, and PrivateKey are required")
	}

	signer, err := parsePrivateKey(opts.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("dkim: parse private key: %w", err)
	}

	dkimOpts := &dkim.SignOptions{
		Domain:   opts.Domain,
		Selector: opts.Selector,
		Signer:   signer,
	}

	if opts.HeaderCanonicalization == "simple" {
		dkimOpts.HeaderCanonicalization = dkim.CanonicalizationSimple
	} else {
		dkimOpts.HeaderCanonicalization = dkim.CanonicalizationRelaxed
	}

	if opts.BodyCanonicalization == "simple" {
		dkimOpts.BodyCanonicalization = dkim.CanonicalizationSimple
	} else {
		dkimOpts.BodyCanonicalization = dkim.CanonicalizationRelaxed
	}

	var b bytes.Buffer
	if err := dkim.Sign(&b, bytes.NewReader(raw), dkimOpts); err != nil {
		return nil, fmt.Errorf("dkim: sign: %w", err)
	}

	return b.Bytes(), nil
}

func parsePrivateKey(key any) (crypto.Signer, error) {
	if s, ok := key.(crypto.Signer); ok {
		return s, nil
	}

	var b []byte
	switch v := key.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return nil, fmt.Errorf("unsupported private key type: %T", key)
	}

	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	var pk any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		pk, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		pk, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}

	if err != nil {
		return nil, err
	}

	signer, ok := pk.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("private key does not implement crypto.Signer")
	}

	return signer, nil
}

// DKIMPublicKeyRecord returns the value a DKIM TXT record should publish for a
// private key: "v=DKIM1; k=rsa; p=<base64 DER>".
//
// It accepts the same key forms as SignDKIM — a PEM string or []byte, or a
// crypto.Signer — so the key you sign with is the key you check against.
func DKIMPublicKeyRecord(privateKey any) (string, error) {
	p, err := dkimPublicKeyBase64(privateKey)
	if err != nil {
		return "", err
	}
	return "v=DKIM1; k=rsa; p=" + p, nil
}

// dkimPublicKeyBase64 derives the base64 DER public half of a private key,
// which is what a DKIM record carries in its p= tag.
func dkimPublicKeyBase64(privateKey any) (string, error) {
	signer, err := parsePrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("dkim: parse private key: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return "", fmt.Errorf("dkim: marshal public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// dkimRecordTags parses a DKIM TXT record into its tag/value pairs.
func dkimRecordTags(record string) map[string]string {
	tags := make(map[string]string)
	for _, part := range strings.Split(record, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if k, v, ok := strings.Cut(part, "="); ok {
			// A base64 p= value may itself contain '=' padding, so only the
			// first separator delimits the tag.
			tags[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return tags
}

// dkimPublishedKey extracts the p= value from a record, with whitespace and
// quoting removed. Published records are frequently split across quoted
// strings and re-joined with spaces, neither of which is part of the key.
//
// The bool reports whether a p= tag was present at all. An absent tag and an
// empty one are different faults -- a malformed record versus a deliberate
// revocation -- so callers must be able to tell them apart.
func dkimPublishedKey(record string) (string, bool) {
	p, ok := dkimRecordTags(record)["p"]
	if !ok {
		return "", false
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n', '"':
			return -1
		}
		return r
	}, p), true
}
