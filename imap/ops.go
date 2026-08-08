package imap

import (
	"context"
	"errors"
	"fmt"

	goimap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/gsoultan/gsmail"
)

// The package could previously only read. Without a way to mark a message
// seen or move it out of the way, every poll re-processes the same messages
// and the caller has to keep its own record of what it has handled.
//
// All of these operate on UIDs, which Receive and Search now set on the
// returned Email. UIDs are stable for the lifetime of a mailbox's
// UIDVALIDITY, unlike sequence numbers, which shift as messages are removed.

// ErrNoUIDs is returned when an operation is called with no messages.
var ErrNoUIDs = errors.New("gsmail/imap: no message UIDs supplied")

// MarkSeen flags messages as read.
func (f *Receiver) MarkSeen(ctx context.Context, uids ...uint32) error {
	return f.storeFlags(ctx, goimap.AddFlags, []any{goimap.SeenFlag}, uids)
}

// MarkUnseen clears the read flag.
func (f *Receiver) MarkUnseen(ctx context.Context, uids ...uint32) error {
	return f.storeFlags(ctx, goimap.RemoveFlags, []any{goimap.SeenFlag}, uids)
}

// Flag adds arbitrary IMAP flags, for example goimap.FlaggedFlag or a
// server-supported keyword.
func (f *Receiver) Flag(ctx context.Context, flags []string, uids ...uint32) error {
	return f.storeFlags(ctx, goimap.AddFlags, toAny(flags), uids)
}

// Unflag removes arbitrary IMAP flags.
func (f *Receiver) Unflag(ctx context.Context, flags []string, uids ...uint32) error {
	return f.storeFlags(ctx, goimap.RemoveFlags, toAny(flags), uids)
}

// Delete marks messages deleted and expunges them.
//
// IMAP deletion is two steps: \Deleted is a flag, and EXPUNGE is what actually
// removes it. Setting the flag alone leaves the message in place on most
// servers and visible to other clients, so this does both. Use MarkDeleted if
// you want the flag without the expunge.
func (f *Receiver) Delete(ctx context.Context, uids ...uint32) error {
	return f.withMailbox(ctx, false, func(c *client.Client) error {
		seqset, err := uidSet(uids)
		if err != nil {
			return err
		}
		if err := c.UidStore(seqset, goimap.FormatFlagsOp(goimap.AddFlags, true),
			[]any{goimap.DeletedFlag}, nil); err != nil {
			return fmt.Errorf("imap mark deleted: %w", err)
		}
		if err := c.Expunge(nil); err != nil {
			return fmt.Errorf("imap expunge: %w", err)
		}
		return nil
	})
}

// MarkDeleted sets \Deleted without expunging.
func (f *Receiver) MarkDeleted(ctx context.Context, uids ...uint32) error {
	return f.storeFlags(ctx, goimap.AddFlags, []any{goimap.DeletedFlag}, uids)
}

// Move relocates messages to another mailbox.
//
// It uses the MOVE extension (RFC 6851) when the server offers it and falls
// back to copy-then-delete otherwise. The fallback is not atomic: a failure
// between the two leaves the message in both mailboxes, which is the safer
// direction to fail.
func (f *Receiver) Move(ctx context.Context, dest string, uids ...uint32) error {
	if dest == "" {
		return gsmail.NonRetryable(fmt.Errorf("gsmail/imap: destination mailbox is required"))
	}

	return f.withMailbox(ctx, false, func(c *client.Client) error {
		seqset, err := uidSet(uids)
		if err != nil {
			return err
		}

		if ok, _ := c.Support("MOVE"); ok {
			if err := c.UidMove(seqset, dest); err != nil {
				return fmt.Errorf("imap move to %q: %w", dest, err)
			}
			return nil
		}

		if err := c.UidCopy(seqset, dest); err != nil {
			return fmt.Errorf("imap copy to %q: %w", dest, err)
		}
		if err := c.UidStore(seqset, goimap.FormatFlagsOp(goimap.AddFlags, true),
			[]any{goimap.DeletedFlag}, nil); err != nil {
			return fmt.Errorf("imap mark deleted after copy: %w", err)
		}
		if err := c.Expunge(nil); err != nil {
			return fmt.Errorf("imap expunge after copy: %w", err)
		}
		return nil
	})
}

// Mailboxes lists the folders available on the server.
func (f *Receiver) Mailboxes(ctx context.Context) ([]string, error) {
	var names []string
	err := gsmail.Retry(ctx, f.GetRetryConfig(), func() error {
		names = nil

		c, tlsOn, err := f.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout() }()

		if err := f.authenticate(ctx, c, tlsOn); err != nil {
			return err
		}

		ch := make(chan *goimap.MailboxInfo, 16)
		done := make(chan error, 1)
		go func() { done <- c.List("", "*", ch) }()

		for m := range ch {
			names = append(names, m.Name)
		}
		if err := <-done; err != nil {
			return fmt.Errorf("imap list mailboxes: %w", err)
		}
		return nil
	})
	return names, err
}

// storeFlags applies a flag operation to a set of UIDs.
func (f *Receiver) storeFlags(ctx context.Context, op goimap.FlagsOp, flags []any, uids []uint32) error {
	return f.withMailbox(ctx, false, func(c *client.Client) error {
		seqset, err := uidSet(uids)
		if err != nil {
			return err
		}
		if err := c.UidStore(seqset, goimap.FormatFlagsOp(op, true), flags, nil); err != nil {
			return fmt.Errorf("imap store flags: %w", err)
		}
		return nil
	})
}

// withMailbox connects, authenticates, selects the configured mailbox and runs
// fn, retrying the whole sequence per the provider's retry configuration.
func (f *Receiver) withMailbox(ctx context.Context, readOnly bool, fn func(*client.Client) error) error {
	return gsmail.Retry(ctx, f.GetRetryConfig(), func() error {
		c, tlsOn, err := f.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout() }()

		if err := f.authenticate(ctx, c, tlsOn); err != nil {
			return err
		}
		if _, err := c.Select(f.mailbox(), readOnly); err != nil {
			return fmt.Errorf("imap select %q: %w", f.mailbox(), err)
		}
		return fn(c)
	})
}

// uidSet builds a SeqSet from UIDs, refusing an empty one. An empty SeqSet is
// not a no-op in IMAP: it can be interpreted as "everything".
func uidSet(uids []uint32) (*goimap.SeqSet, error) {
	if len(uids) == 0 {
		return nil, gsmail.NonRetryable(ErrNoUIDs)
	}
	seqset := new(goimap.SeqSet)
	for _, uid := range uids {
		if uid == 0 {
			return nil, gsmail.NonRetryable(
				fmt.Errorf("gsmail/imap: UID 0 is not valid; was the message fetched by this package?"))
		}
		seqset.AddNum(uid)
	}
	return seqset, nil
}

func toAny(flags []string) []any {
	out := make([]any, 0, len(flags))
	for _, f := range flags {
		out = append(out, f)
	}
	return out
}

// UIDsOf collects the server-side identifiers from fetched messages, for
// feeding straight into MarkSeen, Move or Delete.
func UIDsOf(emails []gsmail.Email) []uint32 {
	out := make([]uint32, 0, len(emails))
	for _, e := range emails {
		if e.UID != 0 {
			out = append(out, e.UID)
		}
	}
	return out
}
