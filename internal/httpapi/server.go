package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/mcpws"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/policy"
)

type Server struct {
	reg *registry.Registry
	ws  *mcpws.Gateway
}

type Config struct {
	Registry       *registry.Registry
	Authenticator  auth.Authenticator
	Policy         policy.Policy
	RateLimiter    mcpws.RateLimiter
}

func New(cfg Config) *Server {
	return &Server{
		reg: cfg.Registry,
		ws: mcpws.New(mcpws.Config{
			Registry:       cfg.Registry,
			Authenticator:  cfg.Authenticator,
			Policy:         cfg.Policy,
			RateLimiter:    cfg.RateLimiter,
		}),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		})
	})

	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ready := s.reg != nil && len(s.reg.Servers) > 0
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{
			"ready":   ready,
			"servers": len(s.reg.Servers),
		})
	})

	mux.HandleFunc("GET /api/servers", func(w http.ResponseWriter, r *http.Request) {
		type item struct {
			Name        string   `json:"name"`
			Categories  []string `json:"categories"`
			Description string   `json:"description,omitempty"`
		}

		out := make([]item, 0, len(s.reg.Servers))
		for _, srv := range s.reg.Servers {
			if srv == nil {
				continue
			}
			desc := ""
			if srv.Common != nil {
				desc = srv.Common.Description
			}
			out = append(out, item{
				Name:        srv.Name,
				Categories:  srv.Categories,
				Description: desc,
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{"servers": out})
	})

	// WebSocket MCP gateway (server-bound, v0).
	mux.HandleFunc("/ws", s.ws.HandleWS)

	// Metrics endpoint.
	mux.Handle("/metrics", promhttp.Handler())

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
