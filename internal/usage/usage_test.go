package usage

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestMemoryStore_StoreAndQuery(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	now := time.Now()
	events := []Event{
		{ID: "1", TenantID: "tenant-1", UserID: "user-1", ToolName: "tool-a", Timestamp: now, Success: true},
		{ID: "2", TenantID: "tenant-1", UserID: "user-1", ToolName: "tool-b", Timestamp: now.Add(-time.Hour), Success: true},
		{ID: "3", TenantID: "tenant-1", UserID: "user-2", ToolName: "tool-a", Timestamp: now.Add(-2 * time.Hour), Success: false},
		{ID: "4", TenantID: "tenant-2", UserID: "user-1", ToolName: "tool-a", Timestamp: now.Add(-3 * time.Hour), Success: true},
	}

	if err := store.Store(ctx, events); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Query all
	result, err := store.Query(ctx, QueryParams{})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 4 {
		t.Errorf("Query all: got %d events, want 4", len(result))
	}

	// Query by tenant
	result, err = store.Query(ctx, QueryParams{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("Query by tenant failed: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("Query by tenant: got %d events, want 3", len(result))
	}

	// Query by user
	result, err = store.Query(ctx, QueryParams{TenantID: "tenant-1", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Query by user failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Query by user: got %d events, want 2", len(result))
	}

	// Query by tool
	result, err = store.Query(ctx, QueryParams{ToolName: "tool-a"})
	if err != nil {
		t.Fatalf("Query by tool failed: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("Query by tool: got %d events, want 3", len(result))
	}

	// Query with limit
	result, err = store.Query(ctx, QueryParams{Limit: 2})
	if err != nil {
		t.Fatalf("Query with limit failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Query with limit: got %d events, want 2", len(result))
	}

	// Query with offset
	result, err = store.Query(ctx, QueryParams{Offset: 2})
	if err != nil {
		t.Fatalf("Query with offset failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Query with offset: got %d events, want 2", len(result))
	}
}

func TestMemoryStore_GetSummary(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	now := time.Now()
	events := []Event{
		{ID: "1", TenantID: "tenant-1", UserID: "user-1", ToolName: "tool-a", Duration: 100 * time.Millisecond, TokensIn: 10, TokensOut: 20, Success: true, Timestamp: now},
		{ID: "2", TenantID: "tenant-1", UserID: "user-1", ToolName: "tool-a", Duration: 200 * time.Millisecond, TokensIn: 15, TokensOut: 25, Success: true, Timestamp: now},
		{ID: "3", TenantID: "tenant-1", UserID: "user-1", ToolName: "tool-b", Duration: 50 * time.Millisecond, TokensIn: 5, TokensOut: 10, Success: false, Timestamp: now},
	}

	store.Store(ctx, events)

	summary, err := store.GetSummary(ctx, "tenant-1", "user-1", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}

	if summary.TotalEvents != 3 {
		t.Errorf("TotalEvents = %d, want 3", summary.TotalEvents)
	}
	if summary.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d, want 2", summary.SuccessCount)
	}
	if summary.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", summary.ErrorCount)
	}
	if summary.TotalTokensIn != 30 {
		t.Errorf("TotalTokensIn = %d, want 30", summary.TotalTokensIn)
	}
	if summary.TotalTokensOut != 55 {
		t.Errorf("TotalTokensOut = %d, want 55", summary.TotalTokensOut)
	}
	if summary.TotalDuration != 350*time.Millisecond {
		t.Errorf("TotalDuration = %v, want 350ms", summary.TotalDuration)
	}
	if summary.ToolBreakdown["tool-a"] != 2 {
		t.Errorf("ToolBreakdown[tool-a] = %d, want 2", summary.ToolBreakdown["tool-a"])
	}
}

func TestMemoryStore_DeleteOlderThan(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	now := time.Now()
	events := []Event{
		{ID: "1", TenantID: "t", Timestamp: now},
		{ID: "2", TenantID: "t", Timestamp: now.Add(-24 * time.Hour)},
		{ID: "3", TenantID: "t", Timestamp: now.Add(-48 * time.Hour)},
	}

	store.Store(ctx, events)

	deleted, err := store.DeleteOlderThan(ctx, now.Add(-36*time.Hour))
	if err != nil {
		t.Fatalf("DeleteOlderThan failed: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	if store.Count() != 2 {
		t.Errorf("remaining = %d, want 2", store.Count())
	}
}

func TestTracker_Track(t *testing.T) {
	tracker, err := New(Config{
		Enabled:       true,
		Store:         "memory",
		BufferSize:    10,
		FlushInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New tracker failed: %v", err)
	}
	defer tracker.Close()

	ctx := context.Background()

	// Track some events
	for i := 0; i < 5; i++ {
		tracker.Track(ctx, Event{
			TenantID: "tenant-1",
			UserID:   "user-1",
			ToolName: "test-tool",
			Success:  true,
		})
	}

	// Wait for flush
	time.Sleep(200 * time.Millisecond)

	// Query should return tracked events
	events, err := tracker.Query(ctx, QueryParams{TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(events) != 5 {
		t.Errorf("Query returned %d events, want 5", len(events))
	}
}

func TestTracker_BufferFlush(t *testing.T) {
	tracker, err := New(Config{
		Enabled:       true,
		Store:         "memory",
		BufferSize:    3,         // Small buffer
		FlushInterval: time.Hour, // Long interval to test buffer flush
	})
	if err != nil {
		t.Fatalf("New tracker failed: %v", err)
	}
	defer tracker.Close()

	ctx := context.Background()

	// Track events to fill buffer
	for i := 0; i < 3; i++ {
		tracker.Track(ctx, Event{TenantID: "t"})
	}

	// Buffer should flush when full
	time.Sleep(100 * time.Millisecond)

	events, _ := tracker.Query(ctx, QueryParams{TenantID: "t"})
	if len(events) != 3 {
		t.Errorf("Buffer flush: got %d events, want 3", len(events))
	}
}

func TestTracker_Disabled(t *testing.T) {
	tracker, err := New(Config{Enabled: false})
	if err != nil {
		t.Fatalf("New tracker failed: %v", err)
	}

	ctx := context.Background()
	tracker.Track(ctx, Event{TenantID: "t"})

	events, _ := tracker.Query(ctx, QueryParams{TenantID: "t"})
	if len(events) != 0 {
		t.Errorf("Disabled tracker should not store events")
	}
}

func TestExporter_JSON(t *testing.T) {
	tracker, _ := New(Config{
		Enabled:       true,
		Store:         "memory",
		BufferSize:    100,
		FlushInterval: time.Hour,
	})
	defer tracker.Close()

	ctx := context.Background()
	tracker.Track(ctx, Event{
		TenantID: "tenant-1",
		UserID:   "user-1",
		ToolName: "test-tool",
		Duration: 100 * time.Millisecond,
		Success:  true,
	})
	tracker.flush()

	exporter := NewExporter(tracker)
	var buf bytes.Buffer

	err := exporter.Export(&buf, QueryParams{TenantID: "tenant-1"}, FormatJSON)
	if err != nil {
		t.Fatalf("Export JSON failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `"tenant_id"`) {
		t.Error("JSON export should contain tenant_id")
	}
	if !strings.Contains(output, `"test-tool"`) {
		t.Error("JSON export should contain tool name")
	}
}

func TestExporter_CSV(t *testing.T) {
	tracker, _ := New(Config{
		Enabled:       true,
		Store:         "memory",
		BufferSize:    100,
		FlushInterval: time.Hour,
	})
	defer tracker.Close()

	ctx := context.Background()
	tracker.Track(ctx, Event{
		TenantID: "tenant-1",
		UserID:   "user-1",
		ToolName: "test-tool",
		Success:  true,
	})
	tracker.flush()

	exporter := NewExporter(tracker)
	var buf bytes.Buffer

	err := exporter.Export(&buf, QueryParams{TenantID: "tenant-1"}, FormatCSV)
	if err != nil {
		t.Fatalf("Export CSV failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "id,timestamp,tenant_id") {
		t.Error("CSV export should contain header")
	}
	if !strings.Contains(output, "tenant-1") {
		t.Error("CSV export should contain tenant_id value")
	}
}

func TestExporter_SummaryJSON(t *testing.T) {
	tracker, _ := New(Config{
		Enabled:       true,
		Store:         "memory",
		BufferSize:    100,
		FlushInterval: time.Hour,
	})
	defer tracker.Close()

	ctx := context.Background()
	tracker.Track(ctx, Event{TenantID: "t", UserID: "u", Success: true, TokensIn: 10})
	tracker.Track(ctx, Event{TenantID: "t", UserID: "u", Success: false, TokensIn: 5})
	tracker.flush()

	exporter := NewExporter(tracker)
	var buf bytes.Buffer

	err := exporter.ExportSummary(&buf, "t", "u", time.Time{}, time.Time{}, FormatJSON)
	if err != nil {
		t.Fatalf("ExportSummary JSON failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `"total_events"`) {
		t.Error("Summary should contain total_events")
	}
	if !strings.Contains(output, `"success_rate"`) {
		t.Error("Summary should contain success_rate")
	}
}

func TestNoopTracker(t *testing.T) {
	tracker := NewNoopTracker()

	ctx := context.Background()
	tracker.Track(ctx, Event{TenantID: "t"})

	events, err := tracker.Query(ctx, QueryParams{})
	if err != nil {
		t.Errorf("NoopTracker.Query should not error: %v", err)
	}
	if len(events) != 0 {
		t.Error("NoopTracker should return empty results")
	}

	if err := tracker.Close(); err != nil {
		t.Errorf("NoopTracker.Close should not error: %v", err)
	}
}
