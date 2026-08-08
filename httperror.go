package gsmail

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxErrorBodyLen bounds how much of a provider error response is kept, so a
// misbehaving API cannot produce multi-megabyte error strings.
const maxErrorBodyLen = 4096

// HTTPError describes a failed HTTP call to a provider API. It classifies
// itself for Retry: 408, 429 and 5xx are transient, everything else is
// permanent, so an invalid recipient is not sent four times.
type HTTPError struct {
	// Provider names the API that failed, e.g. "sendgrid".
	Provider string
	// StatusCode is the HTTP status code returned by the API.
	StatusCode int
	// Body is the (truncated) response body, useful for debugging.
	Body string
	// Delay is the server-requested wait taken from a Retry-After header.
	Delay time.Duration
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s: unexpected status %d (%s)", e.Provider, e.StatusCode, http.StatusText(e.StatusCode))
	}
	return fmt.Sprintf("%s: unexpected status %d (%s): %s", e.Provider, e.StatusCode, http.StatusText(e.StatusCode), e.Body)
}

// Retryable reports whether the request may be repeated.
func (e *HTTPError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return e.StatusCode >= 500
}

// RetryAfter returns the server-requested delay, if the response carried one.
func (e *HTTPError) RetryAfter() time.Duration { return e.Delay }

// NewHTTPError builds an HTTPError from a failed response. It reads a bounded
// amount of the body and leaves the body drained so the connection can be
// reused, but does not close it: that remains the caller's responsibility.
func NewHTTPError(provider string, resp *http.Response) *HTTPError {
	e := &HTTPError{
		Provider:   provider,
		StatusCode: resp.StatusCode,
		Delay:      ParseRetryAfter(resp.Header.Get("Retry-After")),
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyLen+1))
	if len(body) > maxErrorBodyLen {
		body = append(body[:maxErrorBodyLen], "..."...)
	}
	e.Body = strings.TrimSpace(string(body))

	// Consume whatever is left so the connection can go back to the pool.
	_, _ = io.Copy(io.Discard, resp.Body)
	return e
}

// maxDrainLen bounds how much of an unread response body is consumed in order
// to make the connection reusable. It is far larger than maxErrorBodyLen: that
// one bounds what we *keep*, this one bounds what we are willing to *skip*.
// Draining only 4 KiB would leave anything bigger unread, which is precisely
// the case where net/http gives up on reuse.
const maxDrainLen = 1 << 20

// DrainAndClose consumes and closes an HTTP response body. Without draining,
// net/http cannot reuse the connection, which hurts throughput under load.
// A body larger than maxDrainLen is abandoned rather than read to the end;
// the connection is then closed instead of reused, which is the right trade
// for a response that size.
func DrainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrainLen))
	_ = body.Close()
}

// ParseRetryAfter parses an HTTP Retry-After header, which is either a number
// of seconds or an HTTP date. It returns 0 when the value is absent or
// unparseable.
func ParseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
