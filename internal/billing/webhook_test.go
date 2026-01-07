package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookSender_Send(t *testing.T) {
	var received atomic.Int32
	var lastEvent Event
	var lastSignature string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)

		// Check headers
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if et := r.Header.Get("X-Webhook-Event"); et == "" {
			t.Error("X-Webhook-Event header missing")
		}

		lastSignature = r.Header.Get("X-Webhook-Signature")

		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &lastEvent)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewWebhookSender(Config{
		Enabled:    true,
		URL:        server.URL,
		Secret:     "test-secret",
		Timeout:    5 * time.Second,
		MaxRetries: 2,
		RetryDelay: 10 * time.Millisecond,
	})
	defer sender.Close()

	event := Event{
		ID:        "test-1",
		Type:      EventQuotaWarning,
		TenantID:  "tenant-1",
		UserID:    "user-1",
		Timestamp: time.Now(),
		Data: map[string]any{
			"quota_type": "requests",
			"current":    8000,
			"limit":      10000,
		},
	}

	err := sender.Send(context.Background(), event)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if received.Load() != 1 {
		t.Errorf("Server received %d requests, want 1", received.Load())
	}
	if lastEvent.ID != "test-1" {
		t.Errorf("Event ID = %q, want test-1", lastEvent.ID)
	}
	if lastEvent.Type != EventQuotaWarning {
		t.Errorf("Event Type = %q, want %q", lastEvent.Type, EventQuotaWarning)
	}
	if !strings.HasPrefix(lastSignature, "sha256=") {
		t.Errorf("Signature should start with sha256=, got %q", lastSignature)
	}
}

func TestWebhookSender_Retry(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		if count < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewWebhookSender(Config{
		Enabled:    true,
		URL:        server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 3,
		RetryDelay: 10 * time.Millisecond,
	})
	defer sender.Close()

	err := sender.Send(context.Background(), Event{
		Type:     EventUsageReport,
		TenantID: "t",
	})

	if err != nil {
		t.Fatalf("Send with retry failed: %v", err)
	}
	if attempts.Load() != 3 {
		t.Errorf("Attempts = %d, want 3", attempts.Load())
	}
}

func TestWebhookSender_MaxRetriesExceeded(t *testing.T) {
	var attempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	sender := NewWebhookSender(Config{
		Enabled:    true,
		URL:        server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 2,
		RetryDelay: 10 * time.Millisecond,
	})
	defer sender.Close()

	err := sender.Send(context.Background(), Event{
		Type:     EventQuotaExceeded,
		TenantID: "t",
	})

	if err == nil {
		t.Error("Send should fail after max retries")
	}
	// 1 initial + 2 retries = 3 attempts
	if attempts.Load() != 3 {
		t.Errorf("Attempts = %d, want 3", attempts.Load())
	}
}

func TestWebhookSender_SendAsync(t *testing.T) {
	var received atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewWebhookSender(Config{
		Enabled:    true,
		URL:        server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 0,
		RetryDelay: 10 * time.Millisecond,
	})

	// Send async
	sender.SendAsync(Event{Type: EventKeyCreated, TenantID: "t"})
	sender.SendAsync(Event{Type: EventKeyRevoked, TenantID: "t"})

	// Close to ensure all events are processed
	sender.Close()

	if received.Load() != 2 {
		t.Errorf("Async received = %d, want 2", received.Load())
	}
}

func TestWebhookSender_Disabled(t *testing.T) {
	sender := NewWebhookSender(Config{Enabled: false})

	err := sender.Send(context.Background(), Event{Type: EventQuotaWarning})
	if err != nil {
		t.Errorf("Disabled sender should not error: %v", err)
	}

	sender.SendAsync(Event{Type: EventQuotaWarning})
	sender.Close()
}

func TestWebhookSender_NoURL(t *testing.T) {
	sender := NewWebhookSender(Config{Enabled: true, URL: ""})

	err := sender.Send(context.Background(), Event{Type: EventQuotaWarning})
	if err != nil {
		t.Errorf("Sender with no URL should not error: %v", err)
	}
}

func TestWebhookSender_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewWebhookSender(Config{
		Enabled:    true,
		URL:        server.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 5,
		RetryDelay: 100 * time.Millisecond,
	})
	defer sender.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := sender.Send(ctx, Event{Type: EventUsageReport, TenantID: "t"})
	if err == nil {
		t.Error("Send should fail when context is cancelled")
	}
}

func TestNoopSender(t *testing.T) {
	sender := NoopSender{}

	err := sender.Send(context.Background(), Event{})
	if err != nil {
		t.Errorf("NoopSender.Send should not error: %v", err)
	}

	sender.SendAsync(Event{})

	if err := sender.Close(); err != nil {
		t.Errorf("NoopSender.Close should not error: %v", err)
	}
}

func TestEventTypes(t *testing.T) {
	// Verify event type constants
	eventTypes := []EventType{
		EventQuotaWarning,
		EventQuotaExceeded,
		EventUsageReport,
		EventKeyCreated,
		EventKeyRevoked,
	}

	for _, et := range eventTypes {
		if string(et) == "" {
			t.Errorf("EventType %v should not be empty", et)
		}
	}
}
