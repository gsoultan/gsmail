package gsmail

import (
	"context"
	"fmt"
	htmltemplate "html/template"
	"io"
	"net/http"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var (
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
	}
)

type templateExecutor interface {
	Execute(wr io.Writer, data any) error
}

func executeTemplate(tmpl templateExecutor, data any, name string) ([]byte, error) {
	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	writer := &BufferWriter{bufPtr: bufPtr}
	if err := tmpl.Execute(writer, data); err != nil {
		return nil, fmt.Errorf("execute %s template: %w", name, err)
	}

	res := make([]byte, len(*bufPtr))
	copy(res, *bufPtr)
	return res, nil
}

// ParseHTMLTemplate parses an HTML template with the given data.
func ParseHTMLTemplate(tmplStr string, data any) ([]byte, error) {
	tmpl, err := htmltemplate.New("email").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("parse html template: %w", err)
	}
	return executeTemplate(tmpl, data, "html")
}

// ParseTextTemplate parses a text template with the given data.
func ParseTextTemplate(tmplStr string, data any) ([]byte, error) {
	tmpl, err := template.New("email").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("parse text template: %w", err)
	}
	return executeTemplate(tmpl, data, "text")
}

func (e *Email) setBodyFromReader(r io.Reader, data any, sourceName string) error {
	bufPtr := GetBuffer()
	defer PutBuffer(bufPtr)

	if _, err := io.Copy(&BufferWriter{bufPtr: bufPtr}, r); err != nil {
		return fmt.Errorf("read %s: %w", sourceName, err)
	}

	return e.setBodyBytes(*bufPtr, data)
}

// SetBodyFromURL loads a template from an HTTP URL and sets the email body.
func (e *Email) SetBodyFromURL(ctx context.Context, url string, data any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch template from url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch template from %s: status %d (%s)", url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	return e.setBodyFromReader(resp.Body, data, "template body")
}

// SetBodyFromS3 loads a template from an AWS S3 compatible bucket and sets the email body.
func (e *Email) SetBodyFromS3(ctx context.Context, cfg S3Config, data any) error {
	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})

	resp, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(cfg.Key),
	})
	if err != nil {
		return fmt.Errorf("get object from s3: %w", err)
	}
	defer resp.Body.Close()

	return e.setBodyFromReader(resp.Body, data, "s3 object body")
}
