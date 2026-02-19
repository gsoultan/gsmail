package gsmail

import (
	"encoding/json"
	"testing"
)

func TestParseBounce(t *testing.T) {
	raw := []byte(`MIME-Version: 1.0
Content-Type: multipart/report; report-type=delivery-status; boundary="boundary"

--boundary
Content-Type: text/plain

Delivery failed.

--boundary
Content-Type: message/delivery-status

Reporting-MTA: dns; example.com

Final-Recipient: rfc822; failed@example.com
Action: failed
Status: 5.1.1
Diagnostic-Code: smtp; 550 User unknown

--boundary
Content-Type: text/rfc822-headers

To: failed@example.com
From: sender@example.com
Subject: Test
Message-ID: <orig-id@example.com>

--boundary--`)

	email, err := ParseRawEmail(raw)
	if err != nil {
		t.Fatalf("ParseRawEmail failed: %v", err)
	}

	bounce, err := ParseBounce(email)
	if err != nil {
		t.Fatalf("ParseBounce failed: %v", err)
	}

	if bounce.EmailAddress != "failed@example.com" {
		t.Errorf("Expected email failed@example.com, got %s", bounce.EmailAddress)
	}
	if bounce.Status != "5.1.1" {
		t.Errorf("Expected status 5.1.1, got %s", bounce.Status)
	}
	if bounce.Type != BounceHard {
		t.Errorf("Expected hard bounce, got %v", bounce.Type)
	}
	if bounce.OriginalMsgID != "<orig-id@example.com>" {
		t.Errorf("Expected original msg id <orig-id@example.com>, got %s", bounce.OriginalMsgID)
	}
}

func TestParseComplaint(t *testing.T) {
	raw := []byte(`MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary="boundary"

--boundary
Content-Type: text/plain

Spam report.

--boundary
Content-Type: message/feedback-report

Feedback-Type: abuse
User-Agent: SomeAgent/1.0

--boundary
Content-Type: message/rfc822

To: recipient@example.com
From: sender@example.com
Subject: Spammy
Message-ID: <spam-id@example.com>

--boundary--`)

	email, err := ParseRawEmail(raw)
	if err != nil {
		t.Fatalf("ParseRawEmail failed: %v", err)
	}

	complaint, err := ParseComplaint(email)
	if err != nil {
		t.Fatalf("ParseComplaint failed: %v", err)
	}

	if complaint.Type != "abuse" {
		t.Errorf("Expected type abuse, got %s", complaint.Type)
	}
	if complaint.EmailAddress != "recipient@example.com" {
		t.Errorf("Expected email recipient@example.com, got %s", complaint.EmailAddress)
	}
	if complaint.OriginalMsgID != "<spam-id@example.com>" {
		t.Errorf("Expected original msg id <spam-id@example.com>, got %s", complaint.OriginalMsgID)
	}
}

