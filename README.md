# gsmail

`gsmail` is a high-performance, zero-allocation oriented Golang email library. It provides a clean, unified interface for sending and receiving emails, with built-in support for dynamic templates and automatic HTML detection.

## Features

- **Pluggable Senders**: Send emails via standard SMTP (with SSL/TLS), AWS SES, or API-based providers (SendGrid, Mailgun, Postmark).
- **Modern Authentication**: Support for XOAUTH2 and OAUTHBEARER for SMTP, IMAP, and POP3.
- **Deliverability**: Built-in DKIM signing support.
- **Middleware & Interceptors**: Custom logic for logging, recovery, and observability.
- **OpenTelemetry Support**: Native tracing for sending and receiving.
- **Advanced IMAP**: Support for searching emails and real-time IDLE notifications.
- **Background Sending**: In-memory worker pool for non-blocking email delivery.
- **Domain Health Utilities**: Diagnostic tools for SPF, DKIM, DMARC, and MX records.
- **Bounce & Complaint Handling**: Parse DSN/ARF emails and provider webhooks (SES, SendGrid, etc.).
- **Advanced SMTP Connection Pooling**: Reuse connections with a configurable `Wait` mechanism, `MaxLifetime`, and observability (`PoolStats`).
- **Pluggable Receivers**: Receive emails via POP3 and IMAP.
- **Dynamic Templates**: Built-in support for `text/template` and `html/template` with custom template functions.
- **Flexible Template Loading**: Load templates from strings, HTTP URLs, or AWS S3 compatible storage.
- **Automatic Content Type Detection**: Automatically detects if an email is HTML or Plaintext based on the content.
- **Email Validation**: `IsValidEmail` (regex), `IsDisposableEmail`, and a configurable `Validator` for MX and mailbox checks.
- **Attachments Support**: Send and receive multiple attachments with automatic MIME encoding/decoding.
- **Outlook Compatibility**: Convert HTML templates to be compatible with Microsoft Outlook with a single flag.
- **Zero-Allocation Focus**: Optimized hot paths and `sync.Pool` utilization to minimize heap allocations and pressure on the GC.
- **Modern AWS SDK**: Uses AWS SDK for Go v2.
- **Context Awareness**: Full support for `context.Context` for timeouts and cancellation.
- **Connection Health Check**: Easily verify connectivity to providers using the `Ping` method.
- **Examples Included**: See the `examples/` directory for complete usage scenarios, including a dedicated [SMTP example](examples/smtp/main.go) for high-concurrency sending.

## Installation

```bash
go get github.com/gsoultan/gsmail
```

## Quick Start

### Basic SMTP Usage

```go
import (
    "context"
    "github.com/gsoultan/gsmail"
    "github.com/gsoultan/gsmail/smtp"
)

func main() {
    // 1. Create an email
    email := gsmail.Email{
        From:    "sender@example.com",
        To:      []string{"receiver@example.com"},
        Subject: "Hello from gsmail",
    }

    // 2. Set body with template. SetBody sniffs the rendered output and
    //    stores it in HTMLBody (HTML) or Body (plaintext). Call SetHTMLBody
    //    or SetTextBody to choose explicitly.
    data := map[string]string{"Name": "Developer"}
    email.SetBody("<h1>Hello {{.Name}}!</h1>", data) // -> email.HTMLBody

    // Optional: custom headers reach every provider, not just SMTP.
    email.SetHeader("List-Unsubscribe", "<https://example.com/unsubscribe>")

    // 3. Choose a sender and send
    sender := smtp.NewSender("smtp.example.com", 587, "user", "pass", false)
    err := gsmail.Send(context.Background(), sender, email)
    if err != nil {
        panic(err)
    }
}
```

### AWS SES Usage

