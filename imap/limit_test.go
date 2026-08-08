package imap

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gsoultan/gsmail"
)

// fetch used to size its channels and worker pool straight from the caller's
// limit. A negative limit panicked in make(chan), and a zero limit spawned no
// workers at all, so nothing drained the pipeline: the indexer blocked,
// c.Fetch blocked, and the final <-done hung forever with no context escape.
//
// Both are now rejected before any connection is opened.
func TestReceiveAndSearchRejectNonPositiveLimit(t *testing.T) {
	f := NewReceiver("imap.invalid", 993, "u", "p", true)

	for _, limit := range []int{0, -1, -1000} {
		t.Run(fmt.Sprintf("Receive/%d", limit), func(t *testing.T) {
			emails, err := f.Receive(context.Background(), limit)
			if !errors.Is(err, gsmail.ErrInvalidLimit) {
				t.Errorf("Receive(%d) = %v, want ErrInvalidLimit", limit, err)
			}
			if emails != nil {
				t.Errorf("Receive(%d) returned %d emails alongside the error", limit, len(emails))
			}
			if gsmail.IsRetryable(err) {
				t.Errorf("Receive(%d): a bad limit is permanent, not retryable", limit)
			}
		})

		t.Run(fmt.Sprintf("Search/%d", limit), func(t *testing.T) {
			emails, err := f.Search(context.Background(), gsmail.SearchOptions{}, limit)
			if !errors.Is(err, gsmail.ErrInvalidLimit) {
				t.Errorf("Search(%d) = %v, want ErrInvalidLimit", limit, err)
			}
			if emails != nil {
				t.Errorf("Search(%d) returned %d emails alongside the error", limit, len(emails))
			}
		})
	}
}

// The rejection happens before dialling, so an unreachable host is never
// contacted and the call returns immediately rather than after a timeout.
func TestInvalidLimitShortCircuitsBeforeDial(t *testing.T) {
	f := NewReceiver("192.0.2.1", 993, "u", "p", true) // TEST-NET-1, always unroutable

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // even a dead context must not change the outcome

	if _, err := f.Receive(ctx, 0); !errors.Is(err, gsmail.ErrInvalidLimit) {
		t.Errorf("got %v, want ErrInvalidLimit", err)
	}
}
