package mcpws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/pool"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/libs/mcp-go"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/logger"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/metrics"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/policy"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/quota"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/usage"
)

// RateLimiter is an interface for rate limiting within the gateway.
type RateLimiter interface {
	// CheckMessage checks if a message should be rate limited.
	// Returns true if the message is allowed, false if it should be blocked.
	CheckMessage(tenant, user, tool string) (allowed bool, retryAfter time.Duration, err error)
}

type Config struct {
	Registry      *registry.Registry
	Authenticator auth.Authenticator
	Policy        policy.Policy
	RateLimiter   RateLimiter
	UsageTracker  usage.Tracker
	QuotaManager  quota.Manager

	HubNamespace string
	ServerPort   string
	ServerWSPath string
	ServerScheme string
	// ServerHostTemplate formats to the backend host. The default expects both
	// server name and namespace: "mcp-%s.%s.svc.cluster.local".
	// If it contains no formatting verbs, it is used as-is (useful for local testing).
	// If it contains only one formatting verb, it is formatted with the server name.
	// If it contains a literal percent sign, it must be escaped as "%%".
	ServerHostTemplate string

	HandshakeTimeout time.Duration
	DialTimeout      time.Duration

	BackendMaxIdle     int
	BackendMaxOpen     int
	BackendIdleTimeout time.Duration
}

type Gateway struct {
	cfg Config

	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*session
}

func New(cfg Config) *Gateway {
	if cfg.Authenticator == nil {
		cfg.Authenticator = auth.NoAuth{}
	}
	if cfg.Policy == nil {
		cfg.Policy = policy.AllowAll{}
	}
	if cfg.HubNamespace == "" {
		cfg.HubNamespace = envDefault("MCP_HUB_NAMESPACE", "mcp-hub")
	}
	if cfg.ServerPort == "" {
		cfg.ServerPort = envDefault("MCP_SERVER_PORT", "8080")
	}
	if cfg.ServerWSPath == "" {
		cfg.ServerWSPath = envDefault("MCP_SERVER_WS_PATH", "/ws")
	}
	if cfg.ServerScheme == "" {
		cfg.ServerScheme = envDefault("MCP_SERVER_SCHEME", "ws")
	}
	if cfg.ServerHostTemplate == "" {
		cfg.ServerHostTemplate = envDefault("MCP_SERVER_HOST_TEMPLATE", "mcp-%s.%s.svc.cluster.local")
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.BackendMaxIdle <= 0 {
		cfg.BackendMaxIdle = envIntDefault("FI_MCP_BACKEND_MAX_IDLE", 2)
	}
	if cfg.BackendMaxOpen <= 0 {
		cfg.BackendMaxOpen = envIntDefault("FI_MCP_BACKEND_MAX_OPEN", 10)
	}
	if cfg.BackendIdleTimeout <= 0 {
		cfg.BackendIdleTimeout = envDurationDefault("FI_MCP_BACKEND_IDLE_TIMEOUT", 5*time.Minute)
	}

	return &Gateway{
		cfg: cfg,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: cfg.HandshakeTimeout,
			CheckOrigin:      func(r *http.Request) bool { return true },
		},
		sessions: make(map[string]*session),
	}
}

func (g *Gateway) HandleWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	principal, err := g.cfg.Authenticator.Authenticate(r)
	if err != nil {
		metrics.ErrorsTotal.WithLabelValues("auth").Inc()
		if errors.Is(err, auth.ErrUnauthorized) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "auth error", http.StatusInternalServerError)
		return
	}

	// Compatibility with the TS gateway:
	// - client connects with ?profile=...&server=...
	// - initialize has no toolName, so server binding is required for v0.
	profile := r.URL.Query().Get("profile")
	if profile == "" {
		profile = headerOrEmpty(r, "X-MCP-Profile")
	}
	if profile == "" {
		profile = "common"
	}

	serverName := r.URL.Query().Get("server")
	if serverName == "" {
		serverName = headerOrEmpty(r, "X-MCP-Server")
	}
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		http.Error(w, "missing server (use ?server=...)", http.StatusBadRequest)
		return
	}

	if !g.serverExistsAndHubDeployable(serverName) {
		http.Error(w, "unknown or local-only server", http.StatusBadRequest)
		return
	}

	clientConn, err := g.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	s := newSession(g, principal, profile, serverName, clientConn)
	g.trackSession(s)
	defer g.untrackSession(s.id)
	defer s.Close()

	s.Run(ctx)
}

