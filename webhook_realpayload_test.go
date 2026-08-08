package gsmail

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixtures below are real payload *shapes* -- taken from AWS's published
// notification examples and from captured payloads in django-anymail's test
// suite -- rather than the minimal objects the unit tests construct.
//
// The gap they close is not the signing algorithm (that was checked against
// each vendor's reference implementation) but everything around it: real
// envelopes carry fields the minimal fixtures do not, and a canonical string
// or a parser that only ever saw a tidy object is exactly the kind of code
// that breaks on first contact with production.

// signSNS signs an arbitrary SNS envelope with the test key, using the same
// field set and ordering AWS's validator uses.
func signSNS(t *testing.T, priv *rsa.PrivateKey, envelope map[string]any) []byte {
	t.Helper()

	canonical := ""
	for _, k := range []string{"Message", "MessageId", "Subject", "SubscribeURL", "Timestamp", "Token", "TopicArn", "Type"} {
		v, ok := envelope[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue // matches PHP isset(): a null field is absent
		}
		canonical += k + "\n" + s + "\n"
	}

	var hashed []byte
	var alg crypto.Hash
	if envelope["SignatureVersion"] == "2" {
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
	envelope["Signature"] = base64.StdEncoding.EncodeToString(sig)

	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

const realSESBounceEvent = `{"notificationType":"Bounce","bounce":{"bounceType":"Permanent","reportingMTA":"dns; email.example.com","bouncedRecipients":[{"emailAddress":"jane@example.com","status":"5.1.1","action":"failed","diagnosticCode":"smtp; 550 5.1.1 <jane@example.com> User unknown"}],"bounceSubType":"General","timestamp":"2016-01-27T14:59:44.101Z","feedbackId":"00000138111222aa-44455566-cccc-cccc-cccc-ddddaaaa068a-000000","remoteMtaIp":"127.0.2.0"},"mail":{"timestamp":"2016-01-27T14:59:38.237Z","source":"john@example.com","sourceArn":"arn:aws:ses:us-west-2:888888888888:identity/example.com","sourceIp":"127.0.3.0","sendingAccountId":"123456789012","messageId":"00000138111222aa-33322211-cccc-cccc-cccc-ddddaaaa0680-000000","destination":["jane@example.com","mary@example.com","richard@example.com"],"headersTruncated":false,"headers":[{"name":"From","value":"\"John Doe\" <john@example.com>"},{"name":"Subject","value":"Hello"}],"commonHeaders":{"from":["John Doe <john@example.com>"],"subject":"Hello"}}}`

// A full SNS envelope as SES actually delivers one: extra fields the signature
// does not cover, and a Message with a trailing newline.
func TestSNSVerifiesRealSESEnvelope(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-12345abcde.pem"
	priv, server := newSNSFixture(t)

	body := signSNS(t, priv, map[string]any{
		"Type":             "Notification",
		"MessageId":        "19ba9823-d7f2-53c1-860e-cb10e0d13dfc",
		"TopicArn":         "arn:aws:sns:us-east-1:1234567890:SES_Events",
		"Subject":          "Amazon SES Email Event Notification",
		"Message":          realSESBounceEvent + "\n",
		"Timestamp":        "2018-03-26T17:58:59.675Z",
		"SignatureVersion": "1",
		"SigningCertURL":   certURL,
		// Present on every real notification, and NOT part of the signature.
		// Including it in the canonical string would break verification.
		"UnsubscribeURL": "https://sns.us-east-1.amazonaws.com/?Action=Unsubscribe&SubscriptionArn=arn...",
	})

	v := &SNSVerifier{Client: &http.Client{Transport: server}}
	msg, err := v.Verify(t.Context(), body)
	if err != nil {
		t.Fatalf("a realistic SES envelope failed verification: %v", err)
	}

	event, err := ParseSESWebhook([]byte(msg.Message))
	if err != nil {
		t.Fatalf("ParseSESWebhook on the verified payload: %v", err)
	}
	bounce, ok := event.(*Bounce)
	if !ok {
		t.Fatalf("got %T, want *Bounce", event)
	}
	if bounce.Type != BounceHard {
		t.Errorf("bounceType Permanent should map to BounceHard, got %v", bounce.Type)
	}
	if bounce.EmailAddress != "jane@example.com" {
		t.Errorf("EmailAddress = %q", bounce.EmailAddress)
	}
	if bounce.Status != "5.1.1" {
		t.Errorf("Status = %q", bounce.Status)
	}
	if bounce.OriginalMsgID == "" {
		t.Error("OriginalMsgID was not carried through")
	}
}

// SNS sends "Subject": null on notifications published without a subject.
// AWS's validator uses isset(), which treats null as absent; including the
// key with an empty value would produce a different canonical string and
// every such message would fail to verify.
func TestSNSHandlesNullSubject(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	priv, server := newSNSFixture(t)

	body := signSNS(t, priv, map[string]any{
		"Type":             "Notification",
		"MessageId":        "m-null-subject",
		"TopicArn":         "arn:aws:sns:us-east-1:1234567890:SES_Events",
		"Subject":          nil,
		"Message":          `{"notificationType":"Bounce"}`,
		"Timestamp":        "2024-01-01T00:00:00.000Z",
		"SignatureVersion": "2",
		"SigningCertURL":   certURL,
	})

	v := &SNSVerifier{Client: &http.Client{Transport: server}}
	if _, err := v.Verify(t.Context(), body); err != nil {
		t.Fatalf("a notification with a null Subject failed verification: %v", err)
	}
}

// A Message containing characters that JSON escapes must be signed over its
// decoded form, not its escaped form.
func TestSNSHandlesEscapedCharactersInMessage(t *testing.T) {
	const certURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	priv, server := newSNSFixture(t)

	body := signSNS(t, priv, map[string]any{
		"Type":             "Notification",
		"MessageId":        "m-escapes",
		"TopicArn":         "arn:topic",
		"Message":          "line one\nline two\ttabbed \"quoted\" and \\ backslash",
		"Timestamp":        "2024-01-01T00:00:00.000Z",
		"SignatureVersion": "1",
		"SigningCertURL":   certURL,
	})

	v := &SNSVerifier{Client: &http.Client{Transport: server}}
	if _, err := v.Verify(t.Context(), body); err != nil {
		t.Fatalf("a Message with escaped characters failed verification: %v", err)
	}
}

// A real SendGrid delivery is a batch of heterogeneous events with arbitrary
// custom fields, integer timestamps and array-valued categories.
// Shape taken from django-anymail's captured fixtures.
func TestParseSendGridWebhookOnRealBatch(t *testing.T) {
	const batch = `[
      {"email":"a@example.com","timestamp":1461095246,"smtp-id":"<x@ismtpd0006p1sjc2.sendgrid.net>",
       "sg_event_id":"ZyjAM5rnQmuI1KFInHQ3Nw","sg_message_id":"wrfRRvF7Q0GgwUo2CvDmEA.filter0425p1mdw1.13037.57168B4A1D.0",
       "event":"processed","category":["tag1","tag2"],"custom1":"value1"},
      {"email":"b@example.com","timestamp":1461095248,"event":"bounce","reason":"550 5.1.1 unknown",
       "status":"5.1.1","type":"bounce","bounce_classification":"Invalid Address",
       "sg_message_id":"msg-b","category":"single-string-category"},
      {"email":"c@example.com","timestamp":1461095249,"event":"spamreport","sg_message_id":"msg-c"},
      {"email":"d@example.com","timestamp":1461095250,"event":"open","useragent":"Mozilla/5.0","ip":"1.2.3.4"},
      {"email":"e@example.com","timestamp":1461095251,"event":"some_future_event_type"}
    ]`

	events, err := ParseSendGridWebhook([]byte(batch))
	if err != nil {
		t.Fatalf("ParseSendGridWebhook on a real batch: %v", err)
	}
	// Only the bounce and the spamreport are actionable; the rest are ignored
	// rather than treated as errors.
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (one bounce, one complaint); unknown types must be skipped, not fail", len(events))
	}

	bounce, ok := events[0].(*Bounce)
	if !ok {
		t.Fatalf("events[0] is %T, want *Bounce", events[0])
	}
	if bounce.EmailAddress != "b@example.com" || bounce.Status != "5.1.1" {
		t.Errorf("bounce fields not carried: %+v", bounce)
	}
	if bounce.Timestamp.Unix() != 1461095248 {
		t.Errorf("Timestamp = %v, want unix 1461095248", bounce.Timestamp)
	}

	if _, ok := events[1].(*Complaint); !ok {
		t.Fatalf("events[1] is %T, want *Complaint", events[1])
	}
}

// Mailgun subaccount events carry a parent-signature alongside the normal
// signature. The extra field must not disturb verification of the primary one.
func TestMailgunVerifiesPayloadWithParentSignature(t *testing.T) {
	const key = "primary-signing-key"
	now := nowUnixString()

	body := mailgunBody(t, key, now, "tok")

	// Splice in the extra field a subaccount delivery would carry.
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatal(err)
	}
	obj["parent-signature"] = map[string]string{
		"timestamp": now, "token": "tok", "signature": strings.Repeat("00", 32),
	}
	withParent, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}

	v := MailgunVerifier{SigningKey: key}
	if err := v.Verify(withParent); err != nil {
		t.Errorf("a subaccount payload with parent-signature was rejected: %v", err)
	}
}

