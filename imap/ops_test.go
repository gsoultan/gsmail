package imap

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
)

// The package used to read INBOX and nothing else, with the name hardcoded in
// three places.
func TestMailboxSelection(t *testing.T) {
	t.Run("defaults to INBOX", func(t *testing.T) {
		s := startFakeIMAP(t)
		if _, err := idleReceiver(s).Receive(context.Background(), 5); err != nil {
			t.Fatal(err)
		}
		if got := s.selectedMailboxes(); len(got) == 0 || got[0] != "INBOX" {
			t.Errorf("selected %v, want INBOX", got)
		}
	})

	t.Run("honours an explicit mailbox", func(t *testing.T) {
		s := startFakeIMAP(t)
		f := idleReceiver(s)
		f.Mailbox = "Archive"

		if _, err := f.Receive(context.Background(), 5); err != nil {
			t.Fatal(err)
		}
		if got := s.selectedMailboxes(); len(got) == 0 || got[0] != "Archive" {
			t.Errorf("selected %v, want Archive", got)
		}
	})

	t.Run("applies to Search too", func(t *testing.T) {
		s := startFakeIMAP(t)
		f := idleReceiver(s)
		f.Mailbox = "Work/Reports"

		if _, err := f.Search(context.Background(), gsmail.SearchOptions{Unseen: true}, 5); err != nil {
			t.Fatal(err)
		}
		if got := s.selectedMailboxes(); len(got) == 0 || got[0] != "Work/Reports" {
			t.Errorf("selected %v, want Work/Reports", got)
		}
	})
}

// Message operations need a stable identifier, so fetches must carry the UID
// through onto the returned Email.
func TestFetchedMessagesCarryUIDAndMailbox(t *testing.T) {
	s := startFakeIMAP(t)
	f := idleReceiver(s)
	f.Mailbox = "Archive"

	emails, err := f.Receive(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) == 0 {
		t.Fatal("expected at least one message")
	}
	if emails[0].UID != 101 {
		t.Errorf("UID = %d, want 101; without it nothing can act on the message", emails[0].UID)
	}
	if emails[0].Mailbox != "Archive" {
		t.Errorf("Mailbox = %q, want Archive", emails[0].Mailbox)
	}

	if got := UIDsOf(emails); len(got) != 1 || got[0] != 101 {
		t.Errorf("UIDsOf = %v, want [101]", got)
	}
}

func TestUIDsOfSkipsMessagesWithoutOne(t *testing.T) {
	got := UIDsOf([]gsmail.Email{{UID: 7}, {UID: 0}, {UID: 9}})
	if len(got) != 2 || got[0] != 7 || got[1] != 9 {
		t.Errorf("UIDsOf = %v, want [7 9]", got)
	}
}

func TestFlagOperations(t *testing.T) {
	tests := []struct {
		name string
		call func(*Receiver) error
	}{
		{"MarkSeen", func(f *Receiver) error { return f.MarkSeen(context.Background(), 101) }},
		{"MarkUnseen", func(f *Receiver) error { return f.MarkUnseen(context.Background(), 101) }},
		{"MarkDeleted", func(f *Receiver) error { return f.MarkDeleted(context.Background(), 101) }},
		{"Flag", func(f *Receiver) error { return f.Flag(context.Background(), []string{"\\Flagged"}, 101) }},
		{"Unflag", func(f *Receiver) error { return f.Unflag(context.Background(), []string{"\\Flagged"}, 101) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := startFakeIMAP(t)
			if err := tt.call(idleReceiver(s)); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if !s.sawCommand("UID STORE") {
				t.Errorf("%s did not issue UID STORE; saw %v", tt.name, s.seen())
			}
		})
	}
}

// IMAP deletion is two steps. Setting \Deleted alone leaves the message in
// place and visible to other clients.
func TestDeleteExpunges(t *testing.T) {
	s := startFakeIMAP(t)

	if err := idleReceiver(s).Delete(context.Background(), 101); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !s.sawCommand("UID STORE") {
		t.Error("Delete did not set the \\Deleted flag")
	}
	if !s.sawCommand("EXPUNGE") {
		t.Error("Delete set the flag but never expunged; the message is still there")
	}
}