```go
import (
    "context"
    "github.com/gsoultan/gsmail"
    "github.com/gsoultan/gsmail/ses"
)

func main() {
    // ... email creation from previous example

    // Choose AWS SES sender
    sender := ses.NewSender("us-east-1", "ACCESS_KEY", "SECRET_KEY", "")
    err := gsmail.Send(context.Background(), sender, email)
    if err != nil {
        panic(err)
    }
}
```

### Sending with Attachments

```go
// ... assuming email, pdfBytes are defined
email.Attachments = []gsmail.Attachment{
    {
        Filename:    "report.pdf",
        ContentType: "application/pdf",
        Data:        pdfBytes,
    },
}
```

## Advanced Features

### Loading Templates from URL

```go
// ... assuming email, data are defined
err := email.SetBodyFromURL(context.Background(), "https://example.com/templates/welcome.html", data)
```

### Loading Templates from S3

```go
// ... assuming email, data are defined
s3Cfg := gsmail.S3Config{
    Region:    "us-east-1",
    Bucket:    "my-templates",
    Key:       "monthly-report.tmpl",
    AccessKey: "S3_ACCESS_KEY",
    SecretKey: "S3_SECRET_KEY",
}
err := email.SetBodyFromS3(context.Background(), s3Cfg, data)
```

### Custom Template Functions

You can register custom functions for use in templates by setting `HTMLFuncs` (for HTML templates) or `TextFuncs` (for plaintext templates) on the email before calling `SetBody`, `SetOutlookBody`, `SetBodyFromURL`, or `SetBodyFromS3`:

```go
import htmltemplate "html/template"

email := &gsmail.Email{
    From:    "no-reply@example.com",
    To:      []string{"user@example.com"},
    Subject: "Document Request",
}

// Register custom functions (e.g. Add for 1-based list numbering)
email.HTMLFuncs = htmltemplate.FuncMap{
    "Add": func(i int) int { return i + 1 },
}

data := map[string]interface{}{
    "JobTitle":      "Software Engineer",
    "ApplicantName": "John Doe",
    "DocumentTypes": []struct{ Name string }{{Name: "Resume"}, {Name: "Portfolio"}},
}
err := email.SetBody(htmlTemplateString, data)
```

In your template you can now use `{{Add $index}}` (or any other custom function):

```html
{{range $index, $val := .DocumentTypes}}
<p>{{Add $index}}. {{$val.Name}}</p>
{{end}}
```

### TLS Configuration (Cipher Suites and Versions)

**The defaults are already the right choice.** SMTP and IMAP negotiate TLS 1.2 or
better and use Go's own cipher suite list, which is maintained upstream and
tracks current guidance. You only need this section if a compliance regime
forces something narrower.

```go
import (
    "crypto/tls"
    "github.com/gsoultan/gsmail/smtp"
    "github.com/gsoultan/gsmail/imap"
)

// Pin to TLS 1.2 exactly, with a restricted suite list.
sender := smtp.NewSender("smtp.example.com", 587, "user", "pass", false)
sender.MinVersion = tls.VersionTLS12
sender.MaxVersion = tls.VersionTLS12 // optional: disable TLS 1.3
sender.CipherSuites = []uint16{
    tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
    tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
}

// IMAP takes the same options.
receiver := imap.NewReceiver("imap.example.com", 993, "user", "pass", true)
receiver.MinVersion = tls.VersionTLS12
```

- `MinVersion`: 0 uses `DefaultMinTLSVersion` (TLS 1.2). TLS 1.0 and 1.1 are
  deprecated by RFC 8996 — setting them is a downgrade, not a hardening step.
- `MaxVersion`: e.g. `tls.VersionTLS12` to disable TLS 1.3; 0 means no limit.
- `CipherSuites`: nil (the default) defers to the Go standard library. Go only
  honours this for TLS 1.2 and below; **TLS 1.3 suites are not configurable**.
  Setting it by hand pins a list that will go stale — prefer nil.
- `InsecureSkipVerify`: disables certificate verification. Test relays only.

### Webhook signature verification

