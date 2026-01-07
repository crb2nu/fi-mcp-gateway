package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver
)

// PostgresConfig holds Postgres connection configuration.
type PostgresConfig struct {
	// URL is the connection string (postgres://user:pass@host:port/db?sslmode=disable)
	URL string

	// Individual connection params (used if URL is empty)
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string

	// Pool configuration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// LoadPostgresConfigFromEnv loads Postgres configuration from environment variables.
func LoadPostgresConfigFromEnv(prefix string) PostgresConfig {
	if prefix != "" && !strings.HasSuffix(prefix, "_") {
		prefix += "_"
	}

	return PostgresConfig{
		URL:             os.Getenv(prefix + "POSTGRES_URL"),
		Host:            envDefault(prefix+"POSTGRES_HOST", "localhost"),
		Port:            envIntDefault(prefix+"POSTGRES_PORT", 5432),
		User:            envDefault(prefix+"POSTGRES_USER", "postgres"),
		Password:        os.Getenv(prefix + "POSTGRES_PASSWORD"),
		Database:        envDefault(prefix+"POSTGRES_DB", "fi_mcp_gateway"),
		SSLMode:         envDefault(prefix+"POSTGRES_SSLMODE", "disable"),
		MaxOpenConns:    envIntDefault(prefix+"POSTGRES_MAX_OPEN", 25),
		MaxIdleConns:    envIntDefault(prefix+"POSTGRES_MAX_IDLE", 5),
		ConnMaxLifetime: envDurationDefault(prefix+"POSTGRES_CONN_MAX_LIFE", 5*time.Minute),
		ConnMaxIdleTime: envDurationDefault(prefix+"POSTGRES_CONN_MAX_IDLE", 1*time.Minute),
	}
}

// ConnectionString returns the connection string for the config.
func (c PostgresConfig) ConnectionString() string {
	if c.URL != "" {
		return c.URL
	}
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Database, c.SSLMode,
	)
}

// Postgres wraps a PostgreSQL connection pool.
type Postgres struct {
	db  *sql.DB
	cfg PostgresConfig
}

// NewPostgres creates a new Postgres client from configuration.
func NewPostgres(cfg PostgresConfig) (*Postgres, error) {
	db, err := sql.Open("postgres", cfg.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	return &Postgres{
		db:  db,
		cfg: cfg,
	}, nil
}

// Ping tests the database connection.
func (p *Postgres) Ping(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

// Close closes the database connection.
func (p *Postgres) Close() error {
	return p.db.Close()
}

// DB returns the underlying database connection for direct operations.
func (p *Postgres) DB() *sql.DB {
	return p.db
}

// Exec executes a query without returning rows.
func (p *Postgres) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

// Query executes a query that returns rows.
func (p *Postgres) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

// QueryRow executes a query that returns a single row.
func (p *Postgres) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}

// Begin starts a transaction.
func (p *Postgres) Begin(ctx context.Context) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, nil)
}

// BeginTx starts a transaction with options.
func (p *Postgres) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, opts)
}

// InTx executes a function within a transaction.
func (p *Postgres) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

// MigrateSchema runs database migrations for quota tables.
func (p *Postgres) MigrateQuotaSchema(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS quotas (
		tenant_id VARCHAR(255) NOT NULL,
		user_id VARCHAR(255) NOT NULL DEFAULT '',
		quota_type VARCHAR(50) NOT NULL,
		limit_value BIGINT NOT NULL,
		soft_limit BIGINT,
		period VARCHAR(20) NOT NULL DEFAULT 'daily',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		PRIMARY KEY (tenant_id, user_id, quota_type)
	);

	CREATE TABLE IF NOT EXISTS quota_usage (
		tenant_id VARCHAR(255) NOT NULL,
		user_id VARCHAR(255) NOT NULL DEFAULT '',
		quota_type VARCHAR(50) NOT NULL,
		period_start TIMESTAMPTZ NOT NULL,
		period_end TIMESTAMPTZ NOT NULL,
		current_usage BIGINT NOT NULL DEFAULT 0,
		last_updated TIMESTAMPTZ DEFAULT NOW(),
		PRIMARY KEY (tenant_id, user_id, quota_type, period_start)
	);

	CREATE INDEX IF NOT EXISTS idx_quota_usage_lookup
	ON quota_usage (tenant_id, user_id, quota_type, period_end);
	`

	_, err := p.db.ExecContext(ctx, schema)
	return err
}

// MigrateUsageSchema runs database migrations for usage event tables.
func (p *Postgres) MigrateUsageSchema(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS usage_events (
		id VARCHAR(255) PRIMARY KEY,
		tenant_id VARCHAR(255) NOT NULL,
		user_id VARCHAR(255) NOT NULL DEFAULT '',
		tool_name VARCHAR(255),
		server_id VARCHAR(255),
		timestamp TIMESTAMPTZ NOT NULL,
		duration_ns BIGINT NOT NULL DEFAULT 0,
		tokens_in BIGINT NOT NULL DEFAULT 0,
		tokens_out BIGINT NOT NULL DEFAULT 0,
		success BOOLEAN NOT NULL DEFAULT true,
		error_code VARCHAR(100),
		metadata JSONB
	);

	CREATE INDEX IF NOT EXISTS idx_usage_events_tenant_time
	ON usage_events (tenant_id, timestamp DESC);

	CREATE INDEX IF NOT EXISTS idx_usage_events_user_time
	ON usage_events (tenant_id, user_id, timestamp DESC);

	CREATE INDEX IF NOT EXISTS idx_usage_events_tool
	ON usage_events (tenant_id, tool_name, timestamp DESC);
	`

	_, err := p.db.ExecContext(ctx, schema)
	return err
}

// MigrateAPIKeysSchema runs database migrations for API key tables.
func (p *Postgres) MigrateAPIKeysSchema(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS api_keys (
		id VARCHAR(255) PRIMARY KEY,
		tenant_id VARCHAR(255) NOT NULL,
		user_id VARCHAR(255) NOT NULL,
		name VARCHAR(255) NOT NULL,
		key_hash VARCHAR(255) NOT NULL UNIQUE,
		key_prefix VARCHAR(20) NOT NULL,
		scopes TEXT[],
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at TIMESTAMPTZ,
		last_used_at TIMESTAMPTZ,
		revoked_at TIMESTAMPTZ
	);

	CREATE INDEX IF NOT EXISTS idx_api_keys_tenant_user
	ON api_keys (tenant_id, user_id);

	CREATE INDEX IF NOT EXISTS idx_api_keys_hash
	ON api_keys (key_hash);
	`

	_, err := p.db.ExecContext(ctx, schema)
	return err
}

func envIntDefault2(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
