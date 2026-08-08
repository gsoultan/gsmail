# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the module is at `v0`, breaking changes ship in minor releases. Read the
**Breaking** section before upgrading.

## [Unreleased]

### Fixed

- **SMTP retried permanent delivery failures.** A `550` reply was wrapped in a
  plain error, so `IsRetryable` defaulted to true and the message was sent four
  times. Repeated delivery to an address the receiver has already rejected is
  not just wasted work, it is a deliverability signal counted against the
  sender. SMTP reply codes are now classified the way RFC 5321 defines them:
  4xx transient, 5xx permanent. This is what `HTTPError` already did for the
  API-backed providers.

### Added

- **`providertest.RunSMTP`**, extending the conformance suite to SMTP. It
  asserts the same message-level contract as the HTTP suite against a message
  decoded off the wire, plus the cases only SMTP has: Bcc must reach RCPT TO
  and must *not* appear in the message headers, and a generated message must
  carry Date and Message-ID. The SMTP sender is run through it both with and
  without the connection pool.

  It found the 550 retry bug above on its first run, which is the second time
  extending this suite to a new transport has immediately turned up a real
  defect.

## [Unreleased]

### Added

- **Suppression.** `ParseBounce` and the webhook parsers told you an address
  was dead and nothing consumed that, so the next send went out anyway --
  which made bounce handling decorative. `Suppressor` is a one-method
  interface over whatever store holds your bounce history;
  `SuppressionInterceptor` removes suppressed recipients and fails the send
  with `ErrAllRecipientsSuppressed` when none remain.

  It fails closed: a list that cannot be reached is not evidence an address is
  deliverable. Only hard bounces suppress, because a soft bounce is a full
  mailbox and suppressing on one permanently cuts off a live recipient.
  Addresses are normalised via `NormalizeAddress`, so a bounce recorded for
  `Alice <Alice@Example.COM>` suppresses `alice@example.com`.

- **Mailbox selection and IMAP message operations.** `INBOX` was hardcoded in
  three places, so Archive, labels and shared folders were unreachable.
  `imap.Receiver.Mailbox` selects the folder and `Mailboxes()` lists them.

  Nothing could be modified either, so every poll re-processed the same
  messages. Fetches now carry `Email.UID` and `Email.Mailbox`, and
  `MarkSeen`, `MarkUnseen`, `Flag`, `Unflag`, `MarkDeleted`, `Delete`, `Move`
  and `UIDsOf` act on them. `Delete` expunges, because setting `\Deleted`
  alone leaves the message visible to other clients. An empty UID set is
  refused rather than ignored: an empty `SeqSet` can be read as *everything*.

- **`pop3.Receiver.DeleteAfterRetrieve`.** POP3 has no server-side read state,
  so without `DELE` every poll returns the whole mailbox again. Off by default
  because deletion is irreversible; it deletes only messages that were both
  retrieved and parsed, and only after the whole batch succeeded.

- **RFC 8058 one-click unsubscribe.** Gmail and Yahoo have required this of
  bulk senders since February 2024, and the requirement is a *pair* of
  headers. `SetOneClickUnsubscribe` sets both and insists on an https target;
  `RequireOneClickUnsubscribe` refuses to send without them. Plain http is
  rejected: an unsubscribe URL carries a token identifying the recipient.

- **`FailoverSender` and `RateLimitedSender`.** Failover moves on only for
  failures worth retrying elsewhere -- a permanent rejection means the message
  is the problem, not the provider. Rate limiting avoids provoking a 429
  rather than reacting to one; `Limiter` is an interface and `TokenBucket` is
  included so the common case needs no new dependency.

- **`CachingTokenSource`.** `TokenSource` is called on every send and every
  retry, so an uncached one is a round trip per message. Refresh is
  single-flighted and renews ahead of expiry.

- **`gsmailtest`.** `providertest` serves provider authors; this serves the
  far larger group writing applications with gsmail, who were all writing the
  same fake sender. Recorded messages are snapshots, address matching is
  normalised, and failure messages print what *was* sent.

- **OpenTelemetry metrics** in `otelgs`. Tracing answers "what happened to
  this message"; metrics answer "is sending healthy right now". Errors are
  split into permanent and retryable series, since that decides whether to
  page. No attribute carries an address or subject -- unbounded cardinality as
  well as a privacy problem.