`ParseSESWebhook`, `ParseSendGridWebhook`, `ParseMailgunWebhook` and
`ParsePostmarkWebhook` parse **unauthenticated** input. Anyone who can reach
your endpoint can forge a hard bounce and get a real customer suppressed.
Authenticate the request first:

```go
// Mailgun — HMAC-SHA256 over timestamp + token.
mg := gsmail.MailgunVerifier{SigningKey: os.Getenv("MAILGUN_SIGNING_KEY")}
if err := mg.Verify(body); err != nil {
    http.Error(w, "forbidden", http.StatusForbidden)
    return
}
event, err := gsmail.ParseMailgunWebhook(body)

// SendGrid — ECDSA P-256 over timestamp + raw body. Pass the body as received;
// re-marshalling the JSON changes the bytes that were signed.
sg := &gsmail.SendGridVerifier{PublicKey: os.Getenv("SENDGRID_WEBHOOK_KEY")}
if err := sg.Verify(r.Header, body); err != nil { /* reject */ }

// Postmark — HTTP Basic credentials embedded in the webhook URL you register.
pm := gsmail.PostmarkVerifier{Username: "hook", Password: os.Getenv("POSTMARK_HOOK_PW")}
if err := pm.Verify(r.Header); err != nil { /* reject */ }

// SES via SNS — verifies the signature against the AWS signing certificate.
// Always set TopicARNs: a valid signature proves the message came from SNS,
// not that it came from *your* topic.
sns := &gsmail.SNSVerifier{TopicARNs: []string{os.Getenv("SES_TOPIC_ARN")}}
msg, err := sns.Verify(ctx, body)
if err != nil { /* reject */ }
event, err := gsmail.ParseSESWebhook([]byte(msg.Message))
```

All verifiers reject timestamps outside `DefaultWebhookTolerance` (5 minutes);
override it per verifier with `Tolerance`. Mailgun's token is single-use, so
also record tokens you have already accepted if you need complete replay
protection.

### Receiving Emails (IMAP)

```go
import (
    "context"
    "fmt"
    "github.com/gsoultan/gsmail/imap"
)

func main() {
    receiver := imap.NewReceiver("imap.example.com", 993, "user", "pass", true)
    emails, err := receiver.Receive(context.Background(), 10)
    if err != nil {
        panic(err)
    }
    for _, email := range emails {
        fmt.Printf("From: %s, Subject: %s\n", email.From, email.Subject)
    }
}
```

### Receiving Emails (POP3)

```go
import (
    "context"
    "github.com/gsoultan/gsmail/pop3"
)

func main() {
    receiver := pop3.NewReceiver("pop.example.com", 995, "user", "pass", true)
    emails, err := receiver.Receive(context.Background(), 10)
    if err != nil {
        panic(err)
    }
    // ... process emails
}
```

### Email Validation

```go
// Basic regex check
isValid := gsmail.IsValidEmail("test@example.com")

// Check if email is from a disposable/temporary provider
isDisposable := gsmail.IsDisposableEmail("test@temp-mail.com")

// Offline checks only: syntax plus the disposable-domain list.
err := gsmail.ValidateEmailSyntax("test@example.com")

// Network checks are opt-in, and CheckMailbox needs care: outbound port 25 is
// blocked by most cloud providers, many operators treat probing as abuse, and
// a catch-all domain accepts every recipient so a "valid" result means nothing.
v := gsmail.Validator{CheckMX: true}
err = v.Validate(context.Background(), "test@example.com")

// Sender and Receiver also expose Validate, which is the offline check.
err = sender.Validate(context.Background(), "test@example.com")
```

### Outlook Compatibility

Outlook uses Word for rendering, which has limited HTML/CSS support. You can enable Outlook compatibility mode to automatically inject necessary fixes (namespaces, MSO settings, meta tags, and CSS resets):

