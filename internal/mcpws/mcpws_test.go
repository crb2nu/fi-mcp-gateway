package mcpws

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/policy"
)

func TestGateway_ForwardsJSONRPCOverWebSocket(t *testing.T) {
	t.Parallel()

	backendPort, backendClose, _ := startFakeMCPBackend(t)
	t.Cleanup(backendClose)

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "test", Categories: []string{"hub"}},
		},
	}

	gw := New(Config{
		Registry:           reg,
		HubNamespace:       "ignored",
		ServerScheme:       "ws",
		ServerHostTemplate: "127.0.0.1",
		ServerPort:         strconv.Itoa(backendPort),
		ServerWSPath:       "/ws",
		HandshakeTimeout:   2 * time.Second,
		DialTimeout:        2 * time.Second,
		BackendMaxIdle:     1,
		BackendMaxOpen:     2,
		BackendIdleTimeout: 5 * time.Second,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", gw.HandleWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws?server=test"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// initialize
	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "0.0.0",
			},
		},
	})
	initResp := mustReadJSON(t, client, 2*time.Second)
	if initResp["id"] != float64(1) {
		t.Fatalf("initialize id: got %v", initResp["id"])
	}
	if initResp["result"] == nil {
		t.Fatalf("initialize missing result: %v", initResp)
	}

	// tools/list
	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	listResp := mustReadJSON(t, client, 2*time.Second)
	if listResp["id"] != float64(2) {
		t.Fatalf("tools/list id: got %v", listResp["id"])
	}
	result, ok := listResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list result type: %T", listResp["result"])
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools/list tools: %#v", listResp["result"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "ping" {
		t.Fatalf("tools/list tool: %#v", tools[0])
	}

	// tools/call
	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ping",
			"arguments": map[string]any{},
		},
	})
	callResp := mustReadJSON(t, client, 2*time.Second)
	if callResp["id"] != float64(3) {
		t.Fatalf("tools/call id: got %v", callResp["id"])
	}
	callResult, ok := callResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call result type: %T", callResp["result"])
	}
	content, ok := callResult["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("tools/call content: %#v", callResp["result"])
	}
	item, ok := content[0].(map[string]any)
	if !ok || item["type"] != "text" || item["text"] != "pong" {
		t.Fatalf("tools/call content item: %#v", content[0])
	}
}

func TestGateway_WS_AuthRequired(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "test", Categories: []string{"hub"}},
		},
	}

	gw := New(Config{
		Registry:           reg,
		Authenticator:      headerTokenAuth{},
		Policy:             policy.AllowAll{},
		ServerHostTemplate: "127.0.0.1",
		ServerPort:         "1",
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", gw.HandleWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws?server=test"
	_, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatalf("expected auth failure")
	}
}

type headerTokenAuth struct{}

func (headerTokenAuth) Authenticate(r *http.Request) (*auth.Principal, error) {
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		return nil, auth.ErrUnauthorized
	}
	return &auth.Principal{Subject: "test"}, nil
}

type denyToolsCallPolicy struct{}

func (denyToolsCallPolicy) Authorize(ctx context.Context, p *auth.Principal, req policy.Request) policy.Decision {
	if req.Method == "tools/call" {
		return policy.Decision{Allow: false, Reason: "denied"}
	}
	return policy.Decision{Allow: true}
}

func TestGateway_PolicyDeniesToolsCallWithoutDial(t *testing.T) {
	t.Parallel()

	backendPort, backendClose, connCount := startFakeMCPBackend(t)
	t.Cleanup(backendClose)

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "k8s", Categories: []string{"hub"}},
		},
	}

	gw := New(Config{
		Registry:           reg,
		Policy:             denyToolsCallPolicy{},
		ServerScheme:       "ws",
		ServerHostTemplate: "127.0.0.1",
		ServerPort:         strconv.Itoa(backendPort),
		ServerWSPath:       "/ws",
		HandshakeTimeout:   2 * time.Second,
		DialTimeout:        2 * time.Second,
		BackendIdleTimeout: 5 * time.Second,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", gw.HandleWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws?server=k8s"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "k8s__ping",
			"arguments": map[string]any{},
		},
	})
	resp := mustReadJSON(t, client, 2*time.Second)
	if resp["error"] == nil {
		t.Fatalf("expected error response: %#v", resp)
	}

	if got := atomic.LoadInt32(connCount); got != 0 {
		t.Fatalf("backend connection count: got %d, want 0", got)
	}
}