// A realistic Mailgun failure event must parse into a Bounce.
func TestParseMailgunWebhookOnRealFailure(t *testing.T) {
	const payload = `{
      "signature":{"timestamp":"1529006854","token":"tok","signature":"sig"},
      "event-data":{
        "event":"failed","severity":"permanent","reason":"suppress-bounce",
        "id":"CPgfbmQMTCKtHW6uIWtuVe","timestamp":1521472262.908181,
        "recipient":"alice@example.com",
        "delivery-status":{"code":605,"message":"Not delivering to previously bounced address","description":"","attempt-no":1},
        "message":{"headers":{"to":"Alice <alice@example.com>","message-id":"20130503192659.13651.20287@example.com","from":"Bob <bob@example.com>","subject":"Test"},"size":867},
        "flags":{"is-authenticated":true,"is-test-mode":false},
        "envelope":{"sender":"bob@example.com","targets":"alice@example.com"}
      }}`

	event, err := ParseMailgunWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseMailgunWebhook on a real failure event: %v", err)
	}
	bounce, ok := event.(*Bounce)
	if !ok {
		t.Fatalf("got %T, want *Bounce", event)
	}
	if bounce.EmailAddress != "alice@example.com" {
		t.Errorf("EmailAddress = %q", bounce.EmailAddress)
	}
	if bounce.Type != BounceHard {
		t.Errorf("a 605 permanent failure should be a hard bounce, got %v", bounce.Type)
	}
	if bounce.OriginalMsgID == "" {
		t.Error("message-id was not carried through")
	}
}

func nowUnixString() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}
