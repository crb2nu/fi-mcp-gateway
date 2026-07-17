package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
)

// fakeToolBackend is an in-process WebSocket MCP backend speaking just
// enough MCP for one-shot calls: initialize + tools/call.
type fakeToolBackend struct {
	srv *httptest.Server

	// callResult is returned as the JSON-RPC result of tools/call.
	callResult map[string]any

	mu       sync.Mutex
	lastTool string
	lastArgs map[string]any
}

func startFakeToolBackend(t *testing.T, callResult map[string]any) *fakeToolBackend {
	t.Helper()

	fb := &fakeToolBackend{callResult: callResult}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
				continue
			}

			var req map[string]any
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}

			method, _ := req["method"].(string)
			id, hasID := req["id"]
			if !hasID {
				continue // notifications (e.g. notifications/initialized)
			}

			var resp map[string]any
			switch method {
			case "initialize":
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"protocolVersion": "2024-11-05",
						"serverInfo":      map[string]any{"name": "fake-backend", "version": "0.0.0"},
						"capabilities":    map[string]any{"tools": map[string]any{}},
					},
				}
			case "tools/call":
				params, _ := req["params"].(map[string]any)
				fb.mu.Lock()
				fb.lastTool, _ = params["name"].(string)
				fb.lastArgs, _ = params["arguments"].(map[string]any)
				fb.mu.Unlock()
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  fb.callResult,
				}
			default:
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"error":   map[string]any{"code": -32601, "message": "method not found"},
				}
			}

			out, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, out)
		}
	})

	fb.srv = httptest.NewServer(mux)
	t.Cleanup(fb.srv.Close)
	return fb
}

// wsURL returns the backend base URL in ws:// form as stored in the registry.
func (fb *fakeToolBackend) wsURL() string {
	return "ws" + strings.TrimPrefix(fb.srv.URL, "http")
}

// deadBackendURL returns a ws:// URL that refuses connections.
func deadBackendURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return "ws://127.0.0.1:" + strconv.Itoa(port)
}

func toolTestRegistry(backendURL string) *registry.Registry {
	return &registry.Registry{
		Servers: []*registry.Server{
			{
				Name: "atlassian",
				URL:  backendURL,
				Common: &registry.TargetSpec{
					AlwaysAllow: []string{"jira_search", "confluence_search"},
				},
			},
			{
				Name:       "local-fs",
				Categories: []string{"local-only"},
				Common: &registry.TargetSpec{
					AlwaysAllow: []string{"read_file"},
				},
			},
		},
	}
}

