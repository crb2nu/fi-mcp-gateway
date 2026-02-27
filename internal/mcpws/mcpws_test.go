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

type panicRateLimiter struct{}

func (panicRateLimiter) CheckMessage(tenant, user, tool string) (bool, time.Duration, error) {
	panic("boom")
}

type nilPtrRateLimiter struct{}

func (*nilPtrRateLimiter) CheckMessage(tenant, user, tool string) (bool, time.Duration, error) {
	return true, 0, nil
}

func TestCheckMessageSafe_RecoversFromPanic(t *testing.T) {
	t.Parallel()

	allowed, retryAfter, err := checkMessageSafe(panicRateLimiter{}, "tenant", "user", "tool")
	if err == nil {
		t.Fatalf("expected panic recovery error")
	}
	if !strings.Contains(err.Error(), "panic recovered") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allow-on-panic behavior")
	}
	if retryAfter != 0 {
		t.Fatalf("expected retryAfter=0, got %v", retryAfter)
	}
}

func TestIsNilRateLimiter_DetectsTypedNil(t *testing.T) {
	t.Parallel()

	var limiter RateLimiter = (*nilPtrRateLimiter)(nil)
	if !isNilRateLimiter(limiter) {
		t.Fatalf("expected typed nil interface to be detected as nil")
	}
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

	if got, _ := gw.ResolveServer("common", "k8s__getPods", nil); got != "k8s" {
		t.Fatalf("ResolveServer: got %q, want %q", got, "k8s")
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

	if got, _ := gw.ResolveServer("common", "ping", nil); got != "test" {
		t.Fatalf("ResolveServer: got %q, want %q", got, "test")
	}
}

func TestResolveServer_GlobalIndex(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{
				Name:       "server1",
				Categories: []string{"hub"},
				Common: &registry.TargetSpec{
					Tools: []registry.ToolSchema{
						{Name: "unique_tool"},
					},
				},
			},
			{
				Name:       "server2",
				Categories: []string{"hub"},
				Common: &registry.TargetSpec{
					Tools: []registry.ToolSchema{
						{Name: "shared_tool"},
					},
				},
			},
			{
				Name:       "server3",
				Categories: []string{"hub"},
				Common: &registry.TargetSpec{
					Tools: []registry.ToolSchema{
						{Name: "shared_tool"},
					},
				},
			},
		},
	}

	gw := New(Config{Registry: reg})

	// Test unique tool routing
	srv, err := gw.ResolveServer("common", "unique_tool", nil)
	if err != nil {
		t.Fatalf("ResolveServer unique_tool failed: %v", err)
	}
	if srv != "server1" {
		t.Errorf("expected server1, got %q", srv)
	}

	// Test ambiguous tool routing
	_, err = gw.ResolveServer("common", "shared_tool", nil)
	if err == nil {
		t.Fatal("expected error for ambiguous tool")
	}
	if !strings.Contains(err.Error(), "ambiguous tool") {
		t.Errorf("expected ambiguous tool error, got: %v", err)
	}

	// Test unknown tool
	srv, err = gw.ResolveServer("common", "unknown_tool", nil)
	if err != nil {
		t.Fatalf("unexpected error for unknown tool: %v", err)
	}
	if srv != "" {
		t.Errorf("expected empty string for unknown tool, got %q", srv)
	}
}