func TestGateway_RoutesByToolPrefixAndReusesConnection(t *testing.T) {
	t.Parallel()

	backendPort, backendClose, connCount := startFakeMCPBackend(t)
	t.Cleanup(backendClose)

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "k8s", Categories: []string{"hub"}},
		},
	}

	gw := New(Config{
		Registry:           reg,
		HubNamespace:       "ignored",
		ServerScheme:       "ws",
		ServerHostTemplate: "127.0.0.1",
		ServerPort:         strconv.Itoa(backendPort),
		ServerWSPath:       "/ws",
		HandshakeTimeout:   2 * time.Second,
		DialTimeout:        2 * time.Second,
		BackendMaxIdle:     1,
		BackendMaxOpen:     2,
		BackendIdleTimeout: 5 * time.Second,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", gw.HandleWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws?server=k8s"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "0.0.0",
			},
		},
	})
	_ = mustReadJSON(t, client, 2*time.Second)

	// First call should dial once.
	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "k8s__ping",
			"arguments": map[string]any{},
		},
	})
	_ = mustReadJSON(t, client, 2*time.Second)

	// Second call should reuse the same backend WS connection.
	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "k8s__ping",
			"arguments": map[string]any{},
		},
	})
	_ = mustReadJSON(t, client, 2*time.Second)

	if got := atomic.LoadInt32(connCount); got != 1 {
		t.Fatalf("backend connection count: got %d, want 1", got)
	}
}

func TestGateway_BackendIdleTimeoutClosesAndRedials(t *testing.T) {
	t.Parallel()

	backendPort, backendClose, connCount := startFakeMCPBackend(t)
	t.Cleanup(backendClose)

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "test", Categories: []string{"hub"}},
		},
	}

	gw := New(Config{
		Registry:           reg,
		HubNamespace:       "ignored",
		ServerScheme:       "ws",
		ServerHostTemplate: "127.0.0.1",
		ServerPort:         strconv.Itoa(backendPort),
		ServerWSPath:       "/ws",
		HandshakeTimeout:   2 * time.Second,
		DialTimeout:        2 * time.Second,
		BackendMaxIdle:     1,
		BackendMaxOpen:     2,
		BackendIdleTimeout: 80 * time.Millisecond,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", gw.HandleWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws?server=test"
	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{},
	})
	_ = mustReadJSON(t, client, 2*time.Second)

	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ping",
			"arguments": map[string]any{},
		},
	})
	_ = mustReadJSON(t, client, 2*time.Second)

	time.Sleep(250 * time.Millisecond)

	mustWriteJSON(t, client, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ping",
			"arguments": map[string]any{},
		},
	})
	_ = mustReadJSON(t, client, 2*time.Second)

	if got := atomic.LoadInt32(connCount); got < 2 {
		t.Fatalf("backend connection count: got %d, want >= 2", got)
	}
}

func TestRouteByToolName_Prefix(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "k8s", Categories: []string{"hub"}},
			{Name: "git", Categories: []string{"hub"}},
		},
	}
	gw := New(Config{Registry: reg})

	if got := gw.routeByToolName("common", "k8s__getPods"); got != "k8s" {
		t.Fatalf("routeByToolName: got %q, want %q", got, "k8s")
	}
}

func TestRouteByToolName_AlwaysAllow(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{
				Name:       "test",
				Categories: []string{"hub"},
				Targets: map[string]*registry.TargetSpec{
					"common": {AlwaysAllow: []string{"ping"}},
				},
			},
		},
	}
	gw := New(Config{Registry: reg})

	if got := gw.routeByToolName("common", "ping"); got != "test" {
		t.Fatalf("routeByToolName: got %q, want %q", got, "test")
	}
}

func startFakeMCPBackend(t *testing.T) (port int, closeFn func(), connCount *int32) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var count int32
	connCount = &count

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(connCount, 1)
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
				continue
			}

			var resp map[string]any
			switch method {
			case "initialize":
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"protocolVersion": "2024-11-05",
						"serverInfo": map[string]any{
							"name":    "fake-backend",
							"version": "0.0.0",
						},
						"capabilities": map[string]any{
							"tools": map[string]any{},
						},
					},
				}
			case "tools/list":
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"tools": []any{
							map[string]any{
								"name":        "ping",
								"description": "returns pong",
								"inputSchema": map[string]any{
									"type":       "object",
									"properties": map[string]any{},
								},
							},
						},
					},
				}
			case "tools/call":
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"content": []any{
							map[string]any{
								"type": "text",
								"text": "pong",
							},
						},
						"isError": false,
					},
				}
			default:
				resp = map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]any{
						"code":    -32601,
						"message": "method not found",
					},
				}
			}

			out, err := json.Marshal(resp)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, out)
		}
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}

	go func() {
		_ = srv.Serve(ln)
	}()

	closeFn = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}

	return ln.Addr().(*net.TCPAddr).Port, closeFn, connCount
}

func mustWriteJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func mustReadJSON(t *testing.T, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(msg, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
