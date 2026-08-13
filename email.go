package gsmail

import (
	"fmt"
	htmltemplate "html/template"
	"text/template"

	"github.com/gsoultan/gsmail/outlook"
)

// Attachment represents an email attachment.
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Data        []byte
}

// Email represents an email message.
//
// One Email describes both a message you are sending and one you have
// received, which means it does not round-trip: rendering a parsed message
// does not reproduce the bytes it was parsed from. ParseRawEmail retains the
// trace headers that record how a message travelled — Received, Return-Path,
// DKIM-Signature, Authentication-Results and the ARC set — so an inbound
// message can be inspected, and BuildMessage drops them again, because a new
// message asserting a delivery chain or a signature it did not earn is forging
// its own provenance. The drop is silent; nothing has gone wrong.
//
// UID and Mailbox are likewise set by receivers and ignored by every Sender,
// and MessageIdentity reads them, so its answer depends on where the Email
// came from.
type Email struct {
	// UID identifies the message on the server it was fetched from. It is set
	// by receivers that expose a stable identifier (IMAP) and is required by
	// the operations that act on an existing message, such as marking it read
	// or moving it.
	//
	// It is meaningless when sending and is ignored by every Sender. Zero
	// means "not from a server, or the server does not provide one".
	UID uint32

	// Mailbox is the folder the message was fetched from. Set by receivers;
	// ignored when sending.
	Mailbox string

	From              string
	To                []string
	Cc                []string
	Bcc               []string
	ReplyTo           string
	Subject           string
	Body              []byte
	HTMLBody          []byte
	Attachments       []Attachment
	OutlookCompatible bool
	// Headers holds additional header fields such as List-Unsubscribe,
	// In-Reply-To, References or any X-* header. Values are sanitised and
	// RFC 2047 encoded when rendered. Header names that the library generates
	// itself (From, To, Cc, Bcc, Reply-To, Subject, MIME-Version, Content-Type,
	// Content-Transfer-Encoding) are ignored.
	Headers map[string]string
	// Envelope overrides who the message is delivered to, without changing who
	// it appears to be addressed to.
	//
	// By default the SMTP recipient list is To + Cc + Bcc, which conflates two
	// separate things: the envelope of RFC 5321, meaning the addresses given in
	// RCPT TO, and the header fields of RFC 5322, meaning what the reader sees.
	// Collapsing them is right for an ordinary send and wrong whenever a
	// message is rendered per recipient — a personalised copy, an open-tracking
	// pixel, a signed unsubscribe link — because a visible Cc list would then
	// deliver another copy to every Cc address for every recipient.
	//
	// When Envelope is non-empty it replaces the derived list entirely, so To,
	// Cc and Bcc affect only the headers. Sending one copy per recipient while
	// still showing the full Cc list therefore becomes:
	//
	//	for _, rcpt := range everyone {
	//		e := base                     // To and Cc describe the whole audience
	//		e.Envelope = []string{rcpt}   // this copy goes to one address
	//		e.Bcc = nil                   // never disclose the blind list
	//		send(e)
	//	}
	//
	// A Bcc address must appear in Envelope to receive anything, and must stay
	// out of Bcc: BuildMessage writes no Bcc header, but a provider API may.
	//
	// Only the SMTP sender honours this. Providers that hand a recipient list to
	// a vendor API cannot separate the two, and reject a message that sets it
	// rather than silently delivering to everyone named in the headers.
	Envelope []string
	// HTMLFuncs holds custom functions for HTML templates used with this email.
	HTMLFuncs htmltemplate.FuncMap
	// TextFuncs holds custom functions for text templates used with this email.
	TextFuncs template.FuncMap
}

// SetHeader sets a custom header field, allocating the map when needed.
func (e *Email) SetHeader(name, value string) {
	if e.Headers == nil {
		e.Headers = make(map[string]string, 4)
	}
	e.Headers[name] = value
}

// S3Config represents the AWS S3 configuration.
type S3Config struct {
	Region    string
	Bucket    string
	Key       string
	Endpoint  string // Optional for S3 compatible services
	AccessKey string
	SecretKey string
}

// SetBody sets the email body using a template and data.
// It automatically detects whether the template is HTML or plaintext and
// stores the result in HTMLBody or Body accordingly.
func (e *Email) SetBody(tmplStr string, data any) error {
	return e.setBodyBytes(unsafeStringToBytes(tmplStr), data)
}

// SetTextBody renders a plaintext template into Body without HTML sniffing.
func (e *Email) SetTextBody(tmplStr string, data any) error {
	body, err := parseTextTemplateWithFuncs(tmplStr, data, e.TextFuncs)
	if err != nil {
		return fmt.Errorf("set text body: %w", err)
	}
	e.Body = body
	return nil
}

// SetHTMLBody renders an HTML template into HTMLBody without HTML sniffing.
func (e *Email) SetHTMLBody(tmplStr string, data any) error {
	body, err := parseHTMLTemplateWithFuncs(tmplStr, data, e.HTMLFuncs)
	if err != nil {
		return fmt.Errorf("set html body: %w", err)
	}
	if e.OutlookCompatible {
		body = outlook.ToOutlookHTML(body)
	}
	e.HTMLBody = body
	return nil
}

// SetOutlookBody sets the email body using a template and data, and converts it to be Outlook-compatible.
func (e *Email) SetOutlookBody(tmplStr string, data any) error {
	e.OutlookCompatible = true
	return e.SetBody(tmplStr, data)
}

// IsOutlookCompatible returns true if the email is marked as Outlook compatible
// or if the body already contains Outlook-specific fixes.
func (e *Email) IsOutlookCompatible() bool {
	if e.OutlookCompatible {
		return true
	}
	return outlook.IsOutlookCompatible(e.Body) || outlook.IsOutlookCompatible(e.HTMLBody)
}

func (e *Email) setBodyBytes(b []byte, data any) error {
	if IsHTML(b) {
		return e.SetHTMLBody(unsafeBytesToString(b), data)
	}
	return e.SetTextBody(unsafeBytesToString(b), data)
}