## [v0.5.0]

### Added

- **`providertest`, a conformance suite for `Sender` implementations.** Every
  provider here independently got the same handful of decisions wrong —
  dropping `Email.Headers`, retrying a permanent 4xx, sending a `cid:`
  attachment as a plain attachment, swallowing the provider's error text. Those
  are not hard problems; they are easy to forget on the fourth provider. The
  suite makes forgetting impossible, and it is exported so a provider outside
  this module can hold itself to the same contract. All four providers pass it.

  It earned its keep immediately: on first run against SES it caught
  `BuildMessage` returning a bare error for an illegal header name, so the SMTP
  and SES paths would retry a malformed header three times while the API
  providers correctly treated it as permanent.

- **Webhook verifiers validated against official sources.** The SNS canonical
  string, the SendGrid signing scheme and the Mailgun HMAC construction were
  each checked against the vendor's own documentation and reference
  implementation rather than against my reading of them. This surfaced two
  defects — see below. `TestSNSCanonicalStringMatchesAWSDocumentedExample` now
  pins the format to the worked example in AWS's documentation.

### Fixed

- **SNS: per-message-type signable-field lists were wrong.** AWS's reference
  validator uses one list for all types and includes a field when present.
  Type-specific lists drop a `Subject` from a `SubscriptionConfirmation` and
  reject the message as forged.

- **SNS: an unrecognised `SignatureVersion` silently fell back to SHA-1.**
  Anything other than `"1"` or `"2"` is now rejected, as is an unrecognised
  message `Type`.

- **`BuildMessage` marks its caller-input errors permanent.** An illegal header
  name and `ErrConflictingContentType` are now wrapped in `NonRetryable`, so
  the SMTP and SES paths agree with `CustomHeaders`.

### Changed

- **The Outlook HTML builders moved to `github.com/gsoultan/gsmail/outlook`.**
  They are a rendering concern with a different risk profile from mail
  transport: every one produces markup that becomes the template source for
  `SetHTMLBody`, where `html/template`'s contextual escaping does not apply.
  Deprecated aliases remain in the root package, so **nothing breaks** — switch
  the import and drop the `gsmail.` prefix at your convenience. The aliases go
  away at v1.

- Both packages now share `internal/bufpool` rather than each holding a pool.

### Testing

- POP3 went from **0% to 66.7%** coverage, IMAP from **2.1% to 50.7%**, SES
  from **0% to 71.2%**. The new `outlook` package sits at 94.1%.
- Added fake IMAP and POP3 servers so the receive paths — the ones that parse
  untrusted network input — are exercised end to end.

### Breaking

- **`BaseProvider.RetryConfig` is no longer an exported field.** It was an
  exported field guarded by an unexported mutex, and its own documentation
  invited callers to assign it directly — which raced with `GetRetryConfig`
  reading it under that lock (confirmed under `-race`). The configuration now
  lives in an `atomic.Pointer`. Use `SetRetryConfig`, which was already the
  interface method and is safe to call concurrently with `Send`.

### Concurrency

- **Fixed a data race and a protocol violation in `imap.Idle`.** The loop ran
  `SEARCH` and `FETCH` on the connection while the IDLE goroutine still held
  it. An IMAP connection carries one command at a time, so this was both a race
  on the go-imap writer and illegal on the wire. `Idle` now runs a proper
  idle → interrupt (`DONE`) → work → re-idle cycle.

- **Fixed `imap.Idle` deadlocking under a burst of unilateral updates.** go-imap
  delivers updates on the connection's reader goroutine; nothing drained that
  channel during a fetch, so the reader blocked and the response the fetch was
  waiting for could never arrive. Updates are now drained continuously and
  coalesced into a single pending signal.

- **Fixed a data race on the message counter in `imap.fetch`.** The index
  counter was shared between the indexer goroutine and the collector. Workers
  abandon their input channel when the context is cancelled, which broke the
  happens-before chain the old code relied on. Ordering is now derived from the
  collected indices and the counter is goroutine-local.

- **Fixed `imap.fetch` leaving a FETCH in flight on cancellation.** Returning
  early left a command running on a connection the caller was about to log out
  of. The context watchdog now calls `Terminate()` so the command fails fast,
  and `fetch` always waits for it before returning.