```go
email := gsmail.Email{
    OutlookCompatible: true,
}
// All subsequent SetBody calls will now apply Outlook fixes
email.SetBody("<html>...</html>", data)

// Or use the shortcut method which sets the flag for you
email.SetOutlookBody("<html>...</html>", data)
```

### Outlook Compatibility Helpers

In addition to the automatic flag, `gsmail` provides helper functions to handle common Outlook layout issues:

- `WrapInGhostTable(html, width, align)`: Wraps content in a conditional MSO table to enforce widths.
- `MSOOnly(html)`: Content visible only in Outlook.
- `HideFromMSO(html)`: Content hidden from Outlook.
- `MSOButton(cfg)`: Generates a "bulletproof" VML button.
- `MSOTable(width, align, style, content)`: Generates a normalized table with standard Outlook fixes.
- `MSOSpacer(height)`: Generates a spacer row for consistent vertical spacing.
- `MSOImage(src, alt, width, height, style)`: Generates an image with Outlook fixes.
- `MSOFontStack(fonts...)`: Returns a font stack with proper quoting for Outlook.
- `MSOBackground(url, color, w, h, content)`: Generates a VML-based background for Outlook.
- `MSOColumns(widths, cols...)`: Responsive side-by-side columns using ghost tables.
- `MSOBulletList(items, bullet, style)`: Consistent bulleted lists (avoids Outlook <ul> issues).
- `MSOPreheader(text)`: Hidden preheader text for inbox preview (place at top of body).
- `MSOPreheaderTruncated(text, maxLen)`: Preheader with truncation; `MSOPreheaderMaxLength` (130) is a common maxLen.
- `MSOEmoji(text)`: Wraps emoji in a span with Segoe UI Emoji font for Outlook 2016+.
- `MSOSafeFontStack()`: Returns an Outlook-safe font stack (Arial, Helvetica, sans-serif).
- `MSOEmailLayout(width, preheader, header, body, footer)`: Builds a standard email structure with ghost table.
- `IsOutlookCompatible(html)`: Detects if HTML contains Outlook-specific fixes.

These live in `github.com/gsoultan/gsmail/outlook`. The deprecated aliases in
the root package have been removed — change the import and drop the `gsmail.`
prefix:

```go
import "github.com/gsoultan/gsmail/outlook"

btn := outlook.MSOButton(outlook.ButtonConfig{Text: "Confirm", Link: "https://example.com/confirm"})
```

#### What these helpers escape

Each helper takes two kinds of parameter and treats them differently:

- **Text, attribute and URL parameters are untrusted.** Button labels, preheader
  copy, `alt` text, colours, widths and link targets are escaped for you, and
  URLs are restricted to `http`, `https`, `mailto`, `tel` and `cid` — a
  `javascript:` or `data:` URL becomes an inert `#`.
- **Parameters documented as HTML fragments are trusted** and emitted verbatim:
  the `content` argument of `MSOTable` and `MSOBackground`, the `html` argument
  of `WrapInGhostTable`/`MSOOnly`/`HideFromMSO`, the columns of `MSOColumns`,
  and the `header`/`body`/`footer` of `MSOEmailLayout`. Escape user data before
  putting it in one of those.

This matters because the output of these helpers becomes the **template source**
passed to `SetHTMLBody`, and `html/template` treats template text as trusted.
Contextual auto-escaping never inspects it, so escaping has to happen here.

`gsmail` also automatically injects UTF-8 charset, `lang` attribute, Dark Mode support markers, and CSS when Outlook compatibility is enabled.

**Outlook best practices:** Use `MSOSafeFontStack()` or `MSOFontStack()` for fonts; wrap emoji in `MSOEmoji("⏰")` or `<span style="font-family:'Segoe UI Emoji','Segoe UI Symbol',Arial,sans-serif">⏰</span>` so Outlook 2016 renders them; avoid `em`/`rem` in MSO-critical styles (use `px`); use tables for layout; declare `charset="UTF-8"` (injected automatically); add `MSOPreheader` at the top of the body for inbox preview. `outlook.ToOutlookHTML` automatically replaces empty `<td>` cells with `&nbsp;` to prevent Outlook from collapsing them.