func (g *Gateway) serverExistsAndHubDeployable(serverName string) bool {
	if g.cfg.Registry == nil {
		return false
	}
	for _, s := range g.cfg.Registry.Servers {
		if s == nil {
			continue
		}
		if s.Name == serverName {
			return !s.IsLocalOnly()
		}
	}
	return false
}

func (g *Gateway) dialBackend(ctx context.Context, serverName string) (*websocket.Conn, error) {
	host, err := formatBackendHost(g.cfg.ServerHostTemplate, serverName, g.cfg.HubNamespace)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s:%s", host, g.cfg.ServerPort)
	url := fmt.Sprintf("%s://%s%s", g.cfg.ServerScheme, endpoint, g.cfg.ServerWSPath)

	dialer := websocket.Dialer{
		HandshakeTimeout: g.cfg.DialTimeout,
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			d.Timeout = g.cfg.DialTimeout
			return d.DialContext(ctx, network, addr)
		},
	}

	start := time.Now()
	conn, _, err := dialer.DialContext(ctx, url, nil)
	metrics.BackendDialDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ErrorsTotal.WithLabelValues("backend_dial").Inc()
		return nil, err
	}
	return conn, nil
}

func (g *Gateway) dialBackendTransport(ctx context.Context, serverName string) (mcp.Transport, error) {
	ws, err := g.dialBackend(ctx, serverName)
	if err != nil {
		return nil, err
	}
	return newBackendTransport(ws), nil
}

