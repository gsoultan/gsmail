package gsmail

import (
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"net/http"
	"sync"
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

	// s3Clients caches one S3 client per region/endpoint/key combination.
	// Building a client reloads the whole AWS config, which is far too
	// expensive to repeat on every template fetch and every retry.
	s3Clients sync.Map
)

type templateExecutor interface {
	Execute(wr io.Writer, data any) error
}

func executeTemplate(tmpl templateExecutor, data any, name string) ([]byte, error) {
	bufPtr := getBuffer()
	defer putBuffer(bufPtr)

	writer := &bufferWriter{bufPtr: bufPtr}
	if err := tmpl.Execute(writer, data); err != nil {
		return nil, fmt.Errorf("execute %s template: %w", name, err)
	}

	res := make([]byte, len(*bufPtr))
	copy(res, *bufPtr)
	return res, nil
}

// parseHTMLTemplateWithFuncs parses an HTML template with the given data and optional custom functions.
func parseHTMLTemplateWithFuncs(tmplStr string, data any, funcs htmltemplate.FuncMap) ([]byte, error) {
	t := htmltemplate.New("email")
	if funcs != nil {
		t = t.Funcs(funcs)
	}

	tmpl, err := t.Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("parse html template: %w", err)
	}
	return executeTemplate(tmpl, data, "html")
}

// ParseHTMLTemplate parses an HTML template with the given data.
func ParseHTMLTemplate(tmplStr string, data any) ([]byte, error) {
	return parseHTMLTemplateWithFuncs(tmplStr, data, nil)
}

// parseTextTemplateWithFuncs parses a text template with the given data and optional custom functions.
func parseTextTemplateWithFuncs(tmplStr string, data any, funcs template.FuncMap) ([]byte, error) {
	t := template.New("email")
	if funcs != nil {
		t = t.Funcs(funcs)
	}

	tmpl, err := t.Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("parse text template: %w", err)
	}
	return executeTemplate(tmpl, data, "text")
}

// ParseTextTemplate parses a text template with the given data.
func ParseTextTemplate(tmplStr string, data any) ([]byte, error) {
	return parseTextTemplateWithFuncs(tmplStr, data, nil)
}

// MaxTemplateSize bounds how much a remote template may contribute to an
// email body. Inbound MIME parts are bounded too (see ErrPartTooLarge); a
// template fetched over the network deserves the same treatment, so a slow or
// hostile origin cannot drive the process out of memory.
const MaxTemplateSize = 8 << 20

// ErrTemplateTooLarge is returned when a remote template exceeds MaxTemplateSize.
var ErrTemplateTooLarge = errors.New("gsmail: template exceeds maximum size")

func (e *Email) setBodyFromReader(r io.Reader, data any, sourceName string) error {
	bufPtr := getBuffer()
	defer putBuffer(bufPtr)

	n, err := io.Copy(&bufferWriter{bufPtr: bufPtr}, io.LimitReader(r, MaxTemplateSize+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", sourceName, err)
	}
	if n > MaxTemplateSize {
		return NonRetryable(fmt.Errorf("read %s: %w", sourceName, ErrTemplateTooLarge))
	}

	return e.setBodyBytes(*bufPtr, data)
}

// SetBodyFromURL loads a template from an HTTP URL and sets the email body.
func (e *Email) SetBodyFromURL(ctx context.Context, url string, data any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return NonRetryable(fmt.Errorf("create request: %w", err))
	}

	return Retry(ctx, DefaultRetryConfig(), func() error {
		resp, err := httpClient.Do(req.Clone(ctx))
		if err != nil {
			return fmt.Errorf("fetch template from url: %w", err)
		}
		defer DrainAndClose(resp.Body)

		if resp.StatusCode != http.StatusOK {
			return NewHTTPError("template url "+url, resp)
		}

		return e.setBodyFromReader(resp.Body, data, "template body")
	})
}

// SetBodyFromS3 loads a template from an AWS S3 compatible bucket and sets the email body.
//
// AccessKey and SecretKey are only applied when both are set. Leaving them
// empty keeps the default AWS credential chain (IAM role, environment,
// profile) intact; overriding it with empty static credentials would break
// every ambient-credential deployment.
func (e *Email) SetBodyFromS3(ctx context.Context, cfg S3Config, data any) error {
	client, err := s3ClientFor(ctx, cfg)
	if err != nil {
		return err
	}

	return Retry(ctx, DefaultRetryConfig(), func() error {
		resp, err := client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(cfg.Bucket),
			Key:    aws.String(cfg.Key),
		})
		if err != nil {
			return fmt.Errorf("get object from s3: %w", err)
		}
		defer resp.Body.Close()

		return e.setBodyFromReader(resp.Body, data, "s3 object body")
	})
}

// s3ClientFor builds (and caches) an S3 client for the given configuration, so
// that the AWS config is not reloaded on every call and every retry.
func s3ClientFor(ctx context.Context, cfg S3Config) (*s3.Client, error) {
	key := cfg.Region + "\x00" + cfg.Endpoint + "\x00" + cfg.AccessKey

	if c, ok := s3Clients.Load(key); ok {
		return c.(*s3.Client), nil
	}

	opts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})

	actual, _ := s3Clients.LoadOrStore(key, client)
	return actual.(*s3.Client), nil
}
