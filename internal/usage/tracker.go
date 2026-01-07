package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/logger"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/storage"
)

// DefaultTracker implements the Tracker interface with buffered async collection.
type DefaultTracker struct {
	store   Store
	cfg     Config
	enabled bool

	// Buffering
	buffer    []Event
	bufferMu  sync.Mutex
	flushChan chan struct{}
	done      chan struct{}
	wg        sync.WaitGroup

	// Retry state
	retryBuffer []Event
	retryMu     sync.Mutex
	dlqFile     *os.File
	dlqMu       sync.Mutex
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
	var err error

	switch cfg.Store {
	case "memory", "":
		store = NewMemoryStore()
	case "postgres":
		store, err = newPostgresStoreFromConfig(cfg)
		if err != nil {
			return nil, err
		}
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

	// Close DLQ file if open
	t.dlqMu.Lock()
	if t.dlqFile != nil {
		_ = t.dlqFile.Close()
		t.dlqFile = nil
	}
	t.dlqMu.Unlock()

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

// flush writes buffered events to the store with retry and DLQ support.
func (t *DefaultTracker) flush() {
	start := time.Now()
	defer func() {
		metrics.UsageFlushDuration.Observe(time.Since(start).Seconds())
	}()

	// Collect events from main buffer
	t.bufferMu.Lock()
	if len(t.buffer) == 0 {
		t.bufferMu.Unlock()
		return
	}
	events := t.buffer
	t.buffer = make([]Event, 0, t.cfg.BufferSize)
	t.bufferMu.Unlock()

	// Also include any events pending retry
	t.retryMu.Lock()
	if len(t.retryBuffer) > 0 {
		events = append(t.retryBuffer, events...)
		t.retryBuffer = nil
	}
	t.retryMu.Unlock()

	// Try to store with exponential backoff
	if err := t.storeWithRetry(events); err != nil {
		// All retries failed, send to DLQ
		t.sendToDLQ(events)
	}
}

// storeWithRetry attempts to store events with exponential backoff.
func (t *DefaultTracker) storeWithRetry(events []Event) error {
	delay := t.cfg.RetryDelay
	if delay == 0 {
		delay = time.Second
	}

	maxRetries := t.cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			metrics.UsageEventsRetriedTotal.Add(float64(len(events)))
			logger.Warn("retrying usage event storage",
				"attempt", attempt,
				"event_count", len(events),
				"delay", delay)
			time.Sleep(delay)
			delay *= 2 // Exponential backoff
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := t.store.Store(ctx, events)
		cancel()

		if err == nil {
			metrics.UsageEventsStoredTotal.Add(float64(len(events)))
			if attempt > 0 {
				logger.Info("usage events stored after retry",
					"attempt", attempt,
					"event_count", len(events))
			}
			return nil
		}

		lastErr = err
		metrics.UsageEventsFailedTotal.Add(float64(len(events)))
		logger.Error("failed to store usage events",
			"error", err,
			"attempt", attempt,
			"event_count", len(events))
	}

	return lastErr
}

// sendToDLQ writes failed events to the dead-letter queue file.
func (t *DefaultTracker) sendToDLQ(events []Event) {
	if len(events) == 0 {
		return
	}

	dlqPath := t.cfg.DLQPath
	if dlqPath == "" {
		dlqPath = "/tmp/fi-mcp-usage-dlq.jsonl"
	}

	t.dlqMu.Lock()
	defer t.dlqMu.Unlock()

	// Open or create DLQ file (append mode)
	if t.dlqFile == nil {
		f, err := os.OpenFile(dlqPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			logger.Error("failed to open DLQ file, events will be lost",
				"error", err,
				"path", dlqPath,
				"event_count", len(events))
			return
		}
		t.dlqFile = f
	}

	encoder := json.NewEncoder(t.dlqFile)
	written := 0
	for _, event := range events {
		dlqEntry := map[string]any{
			"event":     event,
			"failed_at": time.Now().UTC(),
			"reason":    "max_retries_exceeded",
		}
		if err := encoder.Encode(dlqEntry); err != nil {
			logger.Error("failed to write event to DLQ",
				"error", err,
				"event_id", event.ID)
			continue
		}
		written++
	}

	metrics.UsageEventsDLQTotal.Add(float64(written))
	logger.Warn("events written to dead-letter queue",
		"count", written,
		"path", dlqPath)
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

// newPostgresStoreFromConfig creates a Postgres store from configuration.
func newPostgresStoreFromConfig(cfg Config) (*PostgresStore, error) {
	pgCfg := storage.PostgresConfig{
		URL: cfg.PostgresURL,
	}

	pg, err := storage.NewPostgres(pgCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres client: %w", err)
	}

	ctx := context.Background()
	if err := pg.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	// Run migrations
	if err := pg.MigrateUsageSchema(ctx); err != nil {
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return NewPostgresStore(pg), nil
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
