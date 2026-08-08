package gsmail

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Gmail and Yahoo have required one-click unsubscribe from bulk senders since
// February 2024, and the requirement is for a *pair* of headers. Setting
// List-Unsubscribe on its own -- which is what most code does, and what this
// package previously left callers to do by hand -- does not satisfy it.

// ListUnsubscribePostValue is the fixed value RFC 8058 requires alongside a
// List-Unsubscribe header for one-click unsubscribe.
const ListUnsubscribePostValue = "List-Unsubscribe=One-Click"

// Errors reported when building unsubscribe headers.
var (
	ErrNoUnsubscribeTarget     = errors.New("gsmail: at least one unsubscribe target is required")
	ErrUnsafeUnsubscribeScheme = errors.New(
		"gsmail: unsubscribe targets must be https or mailto")
	ErrNoHTTPSUnsubscribe = errors.New(
		"gsmail: one-click unsubscribe requires an https target")
)

// SetListUnsubscribe sets the List-Unsubscribe header from one or more
// targets, each an https URL or a mailto: address.
//
// It sets List-Unsubscribe only. Use SetOneClickUnsubscribe when the message
// is bulk mail, which is nearly always what you want.
func (e *Email) SetListUnsubscribe(targets ...string) error {
	value, _, err := buildListUnsubscribe(targets)
	if err != nil {
		return err
	}
	e.SetHeader("List-Unsubscribe", value)
	return nil
}

// SetOneClickUnsubscribe sets the header pair RFC 8058 requires:
// List-Unsubscribe with the given targets, and List-Unsubscribe-Post.
//
// At least one target must be an https URL, because one-click unsubscribe
// works by the mailbox provider POSTing to it; a mailto: target alone cannot
// satisfy the requirement and the pair would be invalid. Additional mailto:
// targets are allowed and are kept as a fallback for clients that do not
// implement RFC 8058.
//
// The endpoint must honour a POST with no useful body and must not require the
// recipient to confirm: providers treat a confirmation page as a failure to
// unsubscribe. It must also not be reachable by GET alone, or link-scanning
// security software will unsubscribe your recipients for them.
func (e *Email) SetOneClickUnsubscribe(targets ...string) error {
	value, hasHTTPS, err := buildListUnsubscribe(targets)
	if err != nil {
		return err
	}
	if !hasHTTPS {
		return NonRetryable(ErrNoHTTPSUnsubscribe)
	}

	e.SetHeader("List-Unsubscribe", value)
	e.SetHeader("List-Unsubscribe-Post", ListUnsubscribePostValue)
	return nil
}

// HasOneClickUnsubscribe reports whether the message carries the complete
// header pair. Either header alone does not satisfy RFC 8058.
func (e *Email) HasOneClickUnsubscribe() bool {
	if e.Headers == nil {
		return false
	}
	var unsub, post string
	for name, value := range e.Headers {
		switch strings.ToLower(name) {
		case "list-unsubscribe":
			unsub = value
		case "list-unsubscribe-post":
			post = value
		}
	}
	return unsub != "" && strings.EqualFold(strings.TrimSpace(post), ListUnsubscribePostValue)
}

// buildListUnsubscribe validates targets and renders the header value,
// reporting whether any target was https.
func buildListUnsubscribe(targets []string) (value string, hasHTTPS bool, err error) {
	if len(targets) == 0 {
		return "", false, NonRetryable(ErrNoUnsubscribeTarget)
	}

	parts := make([]string, 0, len(targets))
	for _, raw := range targets {
		t := strings.TrimSpace(raw)
		// Trim angle brackets so callers may pass either form.
		t = strings.TrimPrefix(t, "<")
		t = strings.TrimSuffix(t, ">")
		if t == "" {
			return "", false, NonRetryable(ErrNoUnsubscribeTarget)
		}

		u, parseErr := url.Parse(t)
		if parseErr != nil {
			return "", false, NonRetryable(fmt.Errorf("gsmail: unsubscribe target %q: %w", raw, parseErr))
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			hasHTTPS = true
		case "mailto":
		default:
			// http is excluded deliberately: an unsubscribe endpoint carries a
			// token identifying the recipient, and sending it in clear is a
			// disclosure.
			return "", false, NonRetryable(fmt.Errorf("%w (got %q)", ErrUnsafeUnsubscribeScheme, raw))
		}

		// A CR or LF here would split the header.
		if t != sanitizeHeaderValue(t) {
			return "", false, NonRetryable(
				fmt.Errorf("gsmail: unsubscribe target %q contains an illegal character", raw))
		}

		parts = append(parts, "<"+t+">")
	}

	return strings.Join(parts, ", "), hasHTTPS, nil
}

// RequireOneClickUnsubscribe returns an interceptor that refuses to send a
// message without the RFC 8058 header pair.
//
// Wrap bulk-mail senders in it. Gmail and Yahoo measure compliance across a
// sending domain, so one campaign that forgets the headers degrades the
// reputation of everything else sent from that domain -- failing the send is
// cheaper than finding out from a deliverability dashboard weeks later.
//
// Do not wrap transactional senders: password resets and receipts should not
// carry an unsubscribe header.
func RequireOneClickUnsubscribe() SendInterceptor {
	return func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) error {
		if !email.HasOneClickUnsubscribe() {
			return NonRetryable(fmt.Errorf(
				"gsmail: bulk mail requires List-Unsubscribe and List-Unsubscribe-Post; "+
					"call Email.SetOneClickUnsubscribe: %w", ErrNoHTTPSUnsubscribe))
		}
		return next(ctx, email)
	}
}