func formatBackendHost(tmpl, serverName, namespace string) (string, error) {
	if tmpl == "" {
		return "", fmt.Errorf("empty host template")
	}
	if !strings.Contains(tmpl, "%") {
		return tmpl, nil
	}

	// Prefer the 2-arg format used by the default template.
	out := fmt.Sprintf(tmpl, serverName, namespace)
	if !strings.Contains(out, "%!") {
		return out, nil
	}

	// Fallback to a 1-arg template (useful for local testing).
	out = fmt.Sprintf(tmpl, serverName)
	if !strings.Contains(out, "%!") {
		return out, nil
	}

	return "", fmt.Errorf("invalid host template %q (expected no verbs, 1 verb, or 2 verbs)", tmpl)
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
	if err != nil || n <= 0 {
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

func headerOrEmpty(r *http.Request, name string) string {
	v := r.Header.Get(name)
	if v == "" {
		return ""
	}
	return v
}

type session struct {
	gw *Gateway

	id          string
	principal   *auth.Principal
	tenantID    string
	profile     string
	boundServer string
	client      *websocket.Conn
	createdAt   time.Time
	lastActive  time.Time

	clientWriteMu sync.Mutex

	backendsMu sync.Mutex
	backends   map[string]*backend
	pool       *pool.Pool
}

type backend struct {
	server       string
	conn         *pool.Conn
	tr           *backendTransport
	done         chan struct{}
	lastUsedNano atomic.Int64 // atomic for race-safe access
}

func (b *backend) touch() {
	b.lastUsedNano.Store(time.Now().UnixNano())
}

func (b *backend) lastUsed() time.Time {
	return time.Unix(0, b.lastUsedNano.Load())
}

// syncLastUsed copies the atomic lastUsed to pool.Conn for pool internal use.
// Must be called under appropriate lock before returning conn to pool.
func (b *backend) syncLastUsed() {
	if b.conn != nil {
		b.conn.LastUsed = b.lastUsed()
	}
}

type jsonrpcEnvelope struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type callToolParams struct {
	Name string `json:"name"`
}

func newSession(gw *Gateway, principal *auth.Principal, profile, server string, client *websocket.Conn) *session {
	now := time.Now()

	// Extract tenant ID from principal claims
	tenantID := ""
	if principal != nil {
		tenantID = principal.TenantID()
	}

	return &session{
		gw:          gw,
		id:          fmt.Sprintf("sess-%d", time.Now().UnixNano()),
		principal:   principal,
		tenantID:    tenantID,
		profile:     profile,
		boundServer: server,
		client:      client,
		createdAt:   now,
		lastActive:  now,
		backends:    make(map[string]*backend),
		pool: pool.New(pool.Config{
			MaxIdle:     gw.cfg.BackendMaxIdle,
			MaxOpen:     gw.cfg.BackendMaxOpen,
			IdleTimeout: gw.cfg.BackendIdleTimeout,
			DialFunc:    gw.dialBackendTransport,
		}),
	}
}

func (s *session) Close() {
	s.backendsMu.Lock()
	backends := make([]*backend, 0, len(s.backends))
	for _, b := range s.backends {
		if b != nil {
			backends = append(backends, b)
		}
	}
	s.backendsMu.Unlock()

	for _, b := range backends {
		_ = b.tr.Close()
	}

	_ = s.pool.Close()
	_ = s.client.Close()
}

func (s *session) Run(ctx context.Context) {
	go s.reapBackendsLoop(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgType, msg, err := s.client.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}

		msgTypeStr := "binary"
		if msgType == websocket.TextMessage {
			msgTypeStr = "text"
		}
		metrics.MessagesTotal.WithLabelValues("in", msgTypeStr).Inc()

		s.lastActive = time.Now()

		route, err := s.routeMessage(msg)
		if err != nil {
			metrics.ErrorsTotal.WithLabelValues("route").Inc()
			s.sendJSONRPCError(msg, err.Error())
			continue
		}
		if !s.gw.serverExistsAndHubDeployable(route.serverName) {
			metrics.ErrorsTotal.WithLabelValues("route").Inc()
			s.sendJSONRPCError(msg, "unknown or local-only server")
			continue
		}

		// Rate limit check
		user := ""
		if s.principal != nil {
			user = s.principal.Subject
		}
		if s.gw.cfg.RateLimiter != nil {
			allowed, retryAfter, err := s.gw.cfg.RateLimiter.CheckMessage(s.tenantID, user, route.toolName)
			if err != nil {
				logger.Error("rate limit check failed", "error", err, "tenant", s.tenantID, "user", user)
			}
			if !allowed {
				metrics.ErrorsTotal.WithLabelValues("ratelimit").Inc()
				metrics.RateLimitedTotal.WithLabelValues(s.tenantID, user).Inc()
				errMsg := fmt.Sprintf("rate limited: retry after %v", retryAfter)
				s.sendJSONRPCError(msg, errMsg)
				continue
			}
		}

		// Quota check for tool calls
		if s.gw.cfg.QuotaManager != nil && route.method == "tools/call" {
			result, err := s.gw.cfg.QuotaManager.Check(ctx, s.tenantID, user, quota.QuotaTypeToolCalls, 1)
			if err != nil {
				metrics.ErrorsTotal.WithLabelValues("quota").Inc()
				s.sendJSONRPCError(msg, "quota exceeded")
				continue
			}
			if !result.Allowed {
				metrics.ErrorsTotal.WithLabelValues("quota").Inc()
				s.sendJSONRPCError(msg, fmt.Sprintf("quota exceeded: %d/%d tool calls used", result.Current, result.Limit))
				continue
			}
		}

		decision := s.gw.cfg.Policy.Authorize(ctx, s.principal, policy.Request{
			Method:     route.method,
			ToolName:   route.toolName,
			ServerName: route.serverName,
			Profile:    s.profile,
		})
		if !decision.Allow {
			msgText := "forbidden"
			if decision.Reason != "" {
				msgText = "forbidden: " + decision.Reason
			}
			s.sendJSONRPCError(msg, msgText)
			continue
		}

		b, err := s.getBackend(ctx, route.serverName)
		if err != nil {
			s.sendJSONRPCError(msg, "failed to connect to backend")
			continue
		}

		b.touch()
		startTime := time.Now()
		if err := b.tr.WriteMessage(msgType, msg); err != nil {
			s.dropBackend(route.serverName, true)
			s.sendJSONRPCError(msg, "backend write failed")
			// Track failed usage
			s.trackUsage(ctx, route, user, startTime, false, "backend_write_failed")
			continue
		}

		// Increment quota for successful tool calls
		if s.gw.cfg.QuotaManager != nil && route.method == "tools/call" {
			if err := s.gw.cfg.QuotaManager.Increment(ctx, s.tenantID, user, quota.QuotaTypeToolCalls, 1); err != nil {
				logger.Error("quota increment failed", "error", err, "tenant", s.tenantID, "user", user)
			}
		}

		// Track successful usage
		s.trackUsage(ctx, route, user, startTime, true, "")
	}
}

func (s *session) reapBackendsLoop(ctx context.Context) {
	idleTimeout := s.gw.cfg.BackendIdleTimeout
	if idleTimeout <= 0 {
		return
	}

	tickEvery := idleTimeout / 2
	if tickEvery < 250*time.Millisecond {
		tickEvery = 250 * time.Millisecond
	}

	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now()

		var toDrop []string
		s.backendsMu.Lock()
		for name, b := range s.backends {
			if b == nil || b.conn == nil {
				continue
			}
			if now.Sub(b.lastUsed()) > idleTimeout {
				toDrop = append(toDrop, name)
			}
		}
		s.backendsMu.Unlock()

		for _, name := range toDrop {
			s.dropBackend(name, true)
		}
	}
}