func TestParseSESWebhook(t *testing.T) {
	t.Run("Bounce", func(t *testing.T) {
		payload := []byte(`{
			"notificationType": "Bounce",
			"bounce": {
				"bounceType": "Permanent",
				"bouncedRecipients": [{
					"emailAddress": "bounce@example.com",
					"status": "5.1.1",
					"diagnosticCode": "smtp; 550 5.1.1 User unknown"
				}],
				"timestamp": "2026-02-18T09:13:00Z"
			},
			"mail": {
				"messageId": "ses-id",
				"timestamp": "2026-02-18T09:12:00Z"
			}
		}`)
		res, err := ParseSESWebhook(payload)
		if err != nil {
			t.Fatalf("ParseSESWebhook failed: %v", err)
		}
		bounce, ok := res.(*Bounce)
		if !ok {
			t.Fatal("Expected *Bounce result")
		}
		if bounce.EmailAddress != "bounce@example.com" || bounce.Type != BounceHard {
			t.Errorf("Unexpected bounce data: %+v", bounce)
		}
		if bounce.OriginalMsgID != "ses-id" {
			t.Errorf("Expected original msg id ses-id, got %s", bounce.OriginalMsgID)
		}
	})

	t.Run("Complaint", func(t *testing.T) {
		payload := []byte(`{
			"notificationType": "Complaint",
			"complaint": {
				"complainedRecipients": [{ "emailAddress": "complainer@example.com" }],
				"complaintFeedbackType": "abuse",
				"timestamp": "2026-02-18T09:13:00Z",
				"userAgent": "SomeAgent/1.0"
			},
			"mail": {
				"messageId": "ses-id"
			}
		}`)
		res, err := ParseSESWebhook(payload)
		if err != nil {
			t.Fatalf("ParseSESWebhook failed: %v", err)
		}
		complaint, ok := res.(*Complaint)
		if !ok {
			t.Fatal("Expected *Complaint result")
		}
		if complaint.EmailAddress != "complainer@example.com" || complaint.Type != "abuse" {
			t.Errorf("Unexpected complaint data: %+v", complaint)
		}
	})

	t.Run("WrappedInSNS", func(t *testing.T) {
		inner := `{"notificationType": "Bounce", "bounce": {"bounceType": "Permanent", "bouncedRecipients": [{"emailAddress": "sns@example.com"}]}, "mail": {"messageId": "id"}}`
		sns := map[string]string{
			"Type":    "Notification",
			"Message": inner,
		}
		payload, _ := json.Marshal(sns)
		res, err := ParseSESWebhook(payload)
		if err != nil {
			t.Fatalf("ParseSESWebhook failed: %v", err)
		}
		bounce := res.(*Bounce)
		if bounce.EmailAddress != "sns@example.com" {
			t.Errorf("Expected sns@example.com, got %s", bounce.EmailAddress)
		}
	})
}

func TestParseSendGridWebhook(t *testing.T) {
	payload := []byte(`[
		{
			"event": "bounce",
			"email": "bounce@example.com",
			"sg_message_id": "sg-id",
			"timestamp": 1740000000,
			"status": "5.1.1",
			"reason": "Unknown user"
		},
		{
			"event": "spamreport",
			"email": "spam@example.com",
			"sg_message_id": "sg-id2",
			"timestamp": 1740000001
		}
	]`)
	results, err := ParseSendGridWebhook(payload)
	if err != nil {
		t.Fatalf("ParseSendGridWebhook failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	bounce := results[0].(*Bounce)
	if bounce.EmailAddress != "bounce@example.com" || bounce.Provider != "SendGrid" {
		t.Errorf("Unexpected bounce data: %+v", bounce)
	}
	complaint := results[1].(*Complaint)
	if complaint.EmailAddress != "spam@example.com" || complaint.Provider != "SendGrid" {
		t.Errorf("Unexpected complaint data: %+v", complaint)
	}
}

func TestParseMailgunWebhook(t *testing.T) {
	payload := []byte(`{
		"event-data": {
			"event": "failed",
			"timestamp": 1740000000,
			"recipient": "mg@example.com",
			"message": { "headers": { "message-id": "mg-id" } },
			"delivery-status": { "code": 550, "description": "Hard fail" }
		}
	}`)
	res, err := ParseMailgunWebhook(payload)
	if err != nil {
		t.Fatalf("ParseMailgunWebhook failed: %v", err)
	}
	bounce := res.(*Bounce)
	if bounce.EmailAddress != "mg@example.com" || bounce.Type != BounceHard {
		t.Errorf("Unexpected bounce data: %+v", bounce)
	}
}

func TestParsePostmarkWebhook(t *testing.T) {
	payload := []byte(`{
		"RecordType": "Bounce",
		"Type": "HardBounce",
		"Email": "pm@example.com",
		"MessageID": "pm-id",
		"Description": "Invalid",
		"Details": "Bad user",
		"BouncedAt": "2026-02-18T09:13:00Z"
	}`)
	res, err := ParsePostmarkWebhook(payload)
	if err != nil {
		t.Fatalf("ParsePostmarkWebhook failed: %v", err)
	}
	bounce := res.(*Bounce)
	if bounce.EmailAddress != "pm@example.com" || bounce.Type != BounceHard {
		t.Errorf("Unexpected bounce data: %+v", bounce)
	}
}
