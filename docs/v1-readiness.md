# v1 readiness

A `v1` tag is a promise: the exported API will not break until `v2`. This is an
audit of what would be frozen, and what should change first.

> **Status: the changes below have been made.** Everything under "Decided:
> remove before v1" and "Decided: fix before freezing" is done, with one
> correction noted inline: `DrainAndClose` stays exported. The audit called for
> removing it, and that was wrong — every sub-package provider uses it, and an
> out-of-tree provider needs it alongside `NewHTTPError` to drain a response
> body on the success path. The two belong together.
>
> The root package went from 204 exported symbols to 180.

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
| `ParseRetryAfter(string) time.Duration` | applied by `NewHTTPError`; providers never call it | unexported |

`NewHTTPError` and `DrainAndClose` **stay exported**: an out-of-tree provider
needs both to participate in the retry contract — one to classify the failure,
the other to drain the body so the connection can be reused. `providertest`
asserts the first.

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

**Done.** Providers no longer sniff. `Body` is `text/plain`, `HTMLBody` is
`text/html`, as the field names already said. `SetTextBody` and `SetHTMLBody`
are the explicit setters; `SetBody` still sniffs, because sniffing the
*template* is a convenience the caller opted into, not a guess about a field.

`providertest` now has a `RoutesBodiesByField` case, so no provider can
reintroduce it: it sends a plaintext body containing `<p1>` and asserts it does
not arrive as HTML.

This breaks anyone assigning HTML to `Body` directly. Move it to `HTMLBody`.

> A related bug was fixed in this branch: `IsHTML` matched bare prefixes, so
> `"Please review <p1> pricing"` was classified as HTML and went out as
> `text/html`, where the recipient's renderer swallowed `<p1>` and the
> sentence lost a word. It now requires a character that may legally follow a
> tag name. That reduces the damage; it does not remove the design problem.

### `Email` carries receiver-only fields

`Email.UID` and `Email.Mailbox` were added so IMAP message operations had a
stable identifier to act on. They are set by receivers, meaningless when
sending, and ignored by every `Sender`.

That works, but it puts receive-side state on a type shared with the send
path, and freezing it means `Email` permanently describes both. The
alternative — a distinct `Message` type for fetched mail, embedding or
wrapping `Email` — is cleaner but doubles the surface a caller has to learn
and forces a conversion on the common "fetch, then reply" path.

**Recommendation:** keep them. Two fields documented as receiver-set is a
smaller cost than a second type, and the alternative can still be added later
as a superset without breaking `Email`. Revisit only if receivers accumulate
more state; if a third such field appears, that is the signal to split.

This entry exists because the fields landed *after* the rest of this document
was written, and were flagged in a pull request comment rather than here —
which is exactly how an audit stops being trustworthy.

### Mutable exported configuration read during `Send`

`smtp.Sender.Host`, `.AuthMethod`, `.DKIMConfig`, `imap.Receiver.*` and the
provider `APIKey` fields are exported and read on every send. Mutating a
sender while it is sending is a data race. That is conventional Go — configure
before use — but at v1 it becomes a permanent hazard with no path to a fix.

**Done for the documentation half.** Every provider type now states that its
fields are read on each operation and must be configured before first use,
with `SetRetryConfig` called out as the exception. Functional options remain a
v2 conversation: unexporting the fields would break every existing caller for
a hazard nobody has hit.

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
