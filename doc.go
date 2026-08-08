/*
Package gsmail sends and receives email.

It provides one interface per direction and several implementations behind
each, so switching from SMTP to a provider API — or sending through one and
failing over to another — does not change the calling code.

# Sending

Build an [Email], pick a [Sender], send it:

	email := gsmail.Email{
		From:    "no-reply@example.com",
		To:      []string{"alice@example.com"},
		Subject: "Welcome",
	}
	email.SetBody("<h1>Hello {{.Name}}</h1>", data)

	sender := smtp.NewSender("smtp.example.com", 587, user, pass, false)
	err := gsmail.Send(ctx, sender, email)

Senders live in subpackages: [gsmail/smtp], [gsmail/ses], [gsmail/sendgrid],
[gsmail/mailgun] and [gsmail/postmark]. Each satisfies [Sender], so they are
interchangeable and composable.

[Email.Body] is text/plain and [Email.HTMLBody] is text/html. [Email.SetBody]
sniffs a template and routes the result to whichever fits; [Email.SetTextBody]
and [Email.SetHTMLBody] skip the sniffing when you already know.

# Receiving

[Receiver] covers IMAP and POP3, in [gsmail/imap] and [gsmail/pop3]. IMAP also
supports selecting a mailbox, searching, IDLE, and acting on fetched messages —
marking them read, moving them, deleting them.

# Interceptors

Cross-cutting behaviour wraps a sender rather than configuring one, so it
composes and applies uniformly to every provider:

	sender = gsmail.WrapSender(sender,
		gsmail.SuppressionInterceptor(list),   // withhold mail from dead addresses
		gsmail.RecoveryInterceptor(),          // turn a panic into an error
		otelgs.SendInterceptor(),              // tracing
	)

The first interceptor is the outermost.

# Retries and error classification

[Retry] backs off exponentially and stops early on a permanent failure, so an
invalid recipient is not offered four times. An error says whether it is worth
repeating: wrap one in [NonRetryable] to mark it permanent, or implement
[Retryable] to decide per error. [HTTPError] does this for provider APIs —
408, 429 and 5xx are transient, everything else is not — and honours a
Retry-After header.

This matters more than it looks. Repeated delivery to an address the receiving
system has already rejected is counted against the sending domain, so getting
the classification wrong is a deliverability problem, not just wasted work.

# Deliverability

[DKIMOptions] signs outbound mail. [Email.SetOneClickUnsubscribe] sets the
RFC 8058 header pair that Gmail and Yahoo require from bulk senders — both
headers, since either alone does not satisfy it. [HealthChecker] reports on a
domain's SPF, DKIM, DMARC and MX records.

Bounces and complaints arrive as provider webhooks. Verify them first —
[SNSVerifier], [SendGridVerifier], [MailgunVerifier] and [PostmarkVerifier] —
because the Parse functions accept unauthenticated input and a forged bounce
suppresses a real customer. Then feed the result to a [Suppressor] so the next
send to that address is withheld.

# Safety

Every header value is sanitised and RFC 2047 encoded, addresses containing a
line break are refused, and attachment file names are quoted with an RFC 2231
form alongside. A subject, recipient or file name taken from untrusted input
cannot inject additional headers. The parsers are fuzzed against these
invariants.

Inbound MIME is bounded on nesting depth, decoded size and encoded size, so a
hostile message cannot exhaust memory.

# Testing

[gsmail/providertest] is a conformance suite for anyone implementing a
[Sender]: it asserts the contract every provider must honour.
[gsmail/gsmailtest] is the other side — test doubles for applications that
send mail, so you can assert on what your code produced without a network.

[gsmail/smtp]: https://pkg.go.dev/github.com/gsoultan/gsmail/smtp
[gsmail/ses]: https://pkg.go.dev/github.com/gsoultan/gsmail/ses
[gsmail/sendgrid]: https://pkg.go.dev/github.com/gsoultan/gsmail/sendgrid
[gsmail/mailgun]: https://pkg.go.dev/github.com/gsoultan/gsmail/mailgun
[gsmail/postmark]: https://pkg.go.dev/github.com/gsoultan/gsmail/postmark
[gsmail/imap]: https://pkg.go.dev/github.com/gsoultan/gsmail/imap
[gsmail/pop3]: https://pkg.go.dev/github.com/gsoultan/gsmail/pop3
[gsmail/providertest]: https://pkg.go.dev/github.com/gsoultan/gsmail/providertest
[gsmail/gsmailtest]: https://pkg.go.dev/github.com/gsoultan/gsmail/gsmailtest
*/
package gsmail
