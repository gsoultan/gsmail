package gsmail

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"

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
