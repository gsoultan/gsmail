package gsmail

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// Header returns the value of a header field, matching the name without regard
// to case.
//
// Reach for this rather than indexing Headers directly. A parsed message uses
// the canonical spelling net/mail produces, which is not always the one people
// expect: "Message-ID" becomes "Message-Id" and "DKIM-Signature" becomes
// "Dkim-Signature", so Headers["Message-ID"] finds nothing on a message that
// plainly has one. A message you built yourself keeps whatever spelling you
// used. This reads both.
func (e Email) Header(name string) string {
	if len(e.Headers) == 0 {
		return ""
	}
	if v, ok := e.Headers[name]; ok {
		return v
	}
	for k, v := range e.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// MessageID returns the message's Message-ID with the angle brackets removed,
// or "" if it carries none.
//
// Treat it as a label, not a fact. It is chosen by whoever sent the message,
// so it can be absent, repeated across different messages, or deliberately
// collided with someone else's. That is fine for deduplication and threading;
// it is not something to make a security or authorisation decision on.
func (e Email) MessageID() string {
	id := strings.TrimSpace(e.Header("Message-ID"))
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return strings.TrimSpace(id)
}

// IdentitySource names which property MessageIdentity was able to use.
type IdentitySource string

const (
	// IdentityMessageID is the message's own Message-ID: globally unique by
	// RFC 5322, and stable wherever the message is later found.
	IdentityMessageID IdentitySource = "message-id"

	// IdentityUID is the mailbox and server-side UID. Stable only within that
	// mailbox, and only while its UIDVALIDITY is unchanged.
	IdentityUID IdentitySource = "uid"

	// IdentityContent is a digest of the message itself, used when nothing
	// else identifies it. Two genuinely identical messages share one.
	IdentityContent IdentitySource = "content"

	// IdentityNone means the message carried nothing to identify it by, which
	// only happens for a zero Email.
	IdentityNone IdentitySource = "none"
)

// MessageIdentity returns a stable identifier for a received message, and which
// property it came from.
//
// The precedence is Message-ID, then mailbox and UID, then a digest of the
// content, in decreasing order of how widely each holds:
//
//   - A Message-ID identifies the message anywhere it is found, including in a
//     different mailbox or on a different server.
//   - A UID identifies it only within one mailbox, and only until that
//     mailbox's UIDVALIDITY changes. It is included with the mailbox name
//     because the same number means a different message elsewhere.
//   - A content digest identifies the bytes rather than the message, so two
//     genuinely identical messages collide. For deduplication that is usually
//     the wanted answer.
//
// Key on the pair, not the string. The three sources produce different kinds
// of value and nothing prevents one colliding with another, so a store that
// mixes them should record which was used. Scoping -- by tenant, account or
// connection -- belongs to the caller, since this package has no idea which
// mailboxes are the same mailbox.
//
// Note that a Message-ID is supplied by the sender, so an identity sourced
// from one is attacker-controlled. Deduplicating on it is fine; deciding that
// two messages are "the same message" for anything security-sensitive is not.
func (e Email) MessageIdentity() (string, IdentitySource) {
	if id := e.MessageID(); id != "" {
		return id, IdentityMessageID
	}

	if e.UID != 0 {
		mailbox := e.Mailbox
		if mailbox == "" {
			mailbox = "INBOX"
		}
		return mailbox + "/" + strconv.FormatUint(uint64(e.UID), 10), IdentityUID
	}

	if digest := e.contentDigest(); digest != "" {
		return digest, IdentityContent
	}

	return "", IdentityNone
}

// contentDigest hashes the parts of a message that stay the same each time it
// is fetched, returning "" for a message with nothing in it.
//
// Every field is length-prefixed so that moving bytes from one field to the
// next cannot produce the same digest: without it a subject of "ab" with body
// "c" would hash identically to subject "a" with body "bc".
func (e Email) contentDigest() string {
	if e.From == "" && e.Subject == "" && len(e.Body) == 0 &&
		len(e.HTMLBody) == 0 && len(e.Attachments) == 0 {
		return ""
	}

	h := sha256.New()
	write := func(b []byte) {
		var n [8]byte
		for i := 0; i < 8; i++ {
			n[i] = byte(len(b) >> (8 * (7 - i)))
		}
		h.Write(n[:])
		h.Write(b)
	}
	writeString := func(s string) { write(unsafeStringToBytes(s)) }

	writeString(e.From)
	writeString(e.Subject)
	// The Date the sender set, not the time this copy was fetched.
	writeString(e.Header("Date"))
	for _, to := range e.To {
		writeString(to)
	}
	write(e.Body)
	write(e.HTMLBody)
	for _, att := range e.Attachments {
		writeString(att.Filename)
		write(att.Data)
	}

	return hex.EncodeToString(h.Sum(nil))
}
