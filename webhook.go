package gsmail

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Webhook payloads are unauthenticated by default: anything that can reach
// your endpoint can post a bounce for any address, and a forged hard bounce
// will suppress a real customer. The Parse*Webhook functions in bounce.go
// deliberately do no verification, so authenticate the request first with one
// of the verifiers below and only then parse the body.

// Webhook verification errors.
var (
	ErrSignatureMissing  = errors.New("gsmail: webhook signature is missing")
	ErrSignatureInvalid  = errors.New("gsmail: webhook signature is invalid")
	ErrSignatureExpired  = errors.New("gsmail: webhook timestamp is outside the tolerance window")
	ErrSigningKeyMissing = errors.New("gsmail: no signing key configured")
)

// DefaultWebhookTolerance is how far a webhook timestamp may drift from the
// current time before the request is rejected as a possible replay.
const DefaultWebhookTolerance = 5 * time.Minute

// tolerance returns the configured window, or the default when unset.
func tolerance(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultWebhookTolerance
	}
	return d
}

// withinTolerance reports whether ts is close enough to now.
func withinTolerance(ts time.Time, window time.Duration) bool {
	delta := time.Since(ts)
	if delta < 0 {
		delta = -delta
	}
	return delta <= window
}

// --- Mailgun -----------------------------------------------------------

// MailgunVerifier authenticates Mailgun webhooks.
//
// Mailgun signs each event with HMAC-SHA256 over the concatenation of the
// timestamp and a single-use token, keyed by the webhook signing key from your
// Mailgun dashboard (this is not the API key).
type MailgunVerifier struct {
	// SigningKey is the Mailgun HTTP webhook signing key.
	SigningKey string

	// Tolerance bounds how stale a timestamp may be. Zero uses
	// DefaultWebhookTolerance.
	Tolerance time.Duration
}

// mailgunSignature is the envelope Mailgun wraps every event in.
type mailgunSignature struct {
	Signature struct {
		Timestamp string `json:"timestamp"`
		Token     string `json:"token"`
		Signature string `json:"signature"`
	} `json:"signature"`
}

// Verify authenticates a raw Mailgun webhook body.
//
// It returns nil only when the signature matches and the timestamp is inside
// the tolerance window. Callers should additionally reject a token they have
// already seen, which is the only way to make replay protection complete.
func (v MailgunVerifier) Verify(body []byte) error {
	if v.SigningKey == "" {
		return ErrSigningKeyMissing
	}

	var payload mailgunSignature
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("gsmail: parse mailgun signature: %w", err)
	}

	sig := payload.Signature
	if sig.Signature == "" || sig.Timestamp == "" || sig.Token == "" {
		return ErrSignatureMissing
	}

	secs, err := strconv.ParseInt(sig.Timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("gsmail: parse mailgun timestamp: %w", err)
	}
	if !withinTolerance(time.Unix(secs, 0), tolerance(v.Tolerance)) {
		return ErrSignatureExpired
	}

	mac := hmac.New(sha256.New, []byte(v.SigningKey))
	mac.Write([]byte(sig.Timestamp))
	mac.Write([]byte(sig.Token))
	expected := mac.Sum(nil)

	got, err := hex.DecodeString(sig.Signature)
	if err != nil {
		return ErrSignatureInvalid
	}
	if !hmac.Equal(expected, got) {
		return ErrSignatureInvalid
	}
	return nil
}

// --- SendGrid ----------------------------------------------------------

// SendGrid signed-event header names.
const (
	SendGridSignatureHeader = "X-Twilio-Email-Event-Webhook-Signature"
	SendGridTimestampHeader = "X-Twilio-Email-Event-Webhook-Timestamp"
)

// SendGridVerifier authenticates SendGrid Event Webhook requests.
//
// SendGrid signs the timestamp concatenated with the raw request body using
// ECDSA on P-256, and publishes the matching public key in the dashboard as
// base64-encoded DER.
type SendGridVerifier struct {
	// PublicKey is the base64 DER (PKIX) verification key from the SendGrid
	// "Signed Event Webhook Requests" settings.
	PublicKey string

	// Tolerance bounds how stale a timestamp may be. Zero uses
	// DefaultWebhookTolerance.
	Tolerance time.Duration

	once   sync.Once
	parsed *ecdsa.PublicKey
	err    error
}

// publicKey decodes and caches the configured verification key.
func (v *SendGridVerifier) publicKey() (*ecdsa.PublicKey, error) {
	v.once.Do(func() {
		if v.PublicKey == "" {
			v.err = ErrSigningKeyMissing
			return
		}
		der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v.PublicKey))
		if err != nil {
			v.err = fmt.Errorf("gsmail: decode sendgrid public key: %w", err)
			return
		}
		key, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			v.err = fmt.Errorf("gsmail: parse sendgrid public key: %w", err)
			return
		}
		ecKey, ok := key.(*ecdsa.PublicKey)
		if !ok {
			v.err = fmt.Errorf("gsmail: sendgrid public key is %T, want *ecdsa.PublicKey", key)
			return
		}
		v.parsed = ecKey
	})
	return v.parsed, v.err
}