- **Bounded the SNS signing-certificate cache.** It was an unbounded `sync.Map`
  keyed by a URL taken from the request body. Now capped at
  `maxCachedSigningCerts` (32) with eviction.

### Performance

Measured with `go test -bench` on the rendering path:

- **Header emission is now linear instead of quadratic.** `writeHeader` called
  `HasHeader` against the whole growing buffer for every header. A message with
  64 custom headers went from **118.6 µs to 15.2 µs (7.8× faster)** and from
  29.5 KB to 5.0 KB per render; 32 headers improved 3.6×. Only the caller's
  buffer prefix is scanned now.

- **The buffer pool actually pools.** `maxBufferSize` was 4 KiB, which any real
  HTML email exceeds, so every rendered message was discarded instead of
  recycled. Raised to 64 KiB, with the initial capacity raised from 1 KiB to
  4 KiB.

- **`base64MIMEWriter` no longer allocates per line.** It built `[]byte("\r\n")`
  inside the wrap loop, which escaped into the `io.Writer` call and heap
  allocated every 76 characters — the single largest allocation source when
  rendering a message.

- **The `From` address is parsed once instead of twice.** `generateMessageID`
  re-ran `mail.ParseAddress` on the same value `FormatAddress` had just parsed;
  RFC 5322 parsing was the top allocation source. The domain is now extracted
  by a validated scan.

- Together these take a plain message from **82 to 44 allocations** per render
  (2443 → 2009 B/op).

- **`GetRetryConfig` no longer contends.** Every `Send` calls it, and the
  `RWMutex` read lock cost **51.7 ns/op under parallel load versus 4.2 ns
  single-threaded**. The atomic pointer removes the contention entirely.

### Breaking (earlier entries)

- **`Email.SetBody` routes HTML to `HTMLBody`.** It sniffs the rendered
  template and stores the result in `HTMLBody` (HTML) or `Body` (plaintext).
  Previously HTML was left in `Body`. Code that reads `email.Body` after
  `SetBody` with an HTML template now sees an empty slice.
  Use `SetHTMLBody` or `SetTextBody` to choose explicitly and skip the sniffing.

- **Buffer helpers are unexported.** `GetBuffer`, `PutBuffer`,
  `NewBufferWriter`, `BufferWriter`, `UnsafeStringToBytes` and
  `UnsafeBytesToString` are gone from the public API. They exposed a pooled
  slice whose lifetime callers could not see. Use `RenderMessage` for a message
  you keep, or `WithMessage` for a message you only read inside a callback.

- **`BuildMessage` returns an `error`.** It reports `ErrConflictingContentType`
  and invalid header names instead of emitting an unparseable message.

- **`Receive` and `Search` reject a non-positive `limit`** with
  `ErrInvalidLimit`. Previously `limit` flowed straight into `make(chan)` and a
  worker count: `0` deadlocked with no context escape and a negative value
  panicked.

- **Minimum TLS version is now 1.2** for both SMTP and IMAP when `MinVersion`
  is unset (was 1.1, which RFC 8996 deprecates). `CipherSuites` now defaults to
  nil, deferring to the Go standard library instead of a hand-maintained list
  that pinned CBC and non-forward-secret `TLS_RSA_*` suites. Set the fields
  explicitly if you need the old behaviour.

- **OAuth authenticators refuse plaintext connections.** `NewXOAUTH2Auth` and
  `NewOAuthBearerAuth` return `ErrInsecureAuth` rather than hand a bearer token
  to a STARTTLS-stripping man in the middle. Loopback is exempt; use the
  `*Insecure` constructors for a trusted local relay.

- **`otelgs.SendInterceptor` no longer records personal data.** The sender,
  recipients and subject are replaced with counts, matching
  `gsmail.LoggerInterceptor`. Use `otelgs.VerboseSendInterceptor` for the
  previous behaviour.

- **`MSOBulletList` escapes its `items` and `bullet` arguments** as text.
  Markup passed in those parameters is now rendered literally. Compose markup
  in the surrounding template instead.

### Security

