# v1 readiness

A `v1` tag is a promise: the exported API will not break until `v2`. This is an
audit of what would be frozen, and what should change first.

> **Status, as of v0.9.0: every item below is resolved.** Everything under
> "Decided: remove before v1", "Decided: fix before freezing" and "Decided:
> freeze as-is" is done, with one correction noted inline: `DrainAndClose`
> stays exported. The audit called for removing it, and that was wrong — every
> sub-package provider uses it, and an out-of-tree provider needs it alongside
> `NewHTTPError` to drain a response body on the success path. The two belong
> together.
>
> **And the surface is nonetheless larger than when this was written.** See
> below. That is now the open question, and it is not the one this document
> was opened to answer.

## Surface

Measured at v0.5.0 (the version audited) and at v0.9.0 with the same tool:
exported top-level identifiers plus exported methods on them, excluding
`internal`, examples and test files. The original table gave 164 for the root
package by an unstated method, so compare the two columns here rather than
either against that figure.

| package | v0.5.0 | v0.9.0 | |
| --- | --- | --- | --- |
| `gsmail` (root) | 187 | 229 | +42 |
| `outlook` | 20 | 21 | +1 |
| `smtp` | 19 | 19 | — |
| `imap` | 7 | 17 | +10 |
| `otelgs` | 3 | 17 | +14 |
| `providertest` | 4 | 14 | +10 |
| `pop3` | 6 | 6 | — |
| `ses` / `sendgrid` / `mailgun` / `postmark` | 4 each | 4 each | — |
| **total** | **262** | **339** | **+77** |

The premise of this document was that the root package was too large to freeze
deliberately. Four releases of remediation later it is 42 symbols larger. The
removals happened — all 21 deprecated Outlook aliases and
`ValidateEmailExistence` are gone, and no package now exports anything marked
`Deprecated:` — and were outrun by additions roughly three to one.

Nothing in that growth is obviously wrong; `CheckDKIMKey`, `MessageIdentity`
and the `providertest` field constants each earned their place in a changelog
entry. That is exactly what makes it worth naming. A surface does not get too
large through one bad decision, it gets there through a run of individually
defensible ones, and the audit that was supposed to catch that measured the
package once and never again.

**Before v1, the useful question is no longer "which symbols should go?" — that
list is empty and has been acted on. It is whether 229 is a number anyone has
chosen.** The three packages that grew fastest in relative terms are `otelgs`
(3 → 17), `providertest` (4 → 14) and `imap` (7 → 17), and the first two are
instrumentation and test scaffolding rather than the mail API itself.

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

#### Revisited at v0.9.0: the trigger was watching the wrong thing

No third field appeared, so by the test written above nothing happened. Two
things happened anyway.

`Email.MessageIdentity` reads `UID` and `Mailbox`, and its own documentation
opens "returns a stable identifier for a **received** message". It is the first
*method* on `Email` that is meaningless on the send path — where the two fields
were inert state a sender could ignore, there is now behaviour whose result
depends on which half of the API produced the value.

And `Headers` now retains inbound trace headers — `Received`, `DKIM-Signature`,
`Authentication-Results`, the ARC set — that `BuildMessage` and `CustomHeaders`
deliberately discard on render. One field, populated by the receive path,
partially ignored by the send path. It is not a receiver-only field, so it did
not trip the counter either, but a caller who round-trips a message through
`Email` does not get the message back.

A trigger phrased as "a third such field" could not see either. Counting fields
was a proxy for the thing that matters, which is how much of `Email` only makes
sense in one direction, and the proxy came apart the first time the coupling
grew by any other means.

**Recommendation: still keep them, and fix the trigger.** The reasoning holds
and has if anything strengthened — a `Message` superset can still be added
after v1 without breaking `Email`, so this is a decision that stays cheap to
revisit, which is the whole reason it was safe to defer. Splitting now would
add a type and a conversion to a surface that has grown 29% since someone
called it too big.

The replacement trigger: **split when receive-only surface on `Email` — fields
and methods together — reaches five, or when any send-path function has to
consult a receive-only field to do its job.** The second half is the real one.
Today the two directions share a struct but not a code path; the day a `Sender`
branches on `UID`, they are one type by accident rather than by choice.

Freeze note for v1: `Email` does not round-trip. `ParseRawEmail` retains
headers that rendering drops, by design and for good reason, and v1 makes that
permanent. It should be stated on the type rather than left to be discovered.

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
- ~~`Sent` uses zero values to mean "this transport does not express that", and
  the suite skips the corresponding check. This is implicit; consider an
  explicit `Unsupported []string` before freezing.~~

  **Done.** `Harness.Unsupported []string` names the fields a provider's API
  cannot carry, using exported `Field*` constants. An empty field that is not
  declared is now a failure, so "this API has no Bcc" and "the Bcc was
  dropped" stop looking identical to the suite.

  It went on `Harness` rather than `Sent`, against the suggestion above,
  because it describes the transport rather than one request. SES decides its
  content shape per message and its `Decode` has two return paths; on `Sent`
  the list would have to be repeated on both, and the branch that forgot it
  would read as a provider dropping fields — reintroducing the ambiguity in a
  new place.

  An unrecognised name is rejected rather than ignored. A silently-unmatched
  string disables nothing and skips nothing while looking deliberate, which is
  the same failure as the zero value wearing a better disguise.

  All five in-tree providers pass with no declarations and zero skipped
  subtests, so nothing was relying on the implicit behaviour — the checks were
  live, they just could not have told anyone if they had not been.

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

> **Superseded — every step below shipped.** Kept for the record; the current
> position is in "What is actually left" underneath.

1. ~~Merge the open hardening PRs; let `main` settle for a release cycle so the
   v0.5.0 breaks are exercised by real users.~~
2. ~~**v0.6.0** — remove `ValidateEmailExistence` and the leaked internal
   plumbing. Document the mutate-after-Send constraint.~~ Shipped in v0.6.0.
3. ~~**v1.0.0** — delete the Outlook aliases, stop provider-side sniffing,
   freeze `providertest`.~~ The aliases went and the sniffing stopped before
   v1 rather than at it; `providertest` was settled in v0.9.0.

~~Cutting v1 directly from here would freeze 164 symbols including four that
should not be public and one design flaw that is actively producing bugs.~~
That is no longer true. Nothing on the surface is known to be wrong.

## What is actually left

The blocking list is empty. Every symbol this document said to remove is
removed, every fix it asked for is made, and the one question it left open —
`providertest`'s implicit zero-value convention — was settled in v0.9.0. On its
own terms, v1 could be cut today.

Two things argue for one more pass first, and neither is a defect:

1. **229 symbols in the root package, up from 187, none of them chosen as a
   set.** The audit's premise was that a surface this size cannot be frozen
   deliberately. Acting on its recommendations did not change that; it changed
   which symbols make up the number. Someone should read the current list end
   to end once and say "yes, all of this" — which is a different exercise from
   hunting for symbols that are individually wrong, and is the one that has
   never been done.

2. **`Email` does not round-trip, and v1 freezes that.** Documented above.
   Stating it on the type costs a doc comment; discovering it after v1 costs a
   caller a debugging session over headers that vanished without an error.

Neither needs a release. Both need somebody to decide on purpose, which is what
this document was for and what its own first version stopped doing the moment
its recommendations were carried out and its measurements were not repeated.