func TestResolveServer_ArgumentRouting(t *testing.T) {
	t.Parallel()

	reg := &registry.Registry{
		Servers: []*registry.Server{
			{Name: "fs-ssd", Categories: []string{"hub"}},
			{Name: "fs-hdd", Categories: []string{"hub"}},
		},
		Routing: []*registry.RoutingRule{
			{
				ToolName: "read_file",
				Argument: "path",
				Cases: []registry.RoutingCase{
					{Match: "/ssd/*", Server: "fs-ssd"},
					{Match: "/hdd/*", Server: "fs-hdd"},
				},
				Default: "fs-hdd",
			},
		},
	}

	gw := New(Config{Registry: reg})

	// Test SSD route
	srv, err := gw.ResolveServer("common", "read_file", map[string]any{"path": "/ssd/data.txt"})
	if err != nil {
		t.Fatalf("SSD route failed: %v", err)
	}
	if srv != "fs-ssd" {
		t.Errorf("expected fs-ssd, got %q", srv)
	}

	// Test HDD route
	srv, err = gw.ResolveServer("common", "read_file", map[string]any{"path": "/hdd/logs.txt"})
	if err != nil {
		t.Fatalf("HDD route failed: %v", err)
	}
	if srv != "fs-hdd" {
		t.Errorf("expected fs-hdd, got %q", srv)
	}

	// Test Default route
	srv, err = gw.ResolveServer("common", "read_file", map[string]any{"path": "/other/file.txt"})
	if err != nil {
		t.Fatalf("Default route failed: %v", err)
	}
	if srv != "fs-hdd" {
		t.Errorf("expected fs-hdd (default), got %q", srv)
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

func TestResolveBackendURL_RegistryURL(t *testing.T) {
	t.Parallel()

	g := &Gateway{
		cfg: Config{
			Registry: &registry.Registry{
				Servers: []*registry.Server{
					{Name: "youtube", URL: "ws://mcp-youtube.loom-hub.svc.cluster.local:8080"},
				},
			},
			ServerWSPath:       "/ws",
			ServerHostTemplate: "mcp-%s.%s.svc.cluster.local",
			HubNamespace:       "mcp-hub",
			ServerPort:         "8080",
			ServerScheme:       "ws",
		},
	}

	got, err := g.resolveBackendURL("youtube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "ws://mcp-youtube.loom-hub.svc.cluster.local:8080/ws"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBackendURL_TemplateFallback(t *testing.T) {
	t.Parallel()

	g := &Gateway{
		cfg: Config{
			Registry: &registry.Registry{
				Servers: []*registry.Server{
					{Name: "youtube"}, // no URL set
				},
			},
			ServerWSPath:       "/ws",
			ServerHostTemplate: "mcp-%s.%s.svc.cluster.local",
			HubNamespace:       "mcp-hub",
			ServerPort:         "8080",
			ServerScheme:       "ws",
		},
	}

	got, err := g.resolveBackendURL("youtube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "ws://mcp-youtube.mcp-hub.svc.cluster.local:8080/ws"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBackendURL_NilRegistry(t *testing.T) {
	t.Parallel()

	g := &Gateway{
		cfg: Config{
			ServerWSPath:       "/ws",
			ServerHostTemplate: "mcp-%s.%s.svc.cluster.local",
			HubNamespace:       "mcp-hub",
			ServerPort:         "8080",
			ServerScheme:       "ws",
		},
	}

	got, err := g.resolveBackendURL("youtube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "ws://mcp-youtube.mcp-hub.svc.cluster.local:8080/ws"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBackendURL_CrossNamespace(t *testing.T) {
	t.Parallel()

	g := &Gateway{
		cfg: Config{
			Registry: &registry.Registry{
				Servers: []*registry.Server{
					{Name: "youtube", URL: "ws://mcp-youtube.loom-hub.svc.cluster.local:8080"},
					{Name: "memory", URL: "ws://mcp-memory.mcp-hub.svc.cluster.local:8080"},
				},
			},
			ServerWSPath:       "/ws",
			ServerHostTemplate: "mcp-%s.%s.svc.cluster.local",
			HubNamespace:       "loom-hub",
			ServerPort:         "8080",
			ServerScheme:       "ws",
		},
	}

	// youtube → loom-hub namespace (from registry)
	got, err := g.resolveBackendURL("youtube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "ws://mcp-youtube.loom-hub.svc.cluster.local:8080/ws"; got != want {
		t.Errorf("youtube: got %q, want %q", got, want)
	}

	// memory → mcp-hub namespace (from registry)
	got, err = g.resolveBackendURL("memory")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "ws://mcp-memory.mcp-hub.svc.cluster.local:8080/ws"; got != want {
		t.Errorf("memory: got %q, want %q", got, want)
	}
}

func TestResolveBackendURL_TrailingSlashStripped(t *testing.T) {
	t.Parallel()

	g := &Gateway{
		cfg: Config{
			Registry: &registry.Registry{
				Servers: []*registry.Server{
					{Name: "youtube", URL: "ws://mcp-youtube.loom-hub.svc.cluster.local:8080/"},
				},
			},
			ServerWSPath: "/ws",
		},
	}

	got, err := g.resolveBackendURL("youtube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "ws://mcp-youtube.loom-hub.svc.cluster.local:8080/ws"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveBackendURL_UnknownServerFallsToTemplate(t *testing.T) {
	t.Parallel()

	g := &Gateway{
		cfg: Config{
			Registry: &registry.Registry{
				Servers: []*registry.Server{
					{Name: "youtube", URL: "ws://mcp-youtube.loom-hub.svc.cluster.local:8080"},
				},
			},
			ServerWSPath:       "/ws",
			ServerHostTemplate: "mcp-%s.%s.svc.cluster.local",
			HubNamespace:       "loom-hub",
			ServerPort:         "8080",
			ServerScheme:       "ws",
		},
	}

	got, err := g.resolveBackendURL("unknown-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "ws://mcp-unknown-server.loom-hub.svc.cluster.local:8080/ws"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatBackendHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tmpl       string
		serverName string
		namespace  string
		want       string
		wantErr    bool
	}{
		{
			name:       "simple name",
			tmpl:       "mcp-%s.%s.svc.cluster.local",
			serverName: "time",
			namespace:  "loom-hub",
			want:       "mcp-time.loom-hub.svc.cluster.local",
		},
		{
			name:       "underscore converted to hyphen",
			tmpl:       "mcp-%s.%s.svc.cluster.local",
			serverName: "agent_context",
			namespace:  "loom-hub",
			want:       "mcp-agent-context.loom-hub.svc.cluster.local",
		},
		{
			name:       "multiple underscores",
			tmpl:       "mcp-%s.%s.svc.cluster.local",
			serverName: "k8s_apps_k3s",
			namespace:  "loom-hub",
			want:       "mcp-k8s-apps-k3s.loom-hub.svc.cluster.local",
		},
		{
			name:       "no template verbs",
			tmpl:       "localhost",
			serverName: "agent_context",
			namespace:  "loom-hub",
			want:       "localhost",
		},
		{
			name:       "single verb template",
			tmpl:       "mcp-%s.local",
			serverName: "codebase_memory",
			namespace:  "loom-hub",
			want:       "mcp-codebase-memory.local",
		},
		{
			name:    "empty template",
			tmpl:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := formatBackendHost(tt.tmpl, tt.serverName, tt.namespace)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