// Verify authenticates a SendGrid webhook from its headers and raw body.
//
// Pass the body exactly as received: re-marshalling the JSON changes the bytes
// that were signed and the check will fail.
func (v *SendGridVerifier) Verify(header http.Header, body []byte) error {
	key, err := v.publicKey()
	if err != nil {
		return err
	}

	signature := header.Get(SendGridSignatureHeader)
	timestamp := header.Get(SendGridTimestampHeader)
	if signature == "" || timestamp == "" {
		return ErrSignatureMissing
	}

	secs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("gsmail: parse sendgrid timestamp: %w", err)
	}
	if !withinTolerance(time.Unix(secs, 0), tolerance(v.Tolerance)) {
		return ErrSignatureExpired
	}

	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return ErrSignatureInvalid
	}

	h := sha256.New()
	h.Write([]byte(timestamp))
	h.Write(body)

	if !ecdsa.VerifyASN1(key, h.Sum(nil), sig) {
		return ErrSignatureInvalid
	}
	return nil
}

// --- Postmark ----------------------------------------------------------

// PostmarkVerifier authenticates Postmark webhooks.
//
// Postmark does not sign its webhooks. The supported mechanism is HTTP Basic
// authentication embedded in the webhook URL you register, so this verifier
// checks those credentials in constant time. Serve the endpoint over HTTPS;
// without it the credentials travel in clear text.
type PostmarkVerifier struct {
	Username string
	Password string
}

// Verify authenticates a Postmark webhook request from its Authorization header.
func (v PostmarkVerifier) Verify(header http.Header) error {
	if v.Username == "" && v.Password == "" {
		return ErrSigningKeyMissing
	}

	raw := header.Get("Authorization")
	if raw == "" {
		return ErrSignatureMissing
	}
	const prefix = "Basic "
	if !strings.HasPrefix(raw, prefix) {
		return ErrSignatureInvalid
	}
	decoded, err := base64.StdEncoding.DecodeString(raw[len(prefix):])
	if err != nil {
		return ErrSignatureInvalid
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return ErrSignatureInvalid
	}

	// Compare both halves unconditionally so the timing does not reveal which
	// one was wrong.
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(v.Username))
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(v.Password))
	if userOK&passOK != 1 {
		return ErrSignatureInvalid
	}
	return nil
}

// --- AWS SNS (SES) -----------------------------------------------------

// snsCertURLPattern bounds where a signing certificate may be fetched from.
// Without it, an attacker supplies their own SigningCertURL, signs the payload
// with the matching private key, and the signature checks out.
var snsCertURLPattern = regexp.MustCompile(`^sns\.[a-z0-9-]+\.amazonaws\.com(\.cn)?$`)

// SNSVerifier authenticates AWS SNS messages, which is how SES delivers bounce
// and complaint notifications.
//
// The zero value is usable. It fetches the signing certificate named in the
// message, having first checked that the URL points at an AWS SNS endpoint,
// and caches it by URL.
type SNSVerifier struct {
	// Client fetches signing certificates. Defaults to a client with a ten
	// second timeout.
	Client *http.Client

	// TopicARNs, when non-empty, restricts accepted messages to these topics.
	// Set it: a valid signature only proves the message came from SNS, not
	// that it came from *your* topic.
	TopicARNs []string

	// certs caches signing certificates by URL. The URL comes out of the
	// request body, so it is attacker-influenced even after the AWS host check
	// passes: the path is still arbitrary. The cache is therefore bounded and
	// evicted rather than left to grow, so a flood of distinct URLs cannot
	// turn the verifier into a memory sink.
	certsMu sync.Mutex
	certs   map[string]*x509.Certificate
}

// maxCachedSigningCerts bounds the signing certificate cache. AWS rotates
// through a handful of certificates, so this is far more headroom than a
// legitimate deployment needs.
const maxCachedSigningCerts = 32

// SNSMessage is the envelope SNS wraps a notification in.
type SNSMessage struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	Token            string `json:"Token"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	SubscribeURL     string `json:"SubscribeURL"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
}

