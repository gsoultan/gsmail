// Package bufpool holds the shared byte-slice pool used when rendering
// messages and Outlook-compatible HTML.
//
// It exists so the root package and the outlook package can share one pool
// without either importing the other, which would be an import cycle.
package bufpool

import "sync"

const (
	// MaxSize bounds which buffers are worth keeping. It has to exceed a
	// realistic rendered message or the pool never recycles anything: at 4 KiB
	// even a small HTML mail outgrew it, so every render allocated from
	// scratch. Buffers above this (large attachments) are dropped so a single
	// big send does not pin megabytes per P for the life of the process.
	MaxSize = 64 << 10

	// InitialSize is the starting capacity of a fresh buffer, chosen to cover
	// a header block plus a small body without regrowing.
	InitialSize = 4096
)

var pool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, InitialSize)
		return &b
	},
}

// Get retrieves a byte slice from the pool.
func Get() *[]byte { return pool.Get().(*[]byte) }

// Put returns a byte slice to the pool if it is within the size limit.
func Put(b *[]byte) {
	if b == nil {
		return
	}
	if cap(*b) <= MaxSize {
		*b = (*b)[:0]
		pool.Put(b)
	}
}

// Writer implements io.Writer over a pooled byte slice.
type Writer struct{ BufPtr *[]byte }

// NewWriter creates a Writer for the given buffer.
func NewWriter(b *[]byte) *Writer { return &Writer{BufPtr: b} }

// Write appends data to the underlying buffer.
func (w *Writer) Write(p []byte) (int, error) {
	*w.BufPtr = append(*w.BufPtr, p...)
	return len(p), nil
}
