package storage

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig holds Redis connection configuration.
type RedisConfig struct {
	// URL is the Redis connection string (redis://host:port or redis://user:pass@host:port/db)
	URL string

	// Individual connection params (used if URL is empty)
	Host     string
	Port     int
	Password string
	DB       int

	// Pool configuration
	PoolSize     int
	MinIdleConns int
	MaxRetries   int

	// Timeouts
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// LoadRedisConfigFromEnv loads Redis configuration from environment variables.
func LoadRedisConfigFromEnv(prefix string) RedisConfig {
	if prefix != "" && !strings.HasSuffix(prefix, "_") {
		prefix += "_"
	}

	cfg := RedisConfig{
		URL:          os.Getenv(prefix + "REDIS_URL"),
		Host:         envDefault(prefix+"REDIS_HOST", "localhost"),
		Port:         envIntDefault(prefix+"REDIS_PORT", 6379),
		Password:     os.Getenv(prefix + "REDIS_PASSWORD"),
		DB:           envIntDefault(prefix+"REDIS_DB", 0),
		PoolSize:     envIntDefault(prefix+"REDIS_POOL_SIZE", 10),
		MinIdleConns: envIntDefault(prefix+"REDIS_MIN_IDLE", 2),
		MaxRetries:   envIntDefault(prefix+"REDIS_MAX_RETRIES", 3),
		DialTimeout:  envDurationDefault(prefix+"REDIS_DIAL_TIMEOUT", 5*time.Second),
		ReadTimeout:  envDurationDefault(prefix+"REDIS_READ_TIMEOUT", 3*time.Second),
		WriteTimeout: envDurationDefault(prefix+"REDIS_WRITE_TIMEOUT", 3*time.Second),
	}

	return cfg
}

// Redis wraps a Redis client with connection management.
type Redis struct {
	client *redis.Client
	cfg    RedisConfig
}

// NewRedis creates a new Redis client from configuration.
func NewRedis(cfg RedisConfig) (*Redis, error) {
	var opts *redis.Options
	var err error

	if cfg.URL != "" {
		opts, err = redis.ParseURL(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("parse redis URL: %w", err)
		}
	} else {
		opts = &redis.Options{
			Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
			Password: cfg.Password,
			DB:       cfg.DB,
		}
	}

	// Apply pool settings
	if cfg.PoolSize > 0 {
		opts.PoolSize = cfg.PoolSize
	}
	if cfg.MinIdleConns > 0 {
		opts.MinIdleConns = cfg.MinIdleConns
	}
	if cfg.MaxRetries > 0 {
		opts.MaxRetries = cfg.MaxRetries
	}
	if cfg.DialTimeout > 0 {
		opts.DialTimeout = cfg.DialTimeout
	}
	if cfg.ReadTimeout > 0 {
		opts.ReadTimeout = cfg.ReadTimeout
	}
	if cfg.WriteTimeout > 0 {
		opts.WriteTimeout = cfg.WriteTimeout
	}

	client := redis.NewClient(opts)

	return &Redis{
		client: client,
		cfg:    cfg,
	}, nil
}

// Ping tests the Redis connection.
func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Close closes the Redis connection.
func (r *Redis) Close() error {
	return r.client.Close()
}

// Client returns the underlying Redis client for direct operations.
func (r *Redis) Client() *redis.Client {
	return r.client
}

// Get retrieves a string value.
func (r *Redis) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

// Set stores a string value with optional expiration.
func (r *Redis) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	return r.client.Set(ctx, key, value, expiration).Err()
}

// Incr atomically increments a key.
func (r *Redis) Incr(ctx context.Context, key string) (int64, error) {
	return r.client.Incr(ctx, key).Result()
}

// IncrBy atomically increments a key by a value.
func (r *Redis) IncrBy(ctx context.Context, key string, value int64) (int64, error) {
	return r.client.IncrBy(ctx, key, value).Result()
}

// Expire sets expiration on a key.
func (r *Redis) Expire(ctx context.Context, key string, expiration time.Duration) (bool, error) {
	return r.client.Expire(ctx, key, expiration).Result()
}

// TTL returns the remaining time-to-live of a key.
func (r *Redis) TTL(ctx context.Context, key string) (time.Duration, error) {
	return r.client.TTL(ctx, key).Result()
}

// Del deletes keys.
func (r *Redis) Del(ctx context.Context, keys ...string) (int64, error) {
	return r.client.Del(ctx, keys...).Result()
}

// Eval runs a Lua script.
func (r *Redis) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	return r.client.Eval(ctx, script, keys, args...)
}

// EvalSha runs a cached Lua script by SHA.
func (r *Redis) EvalSha(ctx context.Context, sha string, keys []string, args ...any) *redis.Cmd {
	return r.client.EvalSha(ctx, sha, keys, args...)
}

// ScriptLoad loads a Lua script and returns its SHA.
func (r *Redis) ScriptLoad(ctx context.Context, script string) (string, error) {
	return r.client.ScriptLoad(ctx, script).Result()
}

// Pipeline returns a Redis pipeline for batched operations.
func (r *Redis) Pipeline() redis.Pipeliner {
	return r.client.Pipeline()
}

// TxPipeline returns a transactional pipeline.
func (r *Redis) TxPipeline() redis.Pipeliner {
	return r.client.TxPipeline()
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntDefault(key string, fallback int) int {
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

func envDurationDefault(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
