# v1 surface review

`docs/v1-readiness.md` closes with the one thing left before `v1`:

> Someone should read the current list end to end once and say "yes, all of
> this" — which is a different exercise from hunting for symbols that are
> individually wrong, and is the one that has never been done.

This is the list. It is a navigation aid, not the API: **judging whether a
symbol should be public forever needs to know what it does**, so read it
alongside `go doc -all .`, which carries the doc comments this does not.

    go doc -all .                       # the root package, with prose
    go doc -all ./imap                  # and so on per package
    go run ./internal/cmd/surface -v    # regenerate the names below

Reviewed at **v0.11.0**. If the surface moves, the CI budget check in
`docs/surface-budget.txt` fails and this review is stale by definition —
that is the intended coupling, and the reason this file is a snapshot rather
than a promise.

## Documentation coverage

Every exported declaration in every package carries a doc comment — **341 of
341**. Five were bare when this review was written, and two of those were
functions that other doc comments were already pointing at:

| symbol | |
| --- | --- |
| `ParseRawEmail` | `Email`'s doc explains that it retains trace headers which `BuildMessage` drops; the function itself said nothing |
| `outlook.ToOutlookHTML` | `AlreadyConverted` is documented as reporting "whether `ToOutlookHTML` has already been applied" |
| `smtp.ErrPoolClosed`, `smtp.ErrPoolFull` | error sentinels callers match with `errors.Is` |
| `HTTPError.Error` | self-evident by convention, but a convention is not a description |

This matters for the read-through more than the number suggests. "Yes, all of
this" is a claim about behaviour, not names, and a symbol nobody has described
is one nobody can agree to freeze.

## How to use it

Go file by file. For each, the question is not "is this symbol correct?" —
that list is empty and has been acted on. It is **"do I want to still be
supporting exactly this in three years?"** A `v1` tag says yes to every line
below until `v2`.

Record the answer per file in the right-hand column.

## Root package — 220 symbols