func TestInvokeToolEndpoint(t *testing.T) {
	successResult := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": `{"hits":[{"key":"ICC-1"}]}`},
		},
		"isError": false,
	}
	errorResult := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "jql parse failure"},
		},
		"isError": true,
	}

	tests := []struct {
		name         string
		path         string
		body         string
		callResult   map[string]any // nil = no backend (dead URL)
		wantStatus   int
		wantContains string
	}{
		{
			name:         "happy path returns raw CallToolResult",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `{"jql":"project = ICC","limit":5}`,
			callResult:   successResult,
			wantStatus:   http.StatusOK,
			wantContains: "ICC-1",
		},
		{
			name:         "non-allowlisted tool is forbidden",
			path:         "/api/v1/tools/atlassian/jira_create_issue",
			body:         `{"summary":"nope"}`,
			callResult:   successResult,
			wantStatus:   http.StatusForbidden,
			wantContains: "not allowlisted",
		},
		{
			name:         "unknown server is not found",
			path:         "/api/v1/tools/nonexistent/jira_search",
			body:         `{}`,
			callResult:   successResult,
			wantStatus:   http.StatusNotFound,
			wantContains: "unknown server",
		},
		{
			name:         "local-only server is not found",
			path:         "/api/v1/tools/local-fs/read_file",
			body:         `{"path":"/etc/hosts"}`,
			callResult:   successResult,
			wantStatus:   http.StatusNotFound,
			wantContains: "unknown server",
		},
		{
			name:         "isError result maps to bad gateway",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `{"jql":"broken (("}`,
			callResult:   errorResult,
			wantStatus:   http.StatusBadGateway,
			wantContains: "jql parse failure",
		},
		{
			name:         "unreachable backend maps to bad gateway",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `{"jql":"project = ICC"}`,
			callResult:   nil,
			wantStatus:   http.StatusBadGateway,
			wantContains: "tool call failed",
		},
		{
			name:         "invalid body is bad request",
			path:         "/api/v1/tools/atlassian/jira_search",
			body:         `["not","an","object"]`,
			callResult:   successResult,
			wantStatus:   http.StatusBadRequest,
			wantContains: "JSON object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var backendURL string
			if tt.callResult != nil {
				backendURL = startFakeToolBackend(t, tt.callResult).wsURL()
			} else {
				backendURL = deadBackendURL(t)
			}

			srv := New(Config{
				Registry:        toolTestRegistry(backendURL),
				ToolCallTimeout: 5 * time.Second,
			})
			handler := srv.Handler()

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
			if tt.wantContains != "" && !strings.Contains(rec.Body.String(), tt.wantContains) {
				t.Errorf("expected body to contain %q, got %s", tt.wantContains, rec.Body.String())
			}

			if tt.wantStatus == http.StatusOK {
				var result struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatalf("failed to parse CallToolResult: %v", err)
				}
				if result.IsError {
					t.Error("expected isError=false on 200 response")
				}
				if len(result.Content) == 0 || result.Content[0].Type != "text" {
					t.Fatalf("expected text content, got %+v", result.Content)
				}
			}
		})
	}
}

func TestInvokeToolEndpoint_ForwardsArguments(t *testing.T) {
	fb := startFakeToolBackend(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"isError": false,
	})

	srv := New(Config{
		Registry:        toolTestRegistry(fb.wsURL()),
		ToolCallTimeout: 5 * time.Second,
	})
	handler := srv.Handler()

	body := `{"jql":"project = ICC ORDER BY updated DESC","limit":50}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/atlassian/jira_search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.lastTool != "jira_search" {
		t.Errorf("expected backend to receive tool 'jira_search', got %q", fb.lastTool)
	}
	if fb.lastArgs["jql"] != "project = ICC ORDER BY updated DESC" {
		t.Errorf("expected jql argument forwarded, got %v", fb.lastArgs)
	}
	if fb.lastArgs["limit"] != float64(50) {
		t.Errorf("expected limit argument forwarded, got %v", fb.lastArgs)
	}
}

func TestInvokeToolEndpoint_EmptyBodyMeansNoArguments(t *testing.T) {
	fb := startFakeToolBackend(t, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"isError": false,
	})

	srv := New(Config{
		Registry:        toolTestRegistry(fb.wsURL()),
		ToolCallTimeout: 5 * time.Second,
	})
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/atlassian/confluence_search", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.lastTool != "confluence_search" {
		t.Errorf("expected backend to receive tool 'confluence_search', got %q", fb.lastTool)
	}
	if len(fb.lastArgs) != 0 {
		t.Errorf("expected empty arguments, got %v", fb.lastArgs)
	}
}

func TestInvokeToolEndpoint_UnknownStaticToolIsNotFound(t *testing.T) {
	reg := &registry.Registry{
		Servers: []*registry.Server{
			{
				Name: "atlassian",
				URL:  "ws://127.0.0.1:1",
				Common: &registry.TargetSpec{
					AlwaysAllow: []string{"jira_search"},
					Tools: []registry.ToolSchema{
						{Name: "jira_search"},
						{Name: "jira_create_issue"},
					},
				},
			},
		},
	}

	srv := New(Config{Registry: reg, ToolCallTimeout: time.Second})
	handler := srv.Handler()

	// Not in static tools and not allowlisted: unknown tool.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/atlassian/no_such_tool", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 for unknown tool, got %d: %s", rec.Code, rec.Body.String())
	}

	// In static tools but not allowlisted: forbidden.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/atlassian/jira_create_issue", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 for non-allowlisted known tool, got %d: %s", rec.Code, rec.Body.String())
	}
}
