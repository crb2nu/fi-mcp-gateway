package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/storage"
)

// PostgresStore implements Store using PostgreSQL.
type PostgresStore struct {
	pg *storage.Postgres
}

// NewPostgresStore creates a new Postgres-backed usage store.
func NewPostgresStore(pg *storage.Postgres) *PostgresStore {
	return &PostgresStore{pg: pg}
}

// Store saves events to the database.
func (s *PostgresStore) Store(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.pg.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_events (id, tenant_id, user_id, tool_name, server_id, timestamp, duration_ns, tokens_in, tokens_out, success, error_code, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range events {
		var metadataJSON []byte
		if len(e.Metadata) > 0 {
			metadataJSON, _ = json.Marshal(e.Metadata)
		}

		var errorCode *string
		if e.ErrorCode != "" {
			errorCode = &e.ErrorCode
		}

		_, err := stmt.ExecContext(ctx,
			e.ID,
			e.TenantID,
			e.UserID,
			e.ToolName,
			e.ServerID,
			e.Timestamp,
			e.Duration.Nanoseconds(),
			e.TokensIn,
			e.TokensOut,
			e.Success,
			errorCode,
			metadataJSON,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Query retrieves events matching the params.
func (s *PostgresStore) Query(ctx context.Context, params QueryParams) ([]Event, error) {
	query := `
		SELECT id, tenant_id, user_id, tool_name, server_id, timestamp, duration_ns, tokens_in, tokens_out, success, error_code, metadata
		FROM usage_events
		WHERE 1=1
	`
	args := []any{}
	argIdx := 1

	if params.TenantID != "" {
		query += ` AND tenant_id = $` + itoa(argIdx)
		args = append(args, params.TenantID)
		argIdx++
	}
	if params.UserID != "" {
		query += ` AND user_id = $` + itoa(argIdx)
		args = append(args, params.UserID)
		argIdx++
	}
	if params.ToolName != "" {
		query += ` AND tool_name = $` + itoa(argIdx)
		args = append(args, params.ToolName)
		argIdx++
	}
	if !params.StartTime.IsZero() {
		query += ` AND timestamp >= $` + itoa(argIdx)
		args = append(args, params.StartTime)
		argIdx++
	}
	if !params.EndTime.IsZero() {
		query += ` AND timestamp <= $` + itoa(argIdx)
		args = append(args, params.EndTime)
		argIdx++
	}

	query += ` ORDER BY timestamp DESC`

	if params.Limit > 0 {
		query += ` LIMIT $` + itoa(argIdx)
		args = append(args, params.Limit)
		argIdx++
	}
	if params.Offset > 0 {
		query += ` OFFSET $` + itoa(argIdx)
		args = append(args, params.Offset)
	}

	rows, err := s.pg.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var durationNs int64
		var errorCode sql.NullString
		var metadataJSON []byte

		if err := rows.Scan(
			&e.ID,
			&e.TenantID,
			&e.UserID,
			&e.ToolName,
			&e.ServerID,
			&e.Timestamp,
			&durationNs,
			&e.TokensIn,
			&e.TokensOut,
			&e.Success,
			&errorCode,
			&metadataJSON,
		); err != nil {
			return nil, err
		}

		e.Duration = time.Duration(durationNs)
		if errorCode.Valid {
			e.ErrorCode = errorCode.String
		}
		if len(metadataJSON) > 0 {
			_ = json.Unmarshal(metadataJSON, &e.Metadata)
		}

		events = append(events, e)
	}

	return events, rows.Err()
}

// GetSummary returns aggregated statistics.
func (s *PostgresStore) GetSummary(ctx context.Context, tenantID, userID string, start, end time.Time) (Summary, error) {
	summary := Summary{
		TenantID:      tenantID,
		UserID:        userID,
		PeriodStart:   start,
		PeriodEnd:     end,
		ToolBreakdown: make(map[string]int64),
	}

	// Get aggregate stats
	query := `
		SELECT
			COUNT(*) as total_events,
			COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END), 0) as success_count,
			COALESCE(SUM(CASE WHEN NOT success THEN 1 ELSE 0 END), 0) as error_count,
			COALESCE(SUM(tokens_in), 0) as total_tokens_in,
			COALESCE(SUM(tokens_out), 0) as total_tokens_out,
			COALESCE(SUM(duration_ns), 0) as total_duration_ns,
			COALESCE(AVG(duration_ns), 0) as avg_duration_ns
		FROM usage_events
		WHERE 1=1
	`
	args := []any{}
	argIdx := 1

	if tenantID != "" {
		query += ` AND tenant_id = $` + itoa(argIdx)
		args = append(args, tenantID)
		argIdx++
	}
	if userID != "" {
		query += ` AND user_id = $` + itoa(argIdx)
		args = append(args, userID)
		argIdx++
	}
	if !start.IsZero() {
		query += ` AND timestamp >= $` + itoa(argIdx)
		args = append(args, start)
		argIdx++
	}
	if !end.IsZero() {
		query += ` AND timestamp <= $` + itoa(argIdx)
		args = append(args, end)
		argIdx++
	}

	var totalDurationNs, avgDurationNs int64
	err := s.pg.QueryRow(ctx, query, args...).Scan(
		&summary.TotalEvents,
		&summary.SuccessCount,
		&summary.ErrorCount,
		&summary.TotalTokensIn,
		&summary.TotalTokensOut,
		&totalDurationNs,
		&avgDurationNs,
	)
	if err != nil {
		return Summary{}, err
	}

	summary.TotalDuration = time.Duration(totalDurationNs)
	summary.AvgDuration = time.Duration(avgDurationNs)

	// Get tool breakdown
	breakdownQuery := `
		SELECT tool_name, COUNT(*) as count
		FROM usage_events
		WHERE tool_name IS NOT NULL AND tool_name != ''
	`
	breakdownArgs := []any{}
	breakdownArgIdx := 1

	if tenantID != "" {
		breakdownQuery += ` AND tenant_id = $` + itoa(breakdownArgIdx)
		breakdownArgs = append(breakdownArgs, tenantID)
		breakdownArgIdx++
	}
	if userID != "" {
		breakdownQuery += ` AND user_id = $` + itoa(breakdownArgIdx)
		breakdownArgs = append(breakdownArgs, userID)
		breakdownArgIdx++
	}
	if !start.IsZero() {
		breakdownQuery += ` AND timestamp >= $` + itoa(breakdownArgIdx)
		breakdownArgs = append(breakdownArgs, start)
		breakdownArgIdx++
	}
	if !end.IsZero() {
		breakdownQuery += ` AND timestamp <= $` + itoa(breakdownArgIdx)
		breakdownArgs = append(breakdownArgs, end)
		breakdownArgIdx++
	}

	breakdownQuery += ` GROUP BY tool_name ORDER BY count DESC LIMIT 100`

	rows, err := s.pg.Query(ctx, breakdownQuery, breakdownArgs...)
	if err != nil {
		return summary, nil // Return partial result on breakdown error
	}
	defer rows.Close()

	for rows.Next() {
		var toolName string
		var count int64
		if err := rows.Scan(&toolName, &count); err != nil {
			continue
		}
		summary.ToolBreakdown[toolName] = count
	}

	return summary, nil
}

// DeleteOlderThan removes events older than the given time.
func (s *PostgresStore) DeleteOlderThan(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.pg.Exec(ctx, `
		DELETE FROM usage_events
		WHERE timestamp < $1
	`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Close releases resources.
func (s *PostgresStore) Close() error {
	return s.pg.Close()
}

// itoa converts an int to a string (simple implementation to avoid strconv import).
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
