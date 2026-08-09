# Security policy

## Reporting a vulnerability

Please report security issues privately through
[GitHub's private vulnerability reporting](https://github.com/gsoultan/gsmail/security/advisories/new)
rather than opening a public issue.

Include what you can: the affected version, a description, and ideally a
message or payload that reproduces it. A failing Go test is ideal but not
required.

You should get an acknowledgement within a few days. If a fix is warranted it
will ship as a patch release with an advisory, and you will be credited unless
you would rather not be.

## Supported versions

The module is pre-1.0. Fixes land on the latest minor; there are no backports.
Upgrade to the newest tag before reporting.

## What this library defends against

Knowing where the boundaries are makes a report easier to judge.

**Header injection.** Every header value is sanitised — CR, LF, the remaining
C0 controls and DEL are stripped — and non-ASCII is RFC 2047 encoded. An
address containing a line break is dropped rather than escaped. Attachment
file names are quoted with an RFC 2231 form alongside. Values parsed from an
inbound message are sanitised at the parse boundary, not only on the way out.

**Untrusted MIME.** Inbound parsing is bounded on nesting depth, decoded part
size and encoded part size, so a hostile message cannot exhaust memory. These
paths are fuzzed against those invariants.

**Generated HTML.** The `outlook` package escapes text and attribute
parameters and restricts URL schemes to `http`, `https`, `mailto`, `tel` and
`cid`. Its output becomes the *template source* for `SetHTMLBody`, where
`html/template`'s contextual escaping does not apply, so escaping happens in
the builders. Parameters documented as HTML fragments are trusted by design —
escape your own data before passing it to one.

**Webhooks.** `SNSVerifier`, `SendGridVerifier`, `MailgunVerifier` and
`PostmarkVerifier` authenticate provider callbacks. The `Parse*Webhook`
functions deliberately do not: verify first, then parse.

**Credentials.** OAuth authenticators refuse to send a bearer token over a
connection without TLS. The minimum TLS version is 1.2.

## What it does not defend against

- **Content you pass in.** A template, an HTML fragment argument, or a body is
  emitted as given. Escaping user data before it reaches the library is the
  caller's job.
- **Delivery guarantees.** `BackgroundSender` is in-memory; a crash loses its
  queue.
- **`InsecureSkipVerify`.** Setting it disables certificate verification, as
  documented.
- **Anything reachable only with the caller's own credentials**, such as an
  operator configuring a hostile SMTP relay.