```go
import "github.com/gsoultan/gsmail/outlook"

// MSOEmailLayout builds preheader + header + body + footer in one call.
body := outlook.MSOEmailLayout(600, "Preview text", "<h1>Logo</h1>", "<p>Main content</p>", "<small>Footer</small>")
email.SetOutlookBody("<html><body>"+body+"</body></html>", nil)

data := map[string]any{
    "Preheader": outlook.MSOPreheader("View in browser if formatting is broken"),
    "Content":   outlook.WrapInGhostTable("<div>My Content</div>", "600", "center"),
    "Button": outlook.MSOButton(outlook.ButtonConfig{
        Text:    "Click Here",
        Link:    "https://example.com",
        BgColor: "#007bff",
    }),
    "Image":      outlook.MSOImage("logo.png", "Logo", 200, 50, ""),
    "Background": outlook.MSOBackground("bg.png", "#f8f9fa", 600, 400, "<h1>Centered Text</h1>"),
    "Columns":    outlook.MSOColumns([]int{300, 300}, "Left Content", "Right Content"),
    "List":       outlook.MSOBulletList([]string{"Feature A", "Feature B"}, "✔", "color:green;"),
}
```

### Connection Checking (Ping)

Verify that your provider is correctly configured and reachable.

```go
// Ping any Sender or Receiver
err := gsmail.Ping(context.Background(), sender)
if err != nil {
    fmt.Printf("Connection failed: %v\n", err)
}

// Or call Ping directly on the provider
err = receiver.Ping(context.Background())
```

### SMTP Connection Pooling (Advanced)

For high-volume email sending, you can enable connection pooling to reuse SMTP connections and reduce overhead. `gsmail` provides a robust pooling implementation that supports 1000+ concurrent sends with zero connection leaks and automatic reuse.

```go
import (
    "context"
    "fmt"
    "time"
    "github.com/gsoultan/gsmail"
    "github.com/gsoultan/gsmail/smtp"
)

func main() {
    sender := smtp.NewSender("smtp.example.com", 587, "user", "pass", false)
    
    // Enable connection pooling for high-volume email delivery
    sender.EnablePool(smtp.PoolConfig{
        MaxIdle:     20,                // Number of idle connections to keep
        MaxOpen:     100,               // Total concurrent connections limit (0 for unlimited)
        IdleTimeout: 5 * time.Minute,   // Close connections idle for more than 5 minutes
        MaxLifetime: 1 * time.Hour,     // Maximum age of a connection before refresh
        Wait:        true,              // Block when pool is full (recommended for 1000+ concurrent)
    })
    defer sender.Close() // Gracefully closes all connections in the pool when done

    // Monitoring pool stats (optional)
    stats, _ := sender.PoolStats()
    fmt.Printf("Open: %d, Idle: %d, InUse: %d, WaitCount: %d, WaitDuration: %v\n", 
        stats.OpenConnections, stats.IdleConnections, stats.InUse, stats.WaitCount, stats.WaitDuration)

    // Use sender as usual
    email := gsmail.Email{ /* ... */ }
    err := gsmail.Send(context.Background(), sender, email)
}
```

#### High-Volume Recommendations (1000+ Concurrent)

When sending over 1000 emails concurrently, the following configuration and server choices are recommended:

1. **Enable Waiting**: Set `Wait: true` in `PoolConfig`. This allows your goroutines to wait for an available connection instead of failing with `ErrPoolFull`.
2. **Set MaxOpen**: Align `MaxOpen` with your SMTP server's concurrent connection limit (often 50-100 for commercial providers).
3. **Use MaxLifetime**: Helps avoid issues with long-lived connections that might be silently throttled or closed by the server.