type routeDecision struct {
	method     string
	toolName   string
	serverName string
}

func (s *session) routeMessage(raw []byte) (routeDecision, error) {
	var env jsonrpcEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return routeDecision{}, fmt.Errorf("invalid json-rpc message")
	}

	switch env.Method {
	case "tools/call":
		var p callToolParams
		if len(env.Params) > 0 {
			_ = json.Unmarshal(env.Params, &p)
		}
		if strings.TrimSpace(p.Name) == "" {
			if s.boundServer != "" {
				return routeDecision{method: env.Method, serverName: s.boundServer}, nil
			}
			return routeDecision{}, fmt.Errorf("invalid tools/call: missing params.name")
		}

		if server := s.gw.routeByToolName(s.profile, p.Name); server != "" {
			return routeDecision{method: env.Method, toolName: p.Name, serverName: server}, nil
		}
		if s.boundServer != "" {
			return routeDecision{method: env.Method, toolName: p.Name, serverName: s.boundServer}, nil
		}
		return routeDecision{}, fmt.Errorf("no route for tool")

	default:
		if s.boundServer == "" {
			return routeDecision{}, fmt.Errorf("no server bound (use ?server=...)")
		}
		return routeDecision{method: env.Method, serverName: s.boundServer}, nil
	}
}

func (g *Gateway) routeByToolName(profile, toolName string) string {
	if g.cfg.Registry == nil {
		return ""
	}

	for _, srv := range g.cfg.Registry.Servers {
		if srv == nil {
			continue
		}
		if srv.IsLocalOnly() {
			continue
		}

		spec, err := g.cfg.Registry.GetServerSpec(srv.Name, profile)
		if err == nil && spec != nil {
			for _, allowed := range spec.AlwaysAllow {
				if allowed == toolName {
					return srv.Name
				}
			}
		}

		prefix := srv.Name + "__"
		if strings.HasPrefix(toolName, prefix) {
			return srv.Name
		}
	}

	return ""
}

func (s *session) getBackend(ctx context.Context, serverName string) (*backend, error) {
	var toDrop *backend
	s.backendsMu.Lock()
	if existing := s.backends[serverName]; existing != nil && existing.conn != nil {
		if existing.conn.Healthy {
			s.backendsMu.Unlock()
			return existing, nil
		}
		delete(s.backends, serverName)
		toDrop = existing
	}
	s.backendsMu.Unlock()

	if toDrop != nil && toDrop.conn != nil {
		toDrop.conn.Healthy = false
		toDrop.syncLastUsed()
		_ = toDrop.tr.Close()
		s.pool.Put(toDrop.conn)
	}

	conn, err := s.pool.Get(ctx, serverName)
	if err != nil {
		return nil, err
	}

	tr, ok := conn.Transport.(*backendTransport)
	if !ok || tr == nil {
		conn.Healthy = false
		s.pool.Put(conn)
		return nil, fmt.Errorf("unexpected backend transport type")
	}

	b := &backend{
		server: serverName,
		conn:   conn,
		tr:     tr,
		done:   make(chan struct{}),
	}
	b.touch() // Initialize lastUsedNano

	s.backendsMu.Lock()
	if prev := s.backends[serverName]; prev != nil {
		s.backendsMu.Unlock()
		conn.Healthy = false
		s.pool.Put(conn)
		return prev, nil
	}
	s.backends[serverName] = b
	s.backendsMu.Unlock()

	go s.pumpBackendToClient(ctx, b)

	return b, nil
}

