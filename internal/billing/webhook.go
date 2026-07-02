// Package billing provides webhook integration for billing events.
//
// Webhooks are sent for events like quota warnings, quota exceeded,
// and periodic usage reports.
package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/logger"
)

// EventType defines the type of billing event.
type EventType string

const (
	EventQuotaWarning  EventType = "quota.warning"
	EventQuotaExceeded EventType = "quota.exceeded"
	EventUsageReport   EventType = "usage.report"
	EventKeyCreated    EventType = "apikey.created"
	EventKeyRevoked    EventType = "apikey.revoked"
)

// Event represents a billing webhook event.
type Event struct {
	ID        string         `json:"id"`
	Type      EventType      `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	TenantID  string         `json:"tenant_id"`
	UserID    string         `json:"user_id,omitempty"`
	Data      map[string]any `json:"data"`
}

// Config holds webhook configuration.
type Config struct {
	// Enabled controls whether webhooks are active
	Enabled bool
	// URL is the webhook endpoint
	URL string
	// Secret is used for signing payloads
	Secret string
	// Timeout is the HTTP request timeout
	Timeout time.Duration
	// MaxRetries is the maximum number of retry attempts
	MaxRetries int
	// RetryDelay is the initial delay between retries
	RetryDelay time.Duration
}

// LoadConfigFromEnv loads webhook configuration from environment variables.
func LoadConfigFromEnv() Config {
	return Config{
		Enabled:    envBoolDefault("FI_MCP_BILLING_ENABLED", false),
		URL:        os.Getenv("FI_MCP_BILLING_WEBHOOK_URL"),
		Secret:     os.Getenv("FI_MCP_BILLING_WEBHOOK_SECRET"),
		Timeout:    envDurationDefault("FI_MCP_BILLING_WEBHOOK_TIMEOUT", 10*time.Second),
		MaxRetries: envIntDefault("FI_MCP_BILLING_WEBHOOK_MAX_RETRIES", 3),
		RetryDelay: envDurationDefault("FI_MCP_BILLING_WEBHOOK_RETRY_DELAY", 1*time.Second),
	}
}

// WebhookSender sends billing webhook events.
type WebhookSender interface {
	// Send sends an event to the webhook endpoint.
	Send(ctx context.Context, event Event) error
	// SendAsync sends an event asynchronously.
	SendAsync(event Event)
	// Close shuts down the sender.
	Close() error
}

// HTTPWebhookSender implements WebhookSender using HTTP.
type HTTPWebhookSender struct {
	cfg      Config
	client   *http.Client
	eventsCh chan Event
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewWebhookSender creates a new webhook sender.
func NewWebhookSender(cfg Config) *HTTPWebhookSender {
	if !cfg.Enabled || cfg.URL == "" {
		return &HTTPWebhookSender{cfg: cfg}
	}

	s := &HTTPWebhookSender{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
		eventsCh: make(chan Event, 100),
		done:     make(chan struct{}),
	}

	// Start background worker for async sends
	s.wg.Add(1)
	go s.worker()

	return s
}

// Send sends an event synchronously with retries.
func (s *HTTPWebhookSender) Send(ctx context.Context, event Event) error {
	if !s.cfg.Enabled || s.cfg.URL == "" {
		return nil
	}

	// Set event ID and timestamp if not set
	if event.ID == "" {
		event.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	var lastErr error
	delay := s.cfg.RetryDelay

	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				delay *= 2 // Exponential backoff
			}
		}

		err := s.sendOnce(ctx, event)
		if err == nil {
			return nil
		}
		lastErr = err
		logger.Warn("billing webhook attempt failed",
			"attempt", attempt+1,
			"error", err,
			"event_type", string(event.Type))
	}

	return fmt.Errorf("webhook failed after %d attempts: %w", s.cfg.MaxRetries+1, lastErr)
}

// SendAsync queues an event for async delivery.
func (s *HTTPWebhookSender) SendAsync(event Event) {
	if !s.cfg.Enabled || s.cfg.URL == "" {
		return
	}

	select {
	case s.eventsCh <- event:
	default:
		logger.Warn("billing webhook queue full, dropping event",
			"event_type", string(event.Type),
			"event_id", event.ID)
	}
}

// Close shuts down the sender.
func (s *HTTPWebhookSender) Close() error {
	if s.done != nil {
		close(s.done)
		s.wg.Wait()
	}
	return nil
}

func (s *HTTPWebhookSender) sendOnce(ctx context.Context, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Event", string(event.Type))
	req.Header.Set("X-Webhook-ID", event.ID)
	req.Header.Set("X-Webhook-Timestamp", event.Timestamp.Format(time.RFC3339))

	// Sign the payload if secret is configured
	if s.cfg.Secret != "" {
		signature := s.sign(payload)
		req.Header.Set("X-Webhook-Signature", signature)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
}

func (s *HTTPWebhookSender) sign(payload []byte) string {
	h := hmac.New(sha256.New, []byte(s.cfg.Secret))
	h.Write(payload)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}

func (s *HTTPWebhookSender) worker() {
	defer s.wg.Done()

	for {
		select {
		case <-s.done:
			// Drain remaining events
			for {
				select {
				case event := <-s.eventsCh:
					ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
					if err := s.Send(ctx, event); err != nil {
						logger.Error("failed to send queued billing event",
							"error", err,
							"event_type", string(event.Type))
					}
					cancel()
				default:
					return
				}
			}
		case event := <-s.eventsCh:
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout*time.Duration(s.cfg.MaxRetries+1))
			if err := s.Send(ctx, event); err != nil {
				logger.Error("async billing webhook failed",
					"error", err,
					"event_type", string(event.Type),
					"tenant", event.TenantID)
			}
			cancel()
		}
	}
}

// NoopSender is a sender that does nothing.
type NoopSender struct{}

func (NoopSender) Send(ctx context.Context, event Event) error { return nil }
func (NoopSender) SendAsync(event Event)                       {}
func (NoopSender) Close() error                                { return nil }

// Helper functions

func envBoolDefault(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "true" || v == "1" || v == "yes"
}

func envIntDefault(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	var n int
	for _, c := range v {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			return fallback
		}
	}
	return n
}

func envDurationDefault(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}