func (v *SNSVerifier) client() *http.Client {
	if v.Client != nil {
		return v.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Verify authenticates a raw SNS payload and returns the parsed envelope.
//
// A nil error means the message genuinely originated from AWS SNS and, when
// TopicARNs is set, from one of your topics. Only then is it safe to hand
// msg.Message to ParseSESWebhook.
func (v *SNSVerifier) Verify(ctx context.Context, body []byte) (*SNSMessage, error) {
	var msg SNSMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("gsmail: parse sns message: %w", err)
	}
	if msg.Signature == "" || msg.SigningCertURL == "" {
		return nil, ErrSignatureMissing
	}

	// The canonical string is built from the fields actually present in the
	// payload, so decode it a second time as a raw object. Reconstructing it
	// from the struct cannot distinguish an absent field from an empty one.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("gsmail: parse sns message: %w", err)
	}

	switch msg.Type {
	case "Notification", "SubscriptionConfirmation", "UnsubscribeConfirmation":
	default:
		return nil, fmt.Errorf("%w: unsupported SNS type %q", ErrSignatureInvalid, msg.Type)
	}

	// Pin the hash to the declared version and reject anything else. Falling
	// back to SHA-1 for an unrecognised value would let a future or forged
	// SignatureVersion silently select the weaker algorithm.
	var hashAlg crypto.Hash
	var hashed []byte
	switch msg.SignatureVersion {
	case "1":
		hashAlg = crypto.SHA1
		sum := sha1.Sum(snsCanonicalString(raw))
		hashed = sum[:]
	case "2":
		hashAlg = crypto.SHA256
		sum := sha256.Sum256(snsCanonicalString(raw))
		hashed = sum[:]
	default:
		return nil, fmt.Errorf("%w: unsupported SignatureVersion %q",
			ErrSignatureInvalid, msg.SignatureVersion)
	}

	if len(v.TopicARNs) > 0 {
		matched := false
		for _, arn := range v.TopicARNs {
			if subtle.ConstantTimeCompare([]byte(arn), []byte(msg.TopicArn)) == 1 {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("%w: unexpected topic %q", ErrSignatureInvalid, msg.TopicArn)
		}
	}

	cert, err := v.certificate(ctx, msg.SigningCertURL)
	if err != nil {
		return nil, err
	}

	sig, err := base64.StdEncoding.DecodeString(msg.Signature)
	if err != nil {
		return nil, ErrSignatureInvalid
	}

	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%w: signing cert holds a %T, want *rsa.PublicKey",
			ErrSignatureInvalid, cert.PublicKey)
	}
	if err := rsa.VerifyPKCS1v15(pub, hashAlg, hashed, sig); err != nil {
		return nil, ErrSignatureInvalid
	}

	return &msg, nil
}

// cachedCert returns a previously fetched certificate for rawURL.
func (v *SNSVerifier) cachedCert(rawURL string) (*x509.Certificate, bool) {
	v.certsMu.Lock()
	defer v.certsMu.Unlock()
	c, ok := v.certs[rawURL]
	return c, ok
}

// storeCert caches cert under rawURL, evicting an arbitrary entry once the
// cache is full. Exact LRU is not worth the bookkeeping here: the working set
// is a handful of AWS certificates, and the only thing that must not happen is
// unbounded growth.
func (v *SNSVerifier) storeCert(rawURL string, cert *x509.Certificate) {
	v.certsMu.Lock()
	defer v.certsMu.Unlock()

	if v.certs == nil {
		v.certs = make(map[string]*x509.Certificate, maxCachedSigningCerts)
	}
	if _, exists := v.certs[rawURL]; !exists && len(v.certs) >= maxCachedSigningCerts {
		for k := range v.certs {
			delete(v.certs, k)
			break
		}
	}
	v.certs[rawURL] = cert
}

// certificate fetches and caches the SNS signing certificate at rawURL.
func (v *SNSVerifier) certificate(ctx context.Context, rawURL string) (*x509.Certificate, error) {
	if c, ok := v.cachedCert(rawURL); ok {
		return c, nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("gsmail: parse signing cert url: %w", err)
	}
	if u.Scheme != "https" || !snsCertURLPattern.MatchString(u.Host) || !strings.HasSuffix(u.Path, ".pem") {
		return nil, fmt.Errorf("%w: signing cert url %q is not an AWS SNS endpoint", ErrSignatureInvalid, rawURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gsmail: fetch signing cert: %w", err)
	}
	defer DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, NewHTTPError("sns signing cert", resp)
	}

	pemBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("gsmail: read signing cert: %w", err)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: signing cert is not PEM", ErrSignatureInvalid)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("gsmail: parse signing cert: %w", err)
	}

	v.storeCert(rawURL, cert)
	return cert, nil
}

// snsSignableKeys are the fields that participate in the SNS signature, in the
// byte-sort order AWS requires.
//
// This is one list for every message type, and a field is included when it is
// present in the payload — exactly what AWS's own validator does. Using
// per-type lists instead looks tidier but diverges: it would drop a Subject
// from a SubscriptionConfirmation, and reject the message as forged if AWS
// ever sent one.
var snsSignableKeys = []string{
	"Message",
	"MessageId",
	"Subject",
	"SubscribeURL",
	"Timestamp",
	"Token",
	"TopicArn",
	"Type",
}

// snsCanonicalString builds the exact byte string SNS signed: each present
// field as "name\nvalue\n".
//
// Note the trailing newline after the final value. The AWS prose says "do not
// add a newline character at the end of the string", but its own shell example
// pipes the value through `echo`, which supplies one — and AWS's reference
// validator emits "{key}\n{value}\n" for every field with no special case for
// the last. Dropping it makes every signature fail to verify.
func snsCanonicalString(raw map[string]any) []byte {
	var b strings.Builder
	for _, name := range snsSignableKeys {
		v, present := raw[name]
		if !present {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		b.WriteString(name)
		b.WriteByte('\n')
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