- **Escaped every `MSO*` helper.** Text, attribute and URL parameters were
  interpolated into HTML with `fmt.Sprintf` and no escaping, so a display name
  or link from user input could break out of an attribute or inject markup.
  URLs are now restricted to `http`, `https`, `mailto`, `tel` and `cid`, so
  `javascript:` and `data:` payloads become an inert `#`.
  This matters more than usual because the output is the *template source* for
  `SetHTMLBody`, which `html/template` treats as trusted: contextual
  auto-escaping never sees it.

- **Added webhook signature verification** (`webhook.go`). The `Parse*Webhook`
  functions accept unauthenticated input, so a forged hard bounce could
  suppress a real customer. New verifiers:
  `MailgunVerifier` (HMAC-SHA256), `SendGridVerifier` (ECDSA P-256),
  `PostmarkVerifier` (HTTP Basic) and `SNSVerifier` for SES. All reject
  replays outside `DefaultWebhookTolerance`; `SNSVerifier` pins the signing
  certificate URL to an AWS SNS host and supports a topic ARN allowlist.

- **SES no longer clobbers the ambient AWS credential chain.** Static
  credentials are only applied when both `AccessKey` and `SecretKey` are set;
  empty strings previously overrode IAM roles, environment variables and
  profiles. This matches the behaviour `SetBodyFromS3` already had.

- **Bounded remote template reads.** `SetBodyFromURL` and `SetBodyFromS3` read
  at most `MaxTemplateSize` (8 MiB) and report `ErrTemplateTooLarge`.

### Fixed

- **SMTP pool: connections were stranded on a waiter timeout.** A waiter whose
  context expired as `Put` handed it a connection left that connection live, in
  `p.active`, and counted against `MaxOpen` forever. Leaking `MaxOpen` of them
  wedged the pool permanently.

- **SMTP pool: `Close` drove the open count negative** by zeroing it while
  connections were still checked out, so the later `Put` decremented again.

- **IMAP: `fetch` no longer hangs when the server stalls.** The final receive
  on the fetch result now also selects on `ctx.Done()`.

- **Attachment filenames are always quoted, with an ASCII fallback.**
  `mime.FormatMediaType` emitted `filename=image.png` for simple names and only
  `filename*=utf-8''...` for non-ASCII ones; older Outlook mishandles both and
  falls back to `ATT00001.dat`. Content-ID is emitted as `Content-ID` rather
  than the canonicalised `Content-Id`.

- **`BuildMessage` errors are no longer discarded** by the SMTP and SES
  senders, which previously sent the partial buffer.

- **`Email.Headers` reaches every transport.** `List-Unsubscribe`,
  `In-Reply-To`, `References` and `X-*` headers were silently dropped by the
  SendGrid, Mailgun, Postmark and SES providers while working over SMTP.
  Gmail and Yahoo require `List-Unsubscribe` from bulk senders. The new
  `CustomHeaders` helper applies the same reserved-name filter and validation
  everywhere.

- **HTTP providers classify errors.** SendGrid, Mailgun and Postmark now return
  `*HTTPError`, so a 4xx is permanent and is not retried four times, and a 429
  honours `Retry-After`. Postmark errors now carry the response body, which
  names the `ErrorCode`.

- **SES applies `DKIMConfig` to every message.** It previously signed only
  messages that already needed the raw path, so a plain message went out
  unsigned.

- **`pop3.Receiver.InsecureSkipVerify` is wired up.** It was declared and never
  read, so setting it did nothing.

- **SendGrid marks a `cid:` attachment `inline`** instead of `attachment`,
  which left the image broken in the body and duplicated at the bottom.

- **`DrainAndClose` drains up to 1 MiB** instead of 4 KiB, so connections with
  a larger response body can actually be reused.

### Added

- `CustomHeaders`, `CheckLimit`, `ErrInvalidLimit`, `MaxTemplateSize`,
  `ErrTemplateTooLarge`.
- `postmark.Sender.MessageStream` for Postmark broadcast streams.
- `otelgs.VerboseSendInterceptor`; spans now set an explicit status code.
- `smtp.DefaultMinTLSVersion`, `imap.DefaultMinTLSVersion`.

### Changed

- SendGrid, Mailgun and Postmark build their request body once instead of
  re-encoding it on every retry attempt.
- SendGrid rejects an email with neither `Body` nor `HTMLBody` before sending.
- SMTP rejects a message with no recipients before connecting.
