package gsmail

import (
	"fmt"
)

// Attachment represents an email attachment.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Email represents an email message.
type Email struct {
	From              string
	To                []string
	Subject           string
	Body              []byte
	Attachments       []Attachment
	OutlookCompatible bool
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
// It automatically detects if the template is HTML or plaintext.
func (e *Email) SetBody(tmplStr string, data any) error {
	return e.setBodyBytes(UnsafeStringToBytes(tmplStr), data)
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
	return IsOutlookCompatible(e.Body)
}

func (e *Email) setBodyBytes(b []byte, data any) error {
	var err error
	if IsHTML(b) {
		e.Body, err = ParseHTMLTemplate(UnsafeBytesToString(b), data)
		if err == nil && e.OutlookCompatible {
			e.Body = ToOutlookHTML(e.Body)
		}
	} else {
		e.Body, err = ParseTextTemplate(UnsafeBytesToString(b), data)
	}
	if err != nil {
		return fmt.Errorf("set body: %w", err)
	}
	return nil
}
