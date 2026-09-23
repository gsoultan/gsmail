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
| `gsmailtest` | 29 | ☐ |
| `imap` | 17 | ☐ |
| `mailgun` | 4 | ☐ |
| `otelgs` | 17 | ☐ |
| `outlook` | 21 | ☐ |
| `pop3` | 6 | ☐ |
| `postmark` | 4 | ☐ |
| `providertest` | 14 | ☐ |
| `sendgrid` | 4 | ☐ |
| `ses` | 4 | ☐ |
| `smtp` | 19 | ☐ |

---

### auth.go

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

```
DKIMOptions [type]
DKIMPublicKeyRecord [func]
SignDKIM [func]
```

### email.go

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

```
HeaderHTML [const]
HeaderMIME [const]
HeaderPlain [const]
Ping [func]
Send [func]
```

### health.go

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

```
DrainAndClose [func]
HTTPError [type]
HTTPError.Error [method]
HTTPError.RetryAfter [method]
HTTPError.Retryable [method]
NewHTTPError [func]
```

### identity.go

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

```
Email.SetBodyFromS3 [method]
Email.SetBodyFromURL [method]
ErrTemplateTooLarge [var]
MaxTemplateSize [const]
ParseHTMLTemplate [func]
ParseTextTemplate [func]
```

### unsubscribe.go

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
