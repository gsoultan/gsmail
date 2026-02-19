package gsmail

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"strings"
	"time"
)

// BounceType represents the type of bounce.
type BounceType string

const (
	// BounceHard indicates a permanent delivery failure.
	BounceHard BounceType = "Hard"
	// BounceSoft indicates a temporary delivery failure.
	BounceSoft BounceType = "Soft"
)

// Bounce represents a delivery failure event.
type Bounce struct {
	Type          BounceType `json:"type"`
	EmailAddress  string     `json:"email_address"`
	Reason        string     `json:"reason"`
	Status        string     `json:"status"` // e.g. "5.1.1"
	Timestamp     time.Time  `json:"timestamp"`
	OriginalMsgID string     `json:"original_msg_id"`
	Provider      string     `json:"provider,omitempty"`
}

// Complaint represents a spam complaint event.
type Complaint struct {
	EmailAddress  string    `json:"email_address"`
	Type          string    `json:"type"` // e.g. "abuse"
	Timestamp     time.Time `json:"timestamp"`
	OriginalMsgID string    `json:"original_msg_id"`
	UserAgent     string    `json:"user_agent"`
	Provider      string    `json:"provider,omitempty"`
}

// ParseBounce attempts to extract bounce information from an email.
// It looks for "message/delivery-status" parts according to RFC 3464.
func ParseBounce(email Email) (*Bounce, error) {
	for _, att := range email.Attachments {
		if strings.Contains(strings.ToLower(att.ContentType), "message/delivery-status") {
			return parseDSN(att.Data, email)
		}
	}
	return nil, fmt.Errorf("no delivery-status part found")
}

// ParseComplaint attempts to extract complaint information from an email.
// It looks for "message/feedback-report" parts according to RFC 5965 (ARF).
func ParseComplaint(email Email) (*Complaint, error) {
	for _, att := range email.Attachments {
		if strings.Contains(strings.ToLower(att.ContentType), "message/feedback-report") {
			return parseARF(att.Data, email)
		}
	}
	return nil, fmt.Errorf("no feedback-report part found")
}

func parseDSN(data []byte, email Email) (*Bounce, error) {
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(data)))

	// First section: per-message fields
	_, err := reader.ReadMIMEHeader()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read DSN message headers: %w", err)
	}

	// Second section: per-recipient fields
	headers, err := reader.ReadMIMEHeader()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read DSN recipient headers: %w", err)
	}

	if headers == nil {
		return nil, fmt.Errorf("invalid DSN format: missing recipient section")
	}

	recipient := headers.Get("Final-Recipient")
	if recipient != "" {
		if parts := strings.Split(recipient, ";"); len(parts) > 1 {
			recipient = strings.TrimSpace(parts[1])
		}
	}

	status := headers.Get("Status")
	diagnostic := headers.Get("Diagnostic-Code")

	bounce := &Bounce{
		EmailAddress: recipient,
		Status:       status,
		Reason:       diagnostic,
		Timestamp:    time.Now(),
	}

	if strings.HasPrefix(status, "5") {
		bounce.Type = BounceHard
	} else {
		bounce.Type = BounceSoft
	}

	// Extract Original Message ID if available
	bounce.OriginalMsgID = findOriginalMsgID(email)

	return bounce, nil
}

func parseARF(data []byte, email Email) (*Complaint, error) {
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(data)))
	headers, err := reader.ReadMIMEHeader()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read ARF headers: %w", err)
	}

	complaint := &Complaint{
		Type:      headers.Get("Feedback-Type"),
		UserAgent: headers.Get("User-Agent"),
		Timestamp: time.Now(),
	}

	// Try to find original message ID and recipient
	for _, att := range email.Attachments {
		if isRFC822(att.ContentType) {
			origReader := textproto.NewReader(bufio.NewReader(bytes.NewReader(att.Data)))
			origHeaders, _ := origReader.ReadMIMEHeader()
			if origHeaders != nil {
				complaint.OriginalMsgID = origHeaders.Get("Message-ID")
				complaint.EmailAddress = origHeaders.Get("To")
			}
			break
		}
	}

	return complaint, nil
}

