package policy

import (
	"context"
	"os"
	"strings"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
)

type Request struct {
	Method     string
	ToolName   string
	ServerName string
	Profile    string
}

type Decision struct {
	Allow  bool
	Reason string
}

type Policy interface {
	Authorize(ctx context.Context, p *auth.Principal, req Request) Decision
}

type AllowAll struct{}

func (AllowAll) Authorize(ctx context.Context, p *auth.Principal, req Request) Decision {
	return Decision{Allow: true}
}

type PatternPolicy struct {
	defaultAllow bool

	allowTools []string
	denyTools  []string

	allowMethods []string
	denyMethods  []string

	registry                *registry.Registry
	respectRegistryAlwaysAllow bool
}

type Config struct {
	DefaultAllow bool

	AllowTools []string
	DenyTools  []string

	AllowMethods []string
	DenyMethods  []string

	Registry *registry.Registry

	RespectRegistryAlwaysAllow bool
}

func New(cfg Config) *PatternPolicy {
	return &PatternPolicy{
		defaultAllow:             cfg.DefaultAllow,
		allowTools:               cfg.AllowTools,
		denyTools:                cfg.DenyTools,
		allowMethods:             cfg.AllowMethods,
		denyMethods:              cfg.DenyMethods,
		registry:                 cfg.Registry,
		respectRegistryAlwaysAllow: cfg.RespectRegistryAlwaysAllow,
	}
}

func LoadConfigFromEnv(reg *registry.Registry) Config {
	def := strings.ToLower(envDefault("FI_MCP_POLICY_DEFAULT", "allow"))
	defaultAllow := def != "deny"

	respect := strings.EqualFold(envDefault("FI_MCP_POLICY_RESPECT_ALWAYS_ALLOW", "true"), "true")

	return Config{
		DefaultAllow: defaultAllow,
		AllowTools:   splitCSV(os.Getenv("FI_MCP_POLICY_ALLOW_TOOLS")),
		DenyTools:    splitCSV(os.Getenv("FI_MCP_POLICY_DENY_TOOLS")),
		AllowMethods: splitCSV(os.Getenv("FI_MCP_POLICY_ALLOW_METHODS")),
		DenyMethods:  splitCSV(os.Getenv("FI_MCP_POLICY_DENY_METHODS")),
		Registry:     reg,
		RespectRegistryAlwaysAllow: respect,
	}
}

func (p *PatternPolicy) Authorize(ctx context.Context, pr *auth.Principal, req Request) Decision {
	if req.Method == "" {
		return Decision{Allow: false, Reason: "missing method"}
	}

	// Method allow/deny.
	if matchAny(req.Method, p.denyMethods) {
		return Decision{Allow: false, Reason: "method denied"}
	}
	if len(p.allowMethods) > 0 && !matchAny(req.Method, p.allowMethods) {
		return Decision{Allow: false, Reason: "method not allowed"}
	}

	// Tool allow/deny.
	if req.Method == "tools/call" {
		tool := strings.TrimSpace(req.ToolName)
		if tool == "" {
			return Decision{Allow: false, Reason: "missing tool name"}
		}

		if matchAny(tool, p.denyTools) {
			return Decision{Allow: false, Reason: "tool denied"}
		}

		if p.respectRegistryAlwaysAllow && p.isRegistryAlwaysAllow(req.Profile, tool) {
			return Decision{Allow: true, Reason: "registry always_allow"}
		}

		if len(p.allowTools) > 0 {
			if matchAny(tool, p.allowTools) {
				return Decision{Allow: true, Reason: "tool allowed"}
			}
			return Decision{Allow: false, Reason: "tool not allowed"}
		}

		if p.defaultAllow {
			return Decision{Allow: true}
		}
		return Decision{Allow: false, Reason: "default deny"}
	}

	// Non-tool methods default allow unless constrained by allowMethods/denyMethods.
	return Decision{Allow: true}
}

func (p *PatternPolicy) isRegistryAlwaysAllow(profile, toolName string) bool {
	if p.registry == nil {
		return false
	}
	for _, srv := range p.registry.Servers {
		if srv == nil || srv.IsLocalOnly() {
			continue
		}
		spec, err := p.registry.GetServerSpec(srv.Name, profile)
		if err != nil || spec == nil {
			continue
		}
		for _, t := range spec.AlwaysAllow {
			if t == toolName {
				return true
			}
		}
	}
	return false
}

func matchAny(value string, patterns []string) bool {
	for _, p := range patterns {
		if matchPattern(value, p) {
			return true
		}
	}
	return false
}

// matchPattern supports:
// - "*" matches anything
// - "prefix*" prefix match
// - "*suffix" suffix match
// - exact match otherwise
func matchPattern(value, pattern string) bool {
	value = strings.TrimSpace(value)
	pattern = strings.TrimSpace(pattern)
	if value == "" || pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") && len(pattern) > 1 {
		return strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))
	}
	if strings.HasPrefix(pattern, "*") && len(pattern) > 1 {
		return strings.HasSuffix(value, strings.TrimPrefix(pattern, "*"))
	}
	return value == pattern
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