| file | symbols | reviewed |
| --- | ---: | --- |
| [`provider.go`](#providergo) | 33 | ☐ |
| [`suppression.go`](#suppressiongo) | 27 | ☐ |
| [`utils.go`](#utilsgo) | 27 | ☐ |
| [`webhook.go`](#webhookgo) | 16 | ☐ |
| [`auth.go`](#authgo) | 15 | ☐ |
| [`bounce.go`](#bouncego) | 12 | ☐ |
| [`compose.go`](#composego) | 12 | ☐ |
| [`middleware.go`](#middlewarego) | 12 | ☐ |
| [`worker.go`](#workergo) | 11 | ☐ |
| [`health.go`](#healthgo) | 10 | ☐ |
| [`email.go`](#emailgo) | 9 | ☐ |
| [`identity.go`](#identitygo) | 8 | ☐ |
| [`unsubscribe.go`](#unsubscribego) | 8 | ☐ |
| [`httperror.go`](#httperrorgo) | 6 | ☐ |
| [`template.go`](#templatego) | 6 | ☐ |
| [`gsmail.go`](#gsmailgo) | 5 | ☐ |
| [`dkim.go`](#dkimgo) | 3 | ☐ |

## Other packages — 139 symbols

| package | symbols | reviewed |
| --- | ---: | --- |
| [`gsmailtest`](#gsmailtest) | 29 | ☐ |
| [`imap`](#imap) | 17 | ☐ |
| [`mailgun`](#mailgun) | 4 | ☐ |
| [`otelgs`](#otelgs) | 17 | ☐ |
| [`outlook`](#outlook) | 21 | ☐ |
| [`pop3`](#pop3) | 6 | ☐ |
| [`postmark`](#postmark) | 4 | ☐ |
| [`providertest`](#providertest) | 14 | ☐ |
| [`sendgrid`](#sendgrid) | 4 | ☐ |
| [`ses`](#ses) | 4 | ☐ |
| [`smtp`](#smtp) | 19 | ☐ |

---

### auth.go

**SMTP authentication, mostly OAuth.** `AuthMethod` constants, `SMTPAuth` adapting `sasl.Client` to `net/smtp.Auth`, `TokenSource` and `CachingTokenSource` for bearer tokens. Note the four `New*Insecure` constructors: they permit auth over an unencrypted connection, and `ErrInsecureAuth` is what guards the default. Freezing those four names means keeping that escape hatch.

```
AuthMethod [type]
AuthOAUTHBEARER [const]
AuthPlain [const]
AuthXOAUTH2 [const]
ErrInsecureAuth [var]
NewOAuthBearerAuth [func]
NewOAuthBearerAuthInsecure [func]
NewXOAUTH2Auth [func]
NewXOAUTH2AuthInsecure [func]
NewXOAUTH2Client [func]
SMTPAuth [type]
SMTPAuth.AllowInsecure [method]
SMTPAuth.Next [method]
SMTPAuth.Start [method]
TokenSource [type]
```

### bounce.go

**Turning provider bounce and complaint payloads into `Bounce` and `Complaint`.** Four `Parse*Webhook` functions, `BounceType` constants, `SESNotification`. Reads as the second half of `webhook.go`: verify first, parse second, and the ordering is a security property rather than a style preference.

```
Bounce [type]
BounceHard [const]
BounceSoft [const]
BounceType [type]
Complaint [type]
ParseBounce [func]
ParseComplaint [func]
ParseMailgunWebhook [func]
ParsePostmarkWebhook [func]
ParseSESWebhook [func]
ParseSendGridWebhook [func]
SESNotification [type]
```

### compose.go

**Senders built out of other senders.** `FailoverSender` tries each in order until one succeeds; `RateLimitedSender` paces through a `Limiter`, which `golang.org/x/time/rate` already satisfies; `TokenBucket` exists so the common case needs no extra dependency.

```
CachingTokenSource [func]
DefaultTokenLeeway [const]
ErrNoSenders [var]
FailoverSender [func]
FailoverSenderWithCallback [func]
Limiter [type]
Limiter.Wait [interface method]
NewTokenBucket [func]
RateLimitedSender [func]
RefreshFunc [type]
TokenBucket [type]
TokenBucket.Wait [method]
```

### dkim.go

**DKIM signing** — `DKIMOptions`, `SignDKIM`, and `DKIMPublicKeyRecord`, which returns the value your TXT record should publish. Three symbols; pairs with `CheckDKIMKey` in `health.go` for the verify half.

```
DKIMOptions [type]
DKIMPublicKeyRecord [func]
SignDKIM [func]
```

### email.go

**Small surface, heaviest consequence.** `Email`, `Attachment`, `S3Config` and the body setters. `Email` appears in nearly every signature in the library, and its doc comment carries the round-trip caveat: a parsed message re-rendered does not reproduce its input, and the trace headers are dropped silently.

```
Attachment [type]
Email [type]
Email.IsOutlookCompatible [method]
Email.SetBody [method]
Email.SetHTMLBody [method]
Email.SetHeader [method]
Email.SetOutlookBody [method]
Email.SetTextBody [method]
S3Config [type]
```

### gsmail.go

**Three Content-Type constants, and what remains of the package-level facade** after v0.11.0: `Send` and `Ping`. These are the spelling the README and `doc.go` teach, which is the whole reason they survived the trim.

```
HeaderHTML [const]
HeaderMIME [const]
HeaderPlain [const]
Ping [func]
Send [func]
```

### health.go

**Domain deliverability checks** — SPF, DKIM, DMARC, MX, plus `CheckDKIMKey`, which compares the published record against the key you actually sign with. `HealthChecker` carries an optional `Resolver` so the checks are testable. `CheckDomainHealth` is the one package-level entry point left after v0.11.0 removed its five siblings.

```
CheckDomainHealth [func]
DomainHealth [type]
HealthChecker [type]
HealthChecker.CheckDKIM [method]
HealthChecker.CheckDKIMKey [method]
HealthChecker.CheckDMARC [method]
HealthChecker.CheckDomainHealth [method]
HealthChecker.CheckMX [method]
HealthChecker.CheckSPF [method]
HealthResult [type]
```

### httperror.go

**Classifying provider API failures** — 408, 429 and 5xx transient, everything else permanent, honouring `Retry-After`. `NewHTTPError` and `DrainAndClose` are exported deliberately and the readiness audit reversed itself on removing them: an out-of-tree provider needs both to participate in the retry contract.

```
DrainAndClose [func]
HTTPError [type]
HTTPError.Error [method]
HTTPError.RetryAfter [method]
HTTPError.Retryable [method]
NewHTTPError [func]
```

### identity.go

**Deriving a stable identifier for a received message** — `MessageIdentity` and `IdentitySource`, falling back from Message-ID to UID to a content hash. Meaningful only on the receive path, which is the tension `v1-readiness.md` records under `Email` carrying receiver-only fields.

```
Email.Header [method]
Email.MessageID [method]
Email.MessageIdentity [method]
IdentityContent [const]
IdentityMessageID [const]
IdentityNone [const]
IdentitySource [type]
IdentityUID [const]
```

### middleware.go

**The interceptor mechanism everything cross-cutting composes through.** Four interceptor function types (Send, Receive, Search, Idle), `WrapSender`/`WrapReceiver` to install them, and the built-ins. Suppression, tracing, recovery and the unsubscribe guard are all just interceptors, so this is load-bearing well beyond its twelve symbols.

```
IdleInterceptor [type]
LoggerInterceptor [func]
ReceiveInterceptor [type]
ReceiverInterceptors [type]
RecoveryInterceptor [func]
RecoveryInterceptorWithLogger [func]
SearchInterceptor [type]
SendInterceptor [type]
VerboseLoggerInterceptor [func]
WrapReceiver [func]
WrapReceiverWith [func]
WrapSender [func]
```

### provider.go

**The core contracts, and the least negotiable thing here.** One interface per direction (`Sender`, `Receiver`), plus `Pinger` and `AddressValidator`; the retry contract (`RetryConfig`, `Retryable`, `RetryAfterProvider`, `NonRetryable`, `IsRetryable`); and `BaseProvider`, which in-tree providers embed. This is what an out-of-tree provider implements, so freezing it is the substance of what `v1` promises.

```
AddressValidator [type]
AddressValidator.Validate [interface method]
BaseProvider [type]
BaseProvider.GetRetryConfig [method]
BaseProvider.SetRetryConfig [method]
BaseProvider.Validate [method]
CheckLimit [func]
DefaultRetryConfig [func]
ErrEnvelopeUnsupported [var]
ErrInvalidLimit [var]
ErrNonRetryable [var]
IsRetryable [func]
NonRetryable [func]
Pinger [type]
Pinger.Ping [interface method]
Receiver [type]
Receiver.Idle [interface method]
Receiver.Ping [interface method]
Receiver.Receive [interface method]
Receiver.Search [interface method]
Receiver.SetRetryConfig [interface method]
RejectEnvelope [func]
Retry [func]
RetryAfterProvider [type]
RetryAfterProvider.RetryAfter [interface method]
RetryConfig [type]
Retryable [type]
Retryable.Retryable [interface method]
SearchOptions [type]
Sender [type]
Sender.Ping [interface method]
Sender.Send [interface method]
Sender.SetRetryConfig [interface method]
```

### suppression.go

**Withholding mail from addresses that bounced or complained.** `Suppressor` is the interface; `MemorySuppressionList` is the batteries-included implementation and accounts for ten of the twenty-seven on its own; `SuppressionInterceptor` wires one into a sender. Taught in `doc.go`'s interceptor example and a dedicated README section.

```
ErrAllRecipientsSuppressed [var]
MemorySuppressionList [type]
MemorySuppressionList.Add [method]
MemorySuppressionList.AddBounce [method]
MemorySuppressionList.AddComplaint [method]
MemorySuppressionList.AddEntry [method]
MemorySuppressionList.Entries [method]
MemorySuppressionList.Entry [method]
MemorySuppressionList.Len [method]
MemorySuppressionList.Record [method]
MemorySuppressionList.Remove [method]
MemorySuppressionList.Suppressed [method]
NewMemorySuppressionList [func]
NormalizeAddress [func]
ReasonComplaint [const]
ReasonHardBounce [const]
ReasonManual [const]
ReasonUnsubscribe [const]
SuppressionEntry [type]
SuppressionInterceptor [func]
SuppressionInterceptorWith [func]
SuppressionOptions [type]
SuppressionReason [type]
Suppressor [type]
Suppressor.Suppressed [interface method]
SuppressorFunc [type]
SuppressorFunc.Suppressed [method]
```

### template.go

**Rendering templates into a body**, including remote loads via `SetBodyFromURL` and `SetBodyFromS3`, bounded by `MaxTemplateSize` so a remote template cannot dictate memory use.

```
Email.SetBodyFromS3 [method]
Email.SetBodyFromURL [method]
ErrTemplateTooLarge [var]
MaxTemplateSize [const]
ParseHTMLTemplate [func]
ParseTextTemplate [func]
```

### unsubscribe.go

**RFC 8058 one-click unsubscribe.** `SetOneClickUnsubscribe` sets both headers, because either alone does not satisfy what Gmail and Yahoo require of bulk senders. `RequireOneClickUnsubscribe` is an interceptor that refuses to send without them.

```
Email.HasOneClickUnsubscribe [method]
Email.SetListUnsubscribe [method]
Email.SetOneClickUnsubscribe [method]
ErrNoHTTPSUnsubscribe [var]
ErrNoUnsubscribeTarget [var]
ErrUnsafeUnsubscribeScheme [var]
ListUnsubscribePostValue [const]
RequireOneClickUnsubscribe [func]
```

### utils.go

**The grab-bag, and the one to read hardest.** Nine error sentinels, address parsing and formatting (`ParseEmailAddress`, `FormatAddress*`, `NormalizeAddress`), validation (`IsValidEmail`, `ValidateEmailSyntax`, `IsDisposableEmail`, `Validator`), the `Resolver` seam for DNS, and `RenderMessage`/`WithMessage`/`CustomHeaders`/`SanitizeHeaderValue`. Each is defensible on its own; `utils` is where things land when no better home suggested itself, which is exactly the accretion this review exists to catch.

```
CustomHeaders [func]
DefaultDisposableDomains [func]
ErrConflictingContentType [var]
ErrDisposableEmail [var]
ErrEmptyAddress [var]
ErrIllegalAddress [var]
ErrInvalidEmailFormat [var]
ErrNoMXRecords [var]
ErrPartTooLarge [var]
ErrTooDeeplyNested [var]
FormatAddress [func]
FormatAddressList [func]
FormatAddresses [func]
IsDisposableEmail [func]
IsHTML [func]
IsValidEmail [func]
ParseEmailAddress [func]
ParseRawEmail [func]
RenderMessage [func]
Resolver [type]
Resolver.LookupMX [interface method]
Resolver.LookupTXT [interface method]
SanitizeHeaderValue [func]
ValidateEmailSyntax [func]
Validator [type]
Validator.Validate [method]
WithMessage [func]
```

### webhook.go

**Authenticating provider webhooks before anything trusts them.** Four verifiers (`SNSVerifier`, `SendGridVerifier`, `MailgunVerifier`, `PostmarkVerifier`), their signature errors, and `DefaultWebhookTolerance` for clock drift. The README is blunt about why: the `Parse*` functions in `bounce.go` accept unauthenticated input, so anyone who can reach your endpoint can forge a hard bounce and get a real customer suppressed.

```
DefaultWebhookTolerance [const]
ErrSignatureExpired [var]
ErrSignatureInvalid [var]
ErrSignatureMissing [var]
ErrSigningKeyMissing [var]
MailgunVerifier [type]
MailgunVerifier.Verify [method]
PostmarkVerifier [type]
PostmarkVerifier.Verify [method]
SNSMessage [type]
SNSVerifier [type]
SNSVerifier.Verify [method]
SendGridSignatureHeader [const]
SendGridTimestampHeader [const]
SendGridVerifier [type]
SendGridVerifier.Verify [method]
```

### worker.go

**`BackgroundSender`** — a worker pool for fire-and-forget sending, with an `Errors` channel, `TrySend`, `StopNow`, and the `ErrQueueFull`/`ErrSenderStopped` pair that makes back-pressure explicit rather than silent.

```
BackgroundSendError [type]
BackgroundSender [type]
BackgroundSender.Errors [method]
BackgroundSender.Send [method]
BackgroundSender.Start [method]
BackgroundSender.Stop [method]
BackgroundSender.StopNow [method]
BackgroundSender.TrySend [method]
ErrQueueFull [var]
ErrSenderStopped [var]
NewBackgroundSender [func]
```

---

### gsmailtest

**Test doubles for application authors** — the package that went 29 symbols unmeasured through two surface audits. `NewSender` records messages instead of sending them so a service's tests can assert what it would have sent, without a network or a provider account. Distinct from `providertest`, which serves the other side: people writing a provider. Featured in the README, and frozen by v1 exactly like the rest.

```
ErrNotDelivered [var]
NewReceiver [func]
NewSender [func]
Receiver [type]
Receiver.Add [method]
Receiver.FailWith [method]
Receiver.Idle [method]
Receiver.Ping [method]
Receiver.Push [method]
Receiver.Receive [method]
Receiver.Search [method]
Sender [type]
Sender.Count [method]
Sender.FailNextWith [method]
Sender.FailPingWith [method]
Sender.FailWith [method]
Sender.Last [method]
Sender.MustCount [method]
Sender.MustLast [method]
Sender.MustTo [method]
Sender.OnSend [method]
Sender.Ping [method]
Sender.Reset [method]
Sender.Send [method]
Sender.Sent [method]
Sender.To [method]
TB [type]
TB.Fatalf [interface method]
TB.Helper [interface method]
```

### outlook

**Making HTML survive Microsoft Outlook's renderer.** `ToOutlookHTML` injects the mso conditional block and container table Outlook needs, and is idempotent by design; `AlreadyConverted` tests for the mark. The `MSO*` helpers and `ButtonConfig` cover the constructs that break most often. All twenty-one deprecated forwarders from v0.5.0 are gone — this is the surface that replaced them.

```
AlreadyConverted [func]
ButtonConfig [type]
HideFromMSO [func]
IsOutlookCompatible [func]
MSOBackground [func]
MSOBulletList [func]
MSOButton [func]
MSOColumns [func]
MSOEmailLayout [func]
MSOEmoji [func]
MSOFontStack [func]
MSOImage [func]
MSOOnly [func]
MSOPreheader [func]
MSOPreheaderMaxLength [const]
MSOPreheaderTruncated [func]
MSOSafeFontStack [func]
MSOSpacer [func]
MSOTable [func]
ToOutlookHTML [func]
WrapInGhostTable [func]
```

### smtp

**The SMTP sender, and the only in-tree provider with a connection pool.** `NewSender`/`Sender` plus `Pool`, `NewPool`, `PoolConfig` and the `ErrPoolClosed`/`ErrPoolFull` pair. `DefaultMinTLSVersion` is exported so a caller can see the floor rather than discover it. The readiness audit flags that `Sender.Host`, `.AuthMethod` and `.DKIMConfig` are read on every send, so mutating a sender mid-flight is a data race — conventional Go, but permanent at v1.

```
DefaultMinTLSVersion [const]
ErrPoolClosed [var]
ErrPoolFull [var]
NewPool [func]
NewSender [func]
Pool [type]
Pool.Close [method]
Pool.Get [method]
Pool.Put [method]
Pool.Stats [method]
PoolConfig [type]
Sender [type]
Sender.Close [method]
Sender.EnablePool [method]
Sender.Ping [method]
Sender.PoolStats [method]
Sender.Send [method]
Sender.UseOAuth [method]
Stats [type]
```

### imap

**IMAP receiving, and the richest Receiver.** Beyond `Receive`/`Search`/`Idle` it carries the mailbox operations — `Delete`, `Flag`, `Move`, `SelectMailbox` — which is why it grew 7 → 17 across the audited releases. `ErrNoUIDs` and `Email.UID`/`Mailbox` in the root package are the coupling the readiness doc tracks under receiver-only fields.

```
DefaultMinTLSVersion [const]
ErrNoUIDs [var]
NewReceiver [func]
Receiver [type]
Receiver.Delete [method]
Receiver.Flag [method]
Receiver.Idle [method]
Receiver.Mailboxes [method]
Receiver.MarkDeleted [method]
Receiver.MarkSeen [method]
Receiver.MarkUnseen [method]
Receiver.Move [method]
Receiver.Ping [method]
Receiver.Receive [method]
Receiver.Search [method]
Receiver.Unflag [method]
UIDsOf [func]
```

### otelgs

**OpenTelemetry instrumentation**, and the fastest-growing package in relative terms (3 → 17). Mostly `Metric*` and `Attr*` name constants plus the interceptors. Worth a deliberate look at v1: exported metric and attribute names are a compatibility surface for dashboards, so freezing them is a promise to somebody's alerting, not just to their compiler.

```
AttrErrorKind [const]
AttrOutcome [const]
MetricReceiveCount [const]
MetricReceivedTotal [const]
MetricRecipients [const]
MetricSendBytes [const]
MetricSendCount [const]
MetricSendDuration [const]
Metrics [type]
Metrics.ReceiveInterceptor [method]
Metrics.SendInterceptor [method]
NewMetrics [func]
ReceiveInterceptor [func]
ReceiveMetricsInterceptor [func]
SendInterceptor [func]
SendMetricsInterceptor [func]
VerboseSendInterceptor [func]
```

### providertest

**The conformance suite a provider author runs** to prove a `Sender` obeys the library's contract. `Harness`, `SMTPHarness`, and the `Field*` constants that let a provider declare what its API genuinely cannot carry — settled in v0.9.0, so an undeclared empty field now fails rather than silently skipping.

```
Attachment [type]
FieldAttachments [const]
FieldBcc [const]
FieldCc [const]
FieldDisposition [const]
FieldHTML [const]
FieldSubject [const]
FieldText [const]
FieldTo [const]
Harness [type]
Run [func]
RunSMTP [func]
SMTPHarness [type]
Sent [type]
```

### pop3

**POP3 receiving** — the minimal `Receiver`. Six symbols, unchanged since v0.5.0, and the one package here that has never moved.

```
NewReceiver [func]
Receiver [type]
Receiver.Idle [method]
Receiver.Ping [method]
Receiver.Receive [method]
Receiver.Search [method]
```

### ses

**Amazon SES sender.** `NewSender`, `Sender`, `Send`, `Ping` — the four-symbol shape every HTTP provider in this module shares.

```
NewSender [func]
Sender [type]
Sender.Ping [method]
Sender.Send [method]
```

### sendgrid

**SendGrid sender.** Same four-symbol shape.

```
NewSender [func]
Sender [type]
Sender.Ping [method]
Sender.Send [method]
```

### mailgun

**Mailgun sender.** Same four-symbol shape.

```
NewSender [func]
Sender [type]
Sender.Ping [method]
Sender.Send [method]
```

### postmark

**Postmark sender.** Same four-symbol shape.

```
NewSender [func]
Sender [type]
Sender.Ping [method]
Sender.Send [method]
```