func findOriginalMsgID(email Email) string {
	for _, att := range email.Attachments {
		if isRFC822(att.ContentType) {
			origReader := textproto.NewReader(bufio.NewReader(bytes.NewReader(att.Data)))
			origHeaders, _ := origReader.ReadMIMEHeader()
			if origHeaders != nil {
				return origHeaders.Get("Message-ID")
			}
		}
	}
	return ""
}

func isRFC822(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "message/rfc822") || strings.Contains(ct, "text/rfc822-headers")
}

// --- Provider Webhook Handlers ---

// SESNotification represents a simplified AWS SES notification structure.
type SESNotification struct {
	NotificationType string `json:"notificationType"`
	Bounce           *struct {
		BounceType        string `json:"bounceType"`
		BounceSubType     string `json:"bounceSubType"`
		BouncedRecipients []struct {
			EmailAddress   string `json:"emailAddress"`
			Status         string `json:"status"`
			DiagnosticCode string `json:"diagnosticCode"`
		} `json:"bouncedRecipients"`
		Timestamp string `json:"timestamp"`
	} `json:"bounce,omitempty"`
	Complaint *struct {
		ComplainedRecipients []struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"complainedRecipients"`
		ComplaintFeedbackType string `json:"complaintFeedbackType"`
		Timestamp             string `json:"timestamp"`
		UserAgent             string `json:"userAgent"`
	} `json:"complaint,omitempty"`
	Mail struct {
		MessageID string `json:"messageId"`
		Timestamp string `json:"timestamp"`
	} `json:"mail"`
}

// ParseSESWebhook parses an AWS SES notification payload (can be wrapped in SNS).
func ParseSESWebhook(data []byte) (any, error) {
	// SNS messages have Type and Message fields
	var sns struct {
		Type    string `json:"Type"`
		Message string `json:"Message"`
	}
	if err := json.Unmarshal(data, &sns); err == nil && sns.Type == "Notification" && sns.Message != "" {
		data = []byte(sns.Message)
	}

	var ses SESNotification
	if err := json.Unmarshal(data, &ses); err != nil {
		return nil, err
	}

	switch ses.NotificationType {
	case "Bounce":
		if ses.Bounce == nil || len(ses.Bounce.BouncedRecipients) == 0 {
			return nil, fmt.Errorf("invalid SES bounce notification")
		}
		r := ses.Bounce.BouncedRecipients[0]
		b := &Bounce{
			EmailAddress:  r.EmailAddress,
			Status:        r.Status,
			Reason:        r.DiagnosticCode,
			OriginalMsgID: ses.Mail.MessageID,
			Provider:      "AWS SES",
		}
		if ses.Bounce.BounceType == "Permanent" {
			b.Type = BounceHard
		} else {
			b.Type = BounceSoft
		}
		if t, err := time.Parse(time.RFC3339, ses.Bounce.Timestamp); err == nil {
			b.Timestamp = t
		}
		return b, nil

	case "Complaint":
		if ses.Complaint == nil || len(ses.Complaint.ComplainedRecipients) == 0 {
			return nil, fmt.Errorf("invalid SES complaint notification")
		}
		c := &Complaint{
			EmailAddress:  ses.Complaint.ComplainedRecipients[0].EmailAddress,
			Type:          ses.Complaint.ComplaintFeedbackType,
			OriginalMsgID: ses.Mail.MessageID,
			UserAgent:     ses.Complaint.UserAgent,
			Provider:      "AWS SES",
		}
		if t, err := time.Parse(time.RFC3339, ses.Complaint.Timestamp); err == nil {
			c.Timestamp = t
		}
		return c, nil
	}

	return nil, fmt.Errorf("unsupported SES notification type: %s", ses.NotificationType)
}

// ParseSendGridWebhook parses a SendGrid event webhook payload.
// SendGrid sends an array of events.
func ParseSendGridWebhook(data []byte) ([]any, error) {
	var events []map[string]any
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, err
	}

	var results []any
	for _, event := range events {
		eventType, _ := event["event"].(string)
		email, _ := event["email"].(string)
		msgID, _ := event["sg_message_id"].(string)
		timestamp, _ := event["timestamp"].(float64)
		ts := time.Unix(int64(timestamp), 0)

		switch eventType {
		case "bounce":
			reason, _ := event["reason"].(string)
			status, _ := event["status"].(string)
			b := &Bounce{
				Type:          BounceHard,
				EmailAddress:  email,
				Reason:        reason,
				Status:        status,
				Timestamp:     ts,
				OriginalMsgID: msgID,
				Provider:      "SendGrid",
			}
			results = append(results, b)
		case "spamreport":
			c := &Complaint{
				EmailAddress:  email,
				Type:          "abuse",
				Timestamp:     ts,
				OriginalMsgID: msgID,
				Provider:      "SendGrid",
			}
			results = append(results, c)
		}
	}
	return results, nil
}

// ParseMailgunWebhook parses a Mailgun webhook payload.
func ParseMailgunWebhook(data []byte) (any, error) {
	var payload struct {
		EventData struct {
			Event     string  `json:"event"`
			Timestamp float64 `json:"timestamp"`
			Recipient string  `json:"recipient"`
			Message   struct {
				Headers struct {
					MessageID string `json:"message-id"`
				} `json:"headers"`
			} `json:"message"`
			DeliveryStatus struct {
				Code        int    `json:"code"`
				Description string `json:"description"`
				Message     string `json:"message"`
			} `json:"delivery-status"`
		} `json:"event-data"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}

	ts := time.Unix(int64(payload.EventData.Timestamp), 0)
	msgID := payload.EventData.Message.Headers.MessageID

	switch payload.EventData.Event {
	case "failed":
		b := &Bounce{
			EmailAddress:  payload.EventData.Recipient,
			Reason:        payload.EventData.DeliveryStatus.Description,
			Status:        fmt.Sprintf("%d", payload.EventData.DeliveryStatus.Code),
			Timestamp:     ts,
			OriginalMsgID: msgID,
			Provider:      "Mailgun",
		}
		if payload.EventData.DeliveryStatus.Code >= 500 {
			b.Type = BounceHard
		} else {
			b.Type = BounceSoft
		}
		return b, nil
	case "complained":
		c := &Complaint{
			EmailAddress:  payload.EventData.Recipient,
			Type:          "abuse",
			Timestamp:     ts,
			OriginalMsgID: msgID,
			Provider:      "Mailgun",
		}
		return c, nil
	}

	return nil, fmt.Errorf("unsupported Mailgun event: %s", payload.EventData.Event)
}

// ParsePostmarkWebhook parses a Postmark webhook payload.
func ParsePostmarkWebhook(data []byte) (any, error) {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}

	recordType, _ := payload["RecordType"].(string)
	email, _ := payload["Email"].(string)
	msgID, _ := payload["MessageID"].(string)

	switch recordType {
	case "Bounce":
		bounceType, _ := payload["Type"].(string)
		description, _ := payload["Description"].(string)
		details, _ := payload["Details"].(string)
		b := &Bounce{
			EmailAddress:  email,
			Reason:        details,
			Status:        description,
			OriginalMsgID: msgID,
			Provider:      "Postmark",
		}
		if bounceType == "HardBounce" {
			b.Type = BounceHard
		} else {
			b.Type = BounceSoft
		}
		if ts, ok := payload["BouncedAt"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				b.Timestamp = t
			}
		}
		return b, nil
	case "SpamComplaint":
		c := &Complaint{
			EmailAddress:  email,
			Type:          "abuse",
			OriginalMsgID: msgID,
			Provider:      "Postmark",
		}
		return c, nil
	}

	return nil, fmt.Errorf("unsupported Postmark record type: %s", recordType)
}
