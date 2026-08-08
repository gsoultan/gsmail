# v1 readiness

A `v1` tag is a promise: the exported API will not break until `v2`. This is an
audit of what would be frozen today, and what should change first.

Current surface, as of v0.5.0:

| package | exported symbols |
| --- | --- |
| `gsmail` (root) | 164 |
| `outlook` | 20 |
| `smtp` | 18 |
| `imap` | 7 |
| `pop3` | 6 |
| `ses` / `sendgrid` / `mailgun` / `postmark` | 4 each |
| `providertest` | 4 |
| `otelgs` | 3 |

164 in one package is too many to freeze deliberately. Twenty of those are the
deprecated Outlook aliases, which go at v1 by definition. The rest of this
document is about the remaining ~144.

---

## Decided: remove before v1

### The deprecated Outlook aliases

`outlook_compat.go` — twenty forwarders kept so v0.5.0 broke nobody. Deleting
them is the point of the deprecation.

### `ValidateEmailExistence`

Already marked deprecated. Callback verification is unreliable, gets the
sending host blocklisted, and `Validator` does the same job with the caveats
documented. Remove it.

### Internal plumbing that leaked into the public API

These read as implementation details that happened to be exported. Each is used
by exactly one caller inside this module:

| symbol | why it should go | replacement |
| --- | --- | --- |
| `BuildMessage(*[]byte, Email) error` | the `*[]byte` parameter exists only to hand a caller a pooled buffer — an implementation shape, not an API | `RenderMessage` or `WithMessage` |
| `HasHeader([]byte, string) bool` | a byte-scanning helper over a message this package just built | none needed |
| `DrainAndClose(io.ReadCloser)` | HTTP connection-reuse plumbing | keep unexported |
| `ParseRetryAfter(string) time.Duration` | HTTP header parsing | keep unexported |

`NewHTTPError` **stays exported**: an out-of-tree provider needs it to
participate in the retry contract, and `providertest` asserts that it does.

---

## Decided: fix before freezing

### `Email` does not record its own content type

`Body` and `HTMLBody` are both `[]byte`, and nothing on the type says which one
is authoritative. The consequence is visible today — every provider
re-derives it:

```
postmark/postmark.go:82    if gsmail.IsHTML(email.Body)
sendgrid/sendgrid.go:129   if len(email.Body) > 0 && !gsmail.IsHTML(email.Body)
sendgrid/sendgrid.go:138   if len(htmlBody) == 0 && gsmail.IsHTML(email.Body)
mailgun/mailgun.go:102     if len(email.Body) > 0 && !gsmail.IsHTML(email.Body)
mailgun/mailgun.go:107     if len(htmlBody) == 0 && gsmail.IsHTML(email.Body)
ses/ses.go:148             if gsmail.IsHTML(body)
```

Six sites, three different shapes, one decision. This is the same
propagation failure that produced every other cross-provider bug in this
codebase, and `providertest` cannot catch it because all six agree on the
common cases.

It is also **vestigial**. The sniffing exists because `SetBody` used to leave
HTML in `Body`. Since v0.5.0 `SetBody` routes HTML to `HTMLBody`, so `Body`
holds plaintext unless a caller assigns HTML to it by hand — at which point
sniffing is guessing at a mistake.

**Recommendation:** at v1, providers stop sniffing. `Body` is `text/plain`,
`HTMLBody` is `text/html`, as the field names already say. `SetTextBody` and
`SetHTMLBody` are the explicit setters; `SetBody` keeps sniffing, because
sniffing the *template* is a convenience the caller opted into, not a guess
about a field.

This is a breaking behaviour change for anyone assigning HTML to `Body`
directly, which is why it belongs at v1 rather than in a v0 minor.

> A related bug was fixed in this branch: `IsHTML` matched bare prefixes, so
> `"Please review <p1> pricing"` was classified as HTML and went out as
> `text/html`, where the recipient's renderer swallowed `<p1>` and the
> sentence lost a word. It now requires a character that may legally follow a
> tag name. That reduces the damage; it does not remove the design problem.

### Mutable exported configuration read during `Send`

`smtp.Sender.Host`, `.AuthMethod`, `.DKIMConfig`, `imap.Receiver.*` and the
provider `APIKey` fields are exported and read on every send. Mutating a
sender while it is sending is a data race. That is conventional Go — configure
before use — but at v1 it becomes a permanent hazard with no path to a fix.

**Recommendation:** document the constraint explicitly on each provider type
("not safe to modify after the first Send"), and treat functional options as
the v2 conversation rather than the v1 one. Unexporting the fields now would
break every existing caller for a hazard nobody has hit.

`BaseProvider.RetryConfig` was the one case where the hazard was real — its own
documentation invited concurrent mutation — and it was unexported in v0.5.0.

---

## Decided: freeze as-is

### `providertest.Harness` and `SMTPHarness`

Exported so out-of-tree providers can hold themselves to the same contract.
Freezing means the struct shape becomes a compatibility promise.

Adding fields is safe — every documented use is a keyed struct literal.
Changing `Decode`'s signature is not. Before v1, settle:

- `Decode func(*testing.T, *http.Request, []byte) Sent` ties the harness to
  `net/http`. That is correct for the four HTTP providers and irrelevant to
  SMTP, which is why `SMTPHarness` is separate. Keep them separate.
- `Sent` uses zero values to mean "this transport does not express that", and
  the suite skips the corresponding check. This is implicit; consider an
  explicit `Unsupported []string` before freezing.

### Everything else

The `Sender` / `Receiver` / `Pinger` / `AddressValidator` interfaces, the retry
contract (`Retryable`, `RetryAfterProvider`, `NonRetryable`, `IsRetryable`),
`Email`, the interceptors, the webhook verifiers, the health checks and the
validation helpers are all coherent and worth freezing.

---

## Not blocking v1

- **`internal/bufpool` coverage is 0%.** It is exercised through every render;
  direct tests would be tautological.
- **`go-imap` v1 is in maintenance and v2 exists.** v2 is a rewrite with a
  different API. The v1 races are fixed. Revisit when v1 blocks something.
- **Allocation count.** 44 per rendered message, on a path that ends in a
  network round trip. The quadratic header scan was worth fixing because it was
  algorithmic; this is not.

---

## Suggested sequence

1. Merge the open hardening PRs; let `main` settle for a release cycle so the
   v0.5.0 breaks are exercised by real users.
2. **v0.6.0** — remove `ValidateEmailExistence` and the leaked internal
   plumbing. Document the mutate-after-Send constraint. Still v0, still cheap
   to break.
3. **v1.0.0** — delete the Outlook aliases, stop provider-side sniffing, freeze
   `providertest`.

Cutting v1 directly from here would freeze 164 symbols including four that
should not be public and one design flaw that is actively producing bugs.