func TestMarkDeletedDoesNotExpunge(t *testing.T) {
	s := startFakeIMAP(t)

	if err := idleReceiver(s).MarkDeleted(context.Background(), 101); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if s.sawCommand("EXPUNGE") {
		t.Error("MarkDeleted must leave the expunge to the caller")
	}
}

func TestMoveUsesMoveExtensionWhenAvailable(t *testing.T) {
	s := startFakeIMAP(t)
	s.mu.Lock()
	s.supportsMove = true
	s.mu.Unlock()

	if err := idleReceiver(s).Move(context.Background(), "Archive", 101); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !s.sawCommand("UID MOVE") {
		t.Errorf("expected UID MOVE, saw %v", s.seen())
	}
	if s.sawCommand("UID COPY") {
		t.Error("the copy fallback should not run when MOVE is supported")
	}
}

func TestMoveFallsBackToCopyThenDelete(t *testing.T) {
	s := startFakeIMAP(t) // no MOVE in CAPABILITY

	if err := idleReceiver(s).Move(context.Background(), "Archive", 101); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !s.sawCommand("UID COPY") {
		t.Errorf("expected the UID COPY fallback, saw %v", s.seen())
	}
	if !s.sawCommand("UID STORE") || !s.sawCommand("EXPUNGE") {
		t.Error("the fallback must also mark deleted and expunge, or the message is duplicated")
	}
}

func TestMoveRequiresADestination(t *testing.T) {
	s := startFakeIMAP(t)

	err := idleReceiver(s).Move(context.Background(), "", 101)
	if err == nil {
		t.Fatal("expected an error for an empty destination")
	}
	if gsmail.IsRetryable(err) {
		t.Error("a missing destination is a programming error, not a transient one")
	}
}

// An empty SeqSet is not a no-op in IMAP; it can be read as "everything".
// Refusing it is the difference between doing nothing and deleting a mailbox.
func TestOperationsRefuseAnEmptyUIDSet(t *testing.T) {
	s := startFakeIMAP(t)
	f := idleReceiver(s)

	ops := map[string]func() error{
		"MarkSeen":    func() error { return f.MarkSeen(context.Background()) },
		"Delete":      func() error { return f.Delete(context.Background()) },
		"MarkDeleted": func() error { return f.MarkDeleted(context.Background()) },
		"Move":        func() error { return f.Move(context.Background(), "Archive") },
	}
	for name, op := range ops {
		err := op()
		if !errors.Is(err, ErrNoUIDs) {
			t.Errorf("%s with no UIDs = %v, want ErrNoUIDs", name, err)
		}
		if gsmail.IsRetryable(err) {
			t.Errorf("%s: an empty UID set is permanent", name)
		}
	}
	if s.sawCommand("UID STORE") || s.sawCommand("EXPUNGE") {
		t.Error("an empty UID set must not reach the server at all")
	}
}

// UID 0 means the message did not come from a fetch that recorded one.
// Sending it to the server would address something unintended.
func TestOperationsRefuseZeroUID(t *testing.T) {
	s := startFakeIMAP(t)

	err := idleReceiver(s).MarkSeen(context.Background(), 101, 0)
	if err == nil {
		t.Fatal("expected an error for UID 0")
	}
	if !strings.Contains(err.Error(), "UID 0") {
		t.Errorf("got %v, want an error naming the invalid UID", err)
	}
	if s.sawCommand("UID STORE") {
		t.Error("nothing should reach the server when a UID is invalid")
	}
}

func TestMailboxes(t *testing.T) {
	s := startFakeIMAP(t)

	names, err := idleReceiver(s).Mailboxes(context.Background())
	if err != nil {
		t.Fatalf("Mailboxes: %v", err)
	}
	want := map[string]bool{"INBOX": false, "Archive": false, "Work/Reports": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("mailbox %q missing from %v", name, names)
		}
	}
}

// The read-then-act loop the package could not previously express.
func TestFetchThenMarkSeen(t *testing.T) {
	s := startFakeIMAP(t)
	f := idleReceiver(s)

	emails, err := f.Receive(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	uids := UIDsOf(emails)
	if len(uids) == 0 {
		t.Fatal("no UIDs to act on")
	}
	if err := f.MarkSeen(context.Background(), uids...); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if !s.sawCommand("UID STORE") {
		t.Error("the follow-up MarkSeen never reached the server")
	}
}
