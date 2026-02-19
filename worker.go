package gsmail

import (
	"context"
	"sync"
)

// BackgroundSender handles asynchronous email sending with a worker pool.
type BackgroundSender struct {
	sender  Sender
	queue   chan Email
	errChan chan BackgroundSendError
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// BackgroundSendError represents an error that occurred during asynchronous sending.
type BackgroundSendError struct {
	Email Email
	Err   error
}

// NewBackgroundSender creates a new BackgroundSender.
func NewBackgroundSender(sender Sender, bufferSize int) *BackgroundSender {
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
	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
}

// Send adds an email to the background sending queue.
// It returns false if the queue is full and cannot accept the email.
func (s *BackgroundSender) Send(email Email) bool {
	select {
	case s.queue <- email:
		return true
	default:
		return false
	}
}

// Errors returns a channel for receiving background sending errors.
func (s *BackgroundSender) Errors() <-chan BackgroundSendError {
	return s.errChan
}

// Stop gracefully stops all workers after processing the remaining queue.
func (s *BackgroundSender) Stop() {
	s.cancel()
	close(s.queue)
	s.wg.Wait()
	close(s.errChan)
}

func (s *BackgroundSender) worker() {
	defer s.wg.Done()
	for email := range s.queue {
		// Respect global cancellation while sending.
		if err := s.sender.Send(s.ctx, email); err != nil {
			select {
			case s.errChan <- BackgroundSendError{Email: email, Err: err}:
			default:
				// If errChan is full, we drop the error to avoid blocking the worker
			}
		}
	}
}
