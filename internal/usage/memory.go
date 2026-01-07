package usage

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore implements Store using in-memory storage.
type MemoryStore struct {
	mu     sync.RWMutex
	events []Event
}

// NewMemoryStore creates a new in-memory usage store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		events: make([]Event, 0),
	}
}

// Store saves events to memory.
func (s *MemoryStore) Store(ctx context.Context, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.events = append(s.events, events...)
	return nil
}

// Query retrieves events matching the params.
func (s *MemoryStore) Query(ctx context.Context, params QueryParams) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Event

	for _, e := range s.events {
		if !s.matches(e, params) {
			continue
		}
		result = append(result, e)
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})

	// Apply offset and limit
	if params.Offset > 0 {
		if params.Offset >= len(result) {
			return []Event{}, nil
		}
		result = result[params.Offset:]
	}
	if params.Limit > 0 && params.Limit < len(result) {
		result = result[:params.Limit]
	}

	return result, nil
}

// GetSummary returns aggregated statistics.
func (s *MemoryStore) GetSummary(ctx context.Context, tenantID, userID string, start, end time.Time) (Summary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summary := Summary{
		TenantID:      tenantID,
		UserID:        userID,
		PeriodStart:   start,
		PeriodEnd:     end,
		ToolBreakdown: make(map[string]int64),
	}

	var totalDuration time.Duration

	for _, e := range s.events {
		// Filter by tenant
		if tenantID != "" && e.TenantID != tenantID {
			continue
		}
		// Filter by user
		if userID != "" && e.UserID != userID {
			continue
		}
		// Filter by time range
		if !start.IsZero() && e.Timestamp.Before(start) {
			continue
		}
		if !end.IsZero() && e.Timestamp.After(end) {
			continue
		}

		summary.TotalEvents++
		if e.Success {
			summary.SuccessCount++
		} else {
			summary.ErrorCount++
		}
		summary.TotalTokensIn += e.TokensIn
		summary.TotalTokensOut += e.TokensOut
		totalDuration += e.Duration

		if e.ToolName != "" {
			summary.ToolBreakdown[e.ToolName]++
		}
	}

	summary.TotalDuration = totalDuration
	if summary.TotalEvents > 0 {
		summary.AvgDuration = totalDuration / time.Duration(summary.TotalEvents)
	}

	return summary, nil
}

// DeleteOlderThan removes events older than the given time.
func (s *MemoryStore) DeleteOlderThan(ctx context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var deleted int64
	var kept []Event

	for _, e := range s.events {
		if e.Timestamp.Before(before) {
			deleted++
		} else {
			kept = append(kept, e)
		}
	}

	s.events = kept
	return deleted, nil
}

// Close releases resources.
func (s *MemoryStore) Close() error {
	return nil
}

// matches checks if an event matches the query params.
func (s *MemoryStore) matches(e Event, p QueryParams) bool {
	if p.TenantID != "" && e.TenantID != p.TenantID {
		return false
	}
	if p.UserID != "" && e.UserID != p.UserID {
		return false
	}
	if p.ToolName != "" && e.ToolName != p.ToolName {
		return false
	}
	if !p.StartTime.IsZero() && e.Timestamp.Before(p.StartTime) {
		return false
	}
	if !p.EndTime.IsZero() && e.Timestamp.After(p.EndTime) {
		return false
	}
	return true
}

// Reset clears all data (for testing).
func (s *MemoryStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = make([]Event, 0)
}

// Count returns the total number of stored events (for testing).
func (s *MemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.events)
}
