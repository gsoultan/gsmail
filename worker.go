package gsmail

import (
	"context"
	"errors"
	"sync"
)

// ErrSenderStopped is returned by Send once the BackgroundSender is stopped.
var ErrSenderStopped = errors.New("gsmail: background sender is stopped")

// ErrQueueFull is returned by Send when the queue cannot accept more work.
var ErrQueueFull = errors.New("gsmail: background sender queue is full")

// BackgroundSender handles asynchronous email sending with a worker pool.
//
// The zero value is not usable; construct one with NewBackgroundSender.
// Send is safe for concurrent use and never panics, including after Stop.
type BackgroundSender struct {
	sender  Sender
	queue   chan Email
	errChan chan BackgroundSendError
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu      sync.RWMutex
	stopped bool

	stopOnce sync.Once
}

// BackgroundSendError represents an error that occurred during asynchronous sending.
type BackgroundSendError struct {
	Email Email
	Err   error
}

// NewBackgroundSender creates a new BackgroundSender.
func NewBackgroundSender(sender Sender, bufferSize int) *BackgroundSender {
	if bufferSize < 0 {
		bufferSize = 0
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &BackgroundSender{
		sender:  sender,
		queue:   make(chan Email, bufferSize),
		errChan: make(chan BackgroundSendError, bufferSize),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start launches the specified number of worker goroutines.
func (s *BackgroundSender) Start(numWorkers int) {
	if numWorkers < 1 {
		numWorkers = 1
	}
	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
}

// Send adds an email to the background sending queue.
// It returns false if the sender is stopped or the queue is full.
func (s *BackgroundSender) Send(email Email) bool {
	return s.TrySend(email) == nil
}

// TrySend adds an email to the background sending queue and reports why it
// could not be queued: ErrSenderStopped or ErrQueueFull.
func (s *BackgroundSender) TrySend(email Email) error {
	// The read lock is held across the channel send so that Stop cannot close
	// the queue underneath us, which would panic.
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.stopped {
		return ErrSenderStopped
	}

	select {
	case s.queue <- email:
		return nil
	default:
		return ErrQueueFull
	}
}

// Errors returns a channel for receiving background sending errors.
// The channel is closed once Stop has drained the queue.
func (s *BackgroundSender) Errors() <-chan BackgroundSendError {
	return s.errChan
}

// Stop gracefully stops all workers after the queued emails have been sent.
// It is idempotent and safe to call concurrently with Send.
//
// Emails already queued are delivered; the shared context is only cancelled
// after every worker has finished, so a send in flight is not aborted. Use
// StopNow to abandon queued work instead.
func (s *BackgroundSender) Stop() {
	s.stopOnce.Do(func() {
		s.markStopped()
		s.wg.Wait()
		// Cancel only after the workers are done, so queued and in-flight
		// sends are not failed with context.Canceled.
		s.cancel()
		close(s.errChan)
	})
}

// StopNow cancels in-flight sends and discards anything still queued.
// It is idempotent and safe to call concurrently with Send.
func (s *BackgroundSender) StopNow() {
	s.stopOnce.Do(func() {
		s.markStopped()
		s.cancel()
		s.wg.Wait()
		close(s.errChan)
	})
}

// markStopped closes the queue exactly once, under the write lock, so that a
// concurrent Send can never write to a closed channel.
func (s *BackgroundSender) markStopped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	close(s.queue)
}

func (s *BackgroundSender) worker() {
	defer s.wg.Done()
	for email := range s.queue {
		if err := s.sender.Send(s.ctx, email); err != nil {
			select {
			case s.errChan <- BackgroundSendError{Email: email, Err: err}:
			default:
				// If errChan is full, we drop the error to avoid blocking the worker
			}
		}
	}
}
