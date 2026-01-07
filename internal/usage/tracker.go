package usage

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultTracker implements the Tracker interface with buffered async collection.
type DefaultTracker struct {
	store         Store
	cfg           Config
	enabled       bool

	// Buffering
	buffer        []Event
	bufferMu      sync.Mutex
	flushChan     chan struct{}
	done          chan struct{}
	wg            sync.WaitGroup
}

// New creates a new usage tracker from configuration.
func New(cfg Config) (*DefaultTracker, error) {
	if !cfg.Enabled {
		return &DefaultTracker{
			cfg:     cfg,
			enabled: false,
		}, nil
	}

	var store Store

	switch cfg.Store {
	case "memory", "":
		store = NewMemoryStore()
	// case "postgres":
	// 	PostgresStore to be implemented
	default:
		return nil, fmt.Errorf("unknown usage store: %s", cfg.Store)
	}

	t := &DefaultTracker{
		store:     store,
		cfg:       cfg,
		enabled:   true,
		buffer:    make([]Event, 0, cfg.BufferSize),
		flushChan: make(chan struct{}, 1),
		done:      make(chan struct{}),
	}

	// Start background flush worker
	t.wg.Add(1)
	go t.flushWorker()

	return t, nil
}

// Track records a usage event asynchronously.
func (t *DefaultTracker) Track(ctx context.Context, event Event) {
	if !t.enabled {
		return
	}

	// Ensure event has ID and timestamp
	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	t.bufferMu.Lock()
	t.buffer = append(t.buffer, event)
	shouldFlush := len(t.buffer) >= t.cfg.BufferSize
	t.bufferMu.Unlock()

	if shouldFlush {
		t.triggerFlush()
	}
}

// Query retrieves usage events matching the given params.
func (t *DefaultTracker) Query(ctx context.Context, params QueryParams) ([]Event, error) {
	if !t.enabled {
		return nil, nil
	}

	// Flush pending events first for consistency
	t.flush()

	return t.store.Query(ctx, params)
}

// GetSummary returns aggregated usage statistics.
func (t *DefaultTracker) GetSummary(ctx context.Context, tenantID, userID string, start, end time.Time) (Summary, error) {
	if !t.enabled {
		return Summary{}, nil
	}

	// Flush pending events first for consistency
	t.flush()

	return t.store.GetSummary(ctx, tenantID, userID, start, end)
}

// Close shuts down the tracker and flushes pending events.
func (t *DefaultTracker) Close() error {
	if !t.enabled {
		return nil
	}

	// Signal shutdown
	close(t.done)
	t.wg.Wait()

	// Final flush
	t.flush()

	if t.store != nil {
		return t.store.Close()
	}
	return nil
}

// Enabled returns whether usage tracking is active.
func (t *DefaultTracker) Enabled() bool {
	return t.enabled
}

// triggerFlush signals the flush worker to flush.
func (t *DefaultTracker) triggerFlush() {
	select {
	case t.flushChan <- struct{}{}:
	default:
		// Already a flush pending
	}
}

// flush writes buffered events to the store.
func (t *DefaultTracker) flush() {
	t.bufferMu.Lock()
	if len(t.buffer) == 0 {
		t.bufferMu.Unlock()
		return
	}

	events := t.buffer
	t.buffer = make([]Event, 0, t.cfg.BufferSize)
	t.bufferMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := t.store.Store(ctx, events); err != nil {
		log.Printf("usage: failed to store %d events: %v", len(events), err)
		// TODO: Implement retry or dead-letter queue
	}
}

// flushWorker runs in the background and periodically flushes events.
func (t *DefaultTracker) flushWorker() {
	defer t.wg.Done()

	ticker := time.NewTicker(t.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			t.flush()
		case <-t.flushChan:
			t.flush()
		}
	}
}

// NoopTracker is a tracker that does nothing.
type NoopTracker struct{}

// NewNoopTracker creates a tracker that discards all events.
func NewNoopTracker() *NoopTracker {
	return &NoopTracker{}
}

func (NoopTracker) Track(ctx context.Context, event Event) {}

func (NoopTracker) Query(ctx context.Context, params QueryParams) ([]Event, error) {
	return nil, nil
}

func (NoopTracker) GetSummary(ctx context.Context, tenantID, userID string, start, end time.Time) (Summary, error) {
	return Summary{}, nil
}

func (NoopTracker) Close() error {
	return nil
}
