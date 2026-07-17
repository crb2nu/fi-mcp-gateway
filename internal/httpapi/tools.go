package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/libs/mcp-go"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/policy"
)

const (
	// defaultToolCallTimeout bounds a single REST tool invocation
	// (dial + handshake + tools/call). Override with FI_MCP_TOOL_CALL_TIMEOUT.
	defaultToolCallTimeout = 30 * time.Second

	// maxToolRequestBody caps the tool-arguments payload size.
	maxToolRequestBody = 1 << 20 // 1 MiB

	defaultProfile = "common"
)

// handleInvokeTool implements POST /api/v1/tools/{server}/{tool}.
//
// The route is intentionally keyless (mounted like GET /api/servers) so
// in-cluster callers can use it without credentials. The server-side
// guarantee is the registry always_allow allowlist: only tools listed
// there for the target server may be invoked, which keeps this route
// restricted to whatever the registry deems safe (read-only tools).
func (s *Server) handleInvokeTool(w http.ResponseWriter, r *http.Request) {
	serverName := strings.TrimSpace(r.PathValue("server"))
	toolName := strings.TrimSpace(r.PathValue("tool"))
	if serverName == "" || toolName == "" {
		writeError(w, http.StatusNotFound, "unknown server or tool")
		return
	}

	srv := s.reg.GetServer(serverName)
	if srv == nil || srv.IsLocalOnly() {
		writeError(w, http.StatusNotFound, fmt.Sprintf("unknown server: %s", serverName))
		return
	}

	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	if profile == "" {
		profile = defaultProfile
	}

	if !policy.IsToolAlwaysAllowed(s.reg, serverName, profile, toolName) {
		// When the registry declares static tool schemas for this server we
		// can distinguish an unknown tool from a known-but-not-allowlisted one.
		if spec, err := s.reg.GetServerSpec(serverName, profile); err == nil && spec != nil &&
			len(spec.Tools) > 0 && !hasStaticTool(spec.Tools, toolName) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("unknown tool: %s", toolName))
			return
		}
		writeError(w, http.StatusForbidden, fmt.Sprintf("tool not allowlisted: %s/%s", serverName, toolName))
		return
	}

	// Honor the configured gateway policy (deny lists etc.) exactly as the
	// WebSocket path does for the same tools/call.
	if s.pol != nil {
		decision := s.pol.Authorize(r.Context(), nil, policy.Request{
			Method:     "tools/call",
			ToolName:   toolName,
			ServerName: serverName,
			Profile:    profile,
		})
		if !decision.Allow {
			msg := "forbidden"
			if decision.Reason != "" {
				msg = "forbidden: " + decision.Reason
			}
			writeError(w, http.StatusForbidden, msg)
			return
		}
	}

	args, err := readToolArguments(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.toolTimeout)
	defer cancel()

	raw, err := s.ws.CallTool(ctx, serverName, toolName, args)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("tool call failed: %v", err))
		return
	}

	var result mcp.CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("invalid backend result: %v", err))
		return
	}
	if result.IsError {
		writeError(w, http.StatusBadGateway, "tool returned error: "+errorTextFromContent(result.Content))
		return
	}

	// Return the raw CallToolResult exactly as the backend produced it.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// readToolArguments reads and validates the request body as a JSON object
// of tool arguments. An empty body means no arguments.
func readToolArguments(r *http.Request) (json.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxToolRequestBody+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxToolRequestBody {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxToolRequestBody)
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}

	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, fmt.Errorf("request body must be a JSON object of tool arguments")
	}
	return json.RawMessage(trimmed), nil
}

// errorTextFromContent extracts human-readable error text from an
// isError CallToolResult.
func errorTextFromContent(content []mcp.Content) string {
	var parts []string
	for _, c := range content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	if len(parts) == 0 {
		return "tool reported an error"
	}
	return strings.Join(parts, "; ")
}

func hasStaticTool(tools []registry.ToolSchema, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func toolCallTimeoutFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("FI_MCP_TOOL_CALL_TIMEOUT"))
	if v == "" {
		return defaultToolCallTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return defaultToolCallTimeout
	}
	return d
}
