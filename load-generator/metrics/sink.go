package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sink receives finalized buckets. Write must never block the load loop.
type Sink interface {
	Write(b Bucket)
	// Close flushes pending buckets, giving up when ctx is done.
	Close(ctx context.Context) error
}

// JSONLSink writes one JSON object per line. Intended for local runs where
// the writer is a file or stdout.
type JSONLSink struct {
	mu  sync.Mutex
	enc *json.Encoder
	err error
}

// NewJSONLSink returns a sink writing to w.
func NewJSONLSink(w io.Writer) *JSONLSink { return &JSONLSink{enc: json.NewEncoder(w)} }

// Write encodes b as a JSON line.
func (s *JSONLSink) Write(b Bucket) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enc.Encode(b); err != nil && s.err == nil {
		s.err = err
	}
}

// Close returns the first write error, if any.
func (s *JSONLSink) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// WriteFunc persists one bucket (e.g. one Firestore document).
type WriteFunc func(ctx context.Context, b Bucket) error

// AsyncSinkOptions configure NewAsyncSink.
type AsyncSinkOptions struct {
	// Capacity bounds the queue; when full the oldest pending bucket is dropped.
	Capacity     int
	MaxAttempts  int
	WriteTimeout time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	Logger       *slog.Logger
}

// AsyncSink writes buckets from a bounded in-memory queue on its own
// goroutine and retries failed writes with backoff. Write never blocks.
type AsyncSink struct {
	write  WriteFunc
	opts   AsyncSinkOptions
	mu     sync.Mutex
	queue  []pending
	closed bool
	notify chan struct{}
	abort  chan struct{}
	done   chan struct{}

	written atomic.Int64
	dropped atomic.Int64
	failed  atomic.Int64
}

type pending struct {
	b         Bucket
	attempts  int
	notBefore time.Time
}

// NewAsyncSink starts the background writer.
func NewAsyncSink(write WriteFunc, opts AsyncSinkOptions) *AsyncSink {
	if opts.Capacity <= 0 {
		opts.Capacity = 900
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 8
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = 5 * time.Second
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = 250 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 10 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	s := &AsyncSink{
		write:  write,
		opts:   opts,
		notify: make(chan struct{}, 1),
		abort:  make(chan struct{}),
		done:   make(chan struct{}),
	}
	go s.loop()
	return s
}

// Write enqueues b without blocking.
func (s *AsyncSink) Write(b Bucket) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.enqueueLocked(pending{b: b})
	s.mu.Unlock()
	s.wake()
}

func (s *AsyncSink) enqueueLocked(p pending) {
	if len(s.queue) >= s.opts.Capacity {
		s.queue = s.queue[1:]
		s.dropped.Add(1)
		s.opts.Logger.Warn("traffic bucket queue full; dropped oldest bucket")
	}
	s.queue = append(s.queue, p)
}

func (s *AsyncSink) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *AsyncSink) next() (pending, bool) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			p := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return p, true
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return pending{}, false
		}
		select {
		case <-s.notify:
		case <-s.abort:
			return pending{}, false
		}
	}
}

func (s *AsyncSink) loop() {
	defer close(s.done)
	for {
		p, ok := s.next()
		if !ok {
			return
		}
		if wait := time.Until(p.notBefore); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-s.abort:
				t.Stop()
				s.mu.Lock()
				s.queue = append(s.queue, p) // keep it visible as unwritten
				s.mu.Unlock()
				return
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.WriteTimeout)
		err := s.write(ctx, p.b)
		cancel()
		if err == nil {
			s.written.Add(1)
			continue
		}
		p.attempts++
		if p.attempts >= s.opts.MaxAttempts {
			s.failed.Add(1)
			s.opts.Logger.Error("giving up on traffic bucket", "second", p.b.Second, "attempts", p.attempts, "err", err)
			continue
		}
		backoff := min(s.opts.BaseBackoff<<(p.attempts-1), s.opts.MaxBackoff)
		p.notBefore = time.Now().Add(backoff)
		s.opts.Logger.Warn("traffic bucket write failed; will retry", "second", p.b.Second, "attempt", p.attempts, "err", err)
		s.mu.Lock()
		s.enqueueLocked(p)
		s.mu.Unlock()
	}
}

// Close stops accepting buckets and waits until the queue is drained or ctx
// is done.
func (s *AsyncSink) Close(ctx context.Context) error {
	s.mu.Lock()
	alreadyClosed := s.closed
	s.closed = true
	s.mu.Unlock()
	s.wake()
	if alreadyClosed {
		<-s.done
		return nil
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		close(s.abort)
		<-s.done
	}
	s.mu.Lock()
	unwritten := len(s.queue)
	s.mu.Unlock()
	if lost := int64(unwritten) + s.dropped.Load() + s.failed.Load(); lost > 0 {
		return fmt.Errorf("%d traffic buckets were not persisted", lost)
	}
	return nil
}

// Stats returns written, dropped (queue overflow) and failed (retries exhausted) counts.
func (s *AsyncSink) Stats() (written, dropped, failed int64) {
	return s.written.Load(), s.dropped.Load(), s.failed.Load()
}

// MultiSink fans a bucket out to several sinks.
type MultiSink []Sink

// Write forwards b to every sink.
func (m MultiSink) Write(b Bucket) {
	for _, s := range m {
		s.Write(b)
	}
}

// Close closes every sink and returns the first error.
func (m MultiSink) Close(ctx context.Context) error {
	var first error
	for _, s := range m {
		if err := s.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