**Recommended SMTP Providers:**
- **Amazon SES**: Most cost-effective and highly scalable for massive volumes.
- **Twilio SendGrid**: Industry standard with robust SMTP and API delivery.
- **Mailgun**: Excellent deliverability and developer-friendly features.
- **Postmark**: Best-in-class for transactional email speed and reliability.
- **SMTP2GO**: Highly reliable SMTP-focused service, great for high concurrency.

## Production-Ready Features

### Modern Authentication (OAuth2)

Connect to Gmail, Outlook, and other modern providers using XOAUTH2 or OAUTHBEARER.

```go
sender := smtp.NewSender("smtp.gmail.com", 587, "user@gmail.com", "", false)
tokenSource := func(ctx context.Context) (string, error) { return "your-token", nil }
sender.UseOAuth(gsmail.AuthXOAUTH2, tokenSource)
```

### Middleware & Interceptors

Customize the sending and receiving process with interceptors.

```go
wrappedSender := gsmail.WrapSender(sender,
    gsmail.LoggerInterceptor(log.Printf),
    gsmail.RecoveryInterceptor(),
    otelgs.SendInterceptor(),
)
```

### Background Worker

Send emails asynchronously without blocking your main application flow.

```go
bgSender := gsmail.NewBackgroundSender(sender, 100)
bgSender.Start(5)
bgSender.Send(email)
```

### Advanced IMAP (Search & IDLE)

```go
// Search for emails
opts := gsmail.SearchOptions{From: "boss@example.com", Unseen: true}
emails, _ := receiver.Search(ctx, opts, 10)

// Listen for new emails in real-time
emailsChan, errChan := receiver.Idle(ctx)
```

### Deliverability: DKIM Signing

```go
sender.DKIMConfig = &gsmail.DKIMOptions{
    Domain:     "example.com",
    Selector:   "default",
    PrivateKey: "-----BEGIN RSA PRIVATE KEY-----...",
}
```

### Domain Health Diagnostic

```go
health, _ := gsmail.CheckDomainHealth(ctx, "example.com", []string{"default"})
if !health.SPF.Valid {
    fmt.Printf("SPF Issue: %s\n", health.SPF.Details)
}
```

### Suppression: acting on bounces

Parsing a bounce is only useful if something stops the next send. Wire the
webhook output into a suppression list and the list into your sender:

```go
list := gsmail.NewMemorySuppressionList()

// in your webhook handler, after verifying the signature
event, _ := gsmail.ParseSESWebhook(msg.Message)
list.Record(event)               // hard bounces and complaints only

// wherever you build the sender
sender = gsmail.WrapSender(sender, gsmail.SuppressionInterceptor(list))
```

Suppressed recipients are removed from `To`, `Cc` and `Bcc`; if none remain the
send fails with `ErrAllRecipientsSuppressed` and nothing is transmitted. The
check **fails closed** — if the list cannot be reached the send fails, because
an unreachable list is not evidence that an address is safe to mail. Set
`SuppressionOptions.IgnoreErrors` to opt out.

`MemorySuppressionList` is for tests and small deployments. Implement
`Suppressor` over durable storage for anything else: a suppression list that
forgets on restart re-sends to people who already complained.

### One-click unsubscribe (RFC 8058)

Gmail and Yahoo require this of bulk senders. It needs **both**
`List-Unsubscribe` and `List-Unsubscribe-Post` — setting the first alone does
not satisfy it.

```go
email.SetOneClickUnsubscribe("https://example.com/u?t=" + token)

// refuse to send bulk mail that forgot them
bulk := gsmail.WrapSender(sender, gsmail.RequireOneClickUnsubscribe())
```

The target must be https: one-click works by the provider POSTing to it, and
the URL carries a token identifying the recipient. Your endpoint must accept a
POST without asking the recipient to confirm, and must **not** act on a bare
GET, or link-scanning security software will unsubscribe your recipients for
them.

### Composing senders