func (s *session) dropBackend(serverName string, unhealthy bool) {
	s.backendsMu.Lock()
	b := s.backends[serverName]
	delete(s.backends, serverName)
	s.backendsMu.Unlock()

	if b == nil || b.conn == nil {
		return
	}

	if unhealthy {
		b.conn.Healthy = false
	}
	b.syncLastUsed()
	_ = b.tr.Close()
	s.pool.Put(b.conn)
}

func (s *session) pumpBackendToClient(ctx context.Context, b *backend) {
	defer close(b.done)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msgType, msg, err := b.tr.ReadMessage()
		if err != nil {
			metrics.ErrorsTotal.WithLabelValues("backend_io").Inc()
			s.dropBackend(b.server, true)
			return
		}

		b.touch()
		if err := s.writeToClient(msgType, msg); err != nil {
			return
		}

		msgTypeStr := "binary"
		if msgType == websocket.TextMessage {
			msgTypeStr = "text"
		}
		metrics.MessagesTotal.WithLabelValues("out", msgTypeStr).Inc()
	}
}

func (s *session) writeToClient(msgType int, msg []byte) error {
	s.clientWriteMu.Lock()
	defer s.clientWriteMu.Unlock()
	return s.client.WriteMessage(msgType, msg)
}

func (s *session) sendJSONRPCError(requestRaw []byte, message string) {
	var req map[string]any
	_ = json.Unmarshal(requestRaw, &req)

	id, ok := req["id"]
	if !ok {
		id = nil
	}

	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32000,
			"message": message,
		},
	}

	s.clientWriteMu.Lock()
	defer s.clientWriteMu.Unlock()
	if s.client != nil {
		_ = s.client.WriteJSON(resp)
	}
}

// trackUsage records a usage event for tool calls.
func (s *session) trackUsage(ctx context.Context, route routeDecision, userID string, startTime time.Time, success bool, errorCode string) {
	if s.gw.cfg.UsageTracker == nil {
		return
	}

	// Only track tool calls
	if route.method != "tools/call" {
		return
	}

	event := usage.Event{
		ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
		Timestamp: startTime,
		TenantID:  s.tenantID,
		UserID:    userID,
		ToolName:  route.toolName,
		ServerID:  route.serverName,
		Duration:  time.Since(startTime),
		Success:   success,
		ErrorCode: errorCode,
		Metadata: map[string]string{
			"session_id": s.id,
			"profile":    s.profile,
		},
	}

	s.gw.cfg.UsageTracker.Track(ctx, event)
}

func (g *Gateway) trackSession(s *session) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessions[s.id] = s
	metrics.SessionsActive.Inc()
	if s.tenantID != "" {
		metrics.SessionsActiveByTenant.WithLabelValues(s.tenantID).Inc()
	}
	logger.Info("websocket session opened",
		slog.String("session_id", s.id),
		slog.String("tenant", s.tenantID),
		slog.String("profile", s.profile),
		slog.String("server", s.boundServer))
}

func (g *Gateway) untrackSession(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.sessions[id]; s != nil && s.tenantID != "" {
		metrics.SessionsActiveByTenant.WithLabelValues(s.tenantID).Dec()
	}
	delete(g.sessions, id)
	metrics.SessionsActive.Dec()
	logger.Info("websocket session closed", slog.String("session_id", id))
}

type backendTransport struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	readMu  sync.Mutex
}

func newBackendTransport(ws *websocket.Conn) *backendTransport {
	return &backendTransport{ws: ws}
}

func (t *backendTransport) WriteMessage(msgType int, msg []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.ws.WriteMessage(msgType, msg)
}

func (t *backendTransport) ReadMessage() (int, []byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()
	return t.ws.ReadMessage()
}

func (t *backendTransport) Send(ctx context.Context, msg *mcp.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return t.WriteMessage(websocket.TextMessage, data)
}

func (t *backendTransport) Recv(ctx context.Context) (*mcp.Message, error) {
	_, data, err := t.ReadMessage()
	if err != nil {
		return nil, err
	}
	var msg mcp.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (t *backendTransport) Close() error {
	return t.ws.Close()
}
