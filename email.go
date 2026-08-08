package gsmail

import (
	"fmt"
	htmltemplate "html/template"
	"text/template"
)

// Attachment represents an email attachment.
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Data        []byte
}

// Email represents an email message.
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
		body = ToOutlookHTML(body)
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
	return IsOutlookCompatible(e.Body) || IsOutlookCompatible(e.HTMLBody)
}

func (e *Email) setBodyBytes(b []byte, data any) error {
	if IsHTML(b) {
		return e.SetHTMLBody(unsafeBytesToString(b), data)
	}
	return e.SetTextBody(unsafeBytesToString(b), data)
}