```go
// Try SES, fall back to SMTP. Permanent rejections are not retried elsewhere.
sender := gsmail.FailoverSender(sesSender, smtpSender)

// Pace sends to stay under a provider limit rather than reacting to a 429.
sender = gsmail.RateLimitedSender(sender, gsmail.NewTokenBucket(time.Second/10, 20))

// Fetch an OAuth token once instead of on every send and retry.
smtpSender.UseOAuth(gsmail.AuthXOAUTH2, gsmail.CachingTokenSource(refresh, time.Minute))
```

### Working with a mailbox

```go
r := imap.NewReceiver("imap.example.com", 993, user, pass, true)
r.Mailbox = "Archive"                      // defaults to INBOX

emails, _ := r.Receive(ctx, 50)
r.MarkSeen(ctx, imap.UIDsOf(emails)...)
r.Move(ctx, "Processed", emails[0].UID)
```

POP3 has no server-side read state, so set `DeleteAfterRetrieve` if you want
the next poll to see only new mail. It is off by default because deletion is
irreversible.

## Testing your own code

`gsmailtest` records messages instead of sending them, so you can assert on
what your application produced without a network or a provider account:

```go
sender := gsmailtest.NewSender()
svc := NewSignupService(sender)

svc.Welcome(ctx, "Alice <alice@example.com>")

msg := sender.MustTo(t, "alice@example.com")   // matching ignores case and display names
if msg.Subject != "Welcome" { ... }
```

`FailWith` and `FailNextWith` drive error and retry paths; `Receiver` with
`Push` drives `Idle`.

## Observability

```go
sender = gsmail.WrapSender(sender,
    otelgs.SendInterceptor(),          // spans, no personal data
    otelgs.SendMetricsInterceptor(),   // counts, durations, sizes
)
```

Neither records addresses or subjects. Use `otelgs.VerboseSendInterceptor` if
your retention and access controls allow it.

## Writing a provider

If you implement `gsmail.Sender` — for another API, or for your own internal
relay — run it against the conformance suite. It checks the things that are
easy to forget and expensive to get wrong: custom headers reaching the wire,
permanent 4xx responses not being retried four times, `Retry-After` being
honoured, `cid:` attachments declared inline, and the provider's own error text
surviving into the returned error.

```go
func TestConformance(t *testing.T) {
    providertest.Run(t, providertest.Harness{
        Name:      "acme",
        NewSender: func(t *testing.T, baseURL string) gsmail.Sender { /* ... */ },
        Decode:    func(t *testing.T, r *http.Request, body []byte) providertest.Sent { /* ... */ },
    })
}
```

See `sendgrid/conformance_test.go` for a worked example.

## Performance Guidelines

- **Use connection pooling** for high-volume SMTP delivery.
- **Render once, send many**: `WithMessage` hands you the rendered message in a
  pooled buffer, valid only until the callback returns. Use `RenderMessage`
  when you need to keep it.
- Rendering is bounded by the network, not by CPU. The library avoids
  gratuitous work — header emission is linear in header count, the buffer pool
  is sized to actually recycle real messages — but do not expect allocation
  count to be your bottleneck.

## Benchmarks

`gsmail` is optimized for high performance and low memory allocations. Below are the benchmark results for core utilities:

```text
BenchmarkWithMessage_NoCustomHeaders-15     65556      9145 ns/op     2009 B/op    44 allocs/op
BenchmarkWithMessage_HeaderScaling/headers=64-15
                                            23829     15237 ns/op     5003 B/op   126 allocs/op
BenchmarkGetRetryConfig-15             1000000000     0.34 ns/op         0 B/op     0 allocs/op
BenchmarkParseRawEmail-12                  344103      4371 ns/op     5400 B/op    15 allocs/op
```

Run them yourself with `go test -bench . -benchmem`; the figures above are from
one machine and are only useful as a shape.

*Tested on: AMD Ryzen 5 5500U, Go 1.25+*

## License

MIT
