package usage

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
)

// ExportFormat defines the export format type.
type ExportFormat string

const (
	FormatJSON ExportFormat = "json"
	FormatCSV  ExportFormat = "csv"
)

// Exporter handles exporting usage data.
type Exporter struct {
	tracker Tracker
}

// NewExporter creates a new usage exporter.
func NewExporter(tracker Tracker) *Exporter {
	return &Exporter{tracker: tracker}
}

// Export writes usage events to the given writer in the specified format.
func (e *Exporter) Export(w io.Writer, params QueryParams, format ExportFormat) error {
	events, err := e.tracker.Query(context.Background(), params)
	if err != nil {
		return fmt.Errorf("query events: %w", err)
	}

	switch format {
	case FormatJSON:
		return e.exportJSON(w, events)
	case FormatCSV:
		return e.exportCSV(w, events)
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

// ExportSummary writes a usage summary to the given writer.
func (e *Exporter) ExportSummary(w io.Writer, tenantID, userID string, start, end time.Time, format ExportFormat) error {
	summary, err := e.tracker.GetSummary(context.Background(), tenantID, userID, start, end)
	if err != nil {
		return fmt.Errorf("get summary: %w", err)
	}

	switch format {
	case FormatJSON:
		return e.exportSummaryJSON(w, summary)
	case FormatCSV:
		return e.exportSummaryCSV(w, summary)
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func (e *Exporter) exportJSON(w io.Writer, events []Event) error {
	// Convert duration to milliseconds for JSON
	type jsonEvent struct {
		ID         string            `json:"id"`
		Timestamp  time.Time         `json:"timestamp"`
		TenantID   string            `json:"tenant_id"`
		UserID     string            `json:"user_id,omitempty"`
		ToolName   string            `json:"tool_name,omitempty"`
		ServerID   string            `json:"server_id,omitempty"`
		DurationMs int64             `json:"duration_ms"`
		TokensIn   int64             `json:"tokens_in,omitempty"`
		TokensOut  int64             `json:"tokens_out,omitempty"`
		Success    bool              `json:"success"`
		ErrorCode  string            `json:"error_code,omitempty"`
		Metadata   map[string]string `json:"metadata,omitempty"`
	}

	jsonEvents := make([]jsonEvent, len(events))
	for i, event := range events {
		jsonEvents[i] = jsonEvent{
			ID:         event.ID,
			Timestamp:  event.Timestamp,
			TenantID:   event.TenantID,
			UserID:     event.UserID,
			ToolName:   event.ToolName,
			ServerID:   event.ServerID,
			DurationMs: event.Duration.Milliseconds(),
			TokensIn:   event.TokensIn,
			TokensOut:  event.TokensOut,
			Success:    event.Success,
			ErrorCode:  event.ErrorCode,
			Metadata:   event.Metadata,
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"events":      jsonEvents,
		"count":       len(events),
		"exported_at": time.Now().UTC(),
	})
}

func (e *Exporter) exportCSV(w io.Writer, events []Event) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Header
	header := []string{
		"id",
		"timestamp",
		"tenant_id",
		"user_id",
		"tool_name",
		"server_id",
		"duration_ms",
		"tokens_in",
		"tokens_out",
		"success",
		"error_code",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	// Data rows
	for _, event := range events {
		row := []string{
			event.ID,
			event.Timestamp.Format(time.RFC3339),
			event.TenantID,
			event.UserID,
			event.ToolName,
			event.ServerID,
			strconv.FormatInt(event.Duration.Milliseconds(), 10),
			strconv.FormatInt(event.TokensIn, 10),
			strconv.FormatInt(event.TokensOut, 10),
			strconv.FormatBool(event.Success),
			event.ErrorCode,
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	return cw.Error()
}

func (e *Exporter) exportSummaryJSON(w io.Writer, summary Summary) error {
	// Convert duration to milliseconds for JSON
	type jsonSummary struct {
		TenantID        string           `json:"tenant_id"`
		UserID          string           `json:"user_id,omitempty"`
		PeriodStart     time.Time        `json:"period_start"`
		PeriodEnd       time.Time        `json:"period_end"`
		TotalEvents     int64            `json:"total_events"`
		SuccessCount    int64            `json:"success_count"`
		ErrorCount      int64            `json:"error_count"`
		SuccessRate     float64          `json:"success_rate"`
		TotalTokensIn   int64            `json:"total_tokens_in"`
		TotalTokensOut  int64            `json:"total_tokens_out"`
		TotalDurationMs int64            `json:"total_duration_ms"`
		AvgDurationMs   int64            `json:"avg_duration_ms"`
		ToolBreakdown   map[string]int64 `json:"tool_breakdown,omitempty"`
	}

	var successRate float64
	if summary.TotalEvents > 0 {
		successRate = float64(summary.SuccessCount) / float64(summary.TotalEvents) * 100
	}

	js := jsonSummary{
		TenantID:        summary.TenantID,
		UserID:          summary.UserID,
		PeriodStart:     summary.PeriodStart,
		PeriodEnd:       summary.PeriodEnd,
		TotalEvents:     summary.TotalEvents,
		SuccessCount:    summary.SuccessCount,
		ErrorCount:      summary.ErrorCount,
		SuccessRate:     successRate,
		TotalTokensIn:   summary.TotalTokensIn,
		TotalTokensOut:  summary.TotalTokensOut,
		TotalDurationMs: summary.TotalDuration.Milliseconds(),
		AvgDurationMs:   summary.AvgDuration.Milliseconds(),
		ToolBreakdown:   summary.ToolBreakdown,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"summary":     js,
		"exported_at": time.Now().UTC(),
	})
}

func (e *Exporter) exportSummaryCSV(w io.Writer, summary Summary) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Summary as key-value pairs
	rows := [][]string{
		{"metric", "value"},
		{"tenant_id", summary.TenantID},
		{"user_id", summary.UserID},
		{"period_start", summary.PeriodStart.Format(time.RFC3339)},
		{"period_end", summary.PeriodEnd.Format(time.RFC3339)},
		{"total_events", strconv.FormatInt(summary.TotalEvents, 10)},
		{"success_count", strconv.FormatInt(summary.SuccessCount, 10)},
		{"error_count", strconv.FormatInt(summary.ErrorCount, 10)},
		{"total_tokens_in", strconv.FormatInt(summary.TotalTokensIn, 10)},
		{"total_tokens_out", strconv.FormatInt(summary.TotalTokensOut, 10)},
		{"total_duration_ms", strconv.FormatInt(summary.TotalDuration.Milliseconds(), 10)},
		{"avg_duration_ms", strconv.FormatInt(summary.AvgDuration.Milliseconds(), 10)},
	}

	for _, row := range rows {
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	// Tool breakdown as separate section
	if len(summary.ToolBreakdown) > 0 {
		cw.Write([]string{"", ""})
		cw.Write([]string{"tool", "count"})
		for tool, count := range summary.ToolBreakdown {
			cw.Write([]string{tool, strconv.FormatInt(count, 10)})
		}
	}

	return cw.Error()
}
