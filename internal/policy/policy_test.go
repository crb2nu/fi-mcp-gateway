package policy

import (
	"context"
	"testing"

	"gitlab.flexinfer.ai/libs/fi-mcp-kit/pkg/registry"
	"gitlab.flexinfer.ai/services/fi-mcp-gateway/internal/auth"
)

func TestPatternPolicy_DefaultAllow(t *testing.T) {
	t.Parallel()

	p := New(Config{DefaultAllow: true})
	d := p.Authorize(context.Background(), &auth.Principal{Subject: "u"}, Request{
		Method:   "tools/call",
		ToolName: "k8s__getPods",
		Profile:  "common",
	})
	if !d.Allow {
		t.Fatalf("expected allow, got %#v", d)
	}
}

func TestPatternPolicy_DefaultDeny(t *testing.T) {
	t.Parallel()

	p := New(Config{DefaultAllow: false})
	d := p.Authorize(context.Background(), &auth.Principal{Subject: "u"}, Request{
		Method:   "tools/call",
		ToolName: "k8s__getPods",
		Profile:  "common",
	})
	if d.Allow {
		t.Fatalf("expected deny, got %#v", d)
	}
}

func TestPatternPolicy_AllowList(t *testing.T) {
	t.Parallel()

	p := New(Config{
		DefaultAllow: false,
		AllowTools:   []string{"k8s__*"},
	})
	d := p.Authorize(context.Background(), &auth.Principal{Subject: "u"}, Request{
		Method:   "tools/call",
		ToolName: "k8s__getPods",
		Profile:  "common",
	})
	if !d.Allow {
		t.Fatalf("expected allow, got %#v", d)
	}
}

func TestPatternPolicy_DenyBeatsAllow(t *testing.T) {
	t.Parallel()

	p := New(Config{
		DefaultAllow: true,
		AllowTools:   []string{"k8s__*"},
		DenyTools:    []string{"k8s__delete*"},
	})
	d := p.Authorize(context.Background(), &auth.Principal{Subject: "u"}, Request{
		Method:   "tools/call",
		ToolName: "k8s__deletePods",
		Profile:  "common",
	})
	if d.Allow {
		t.Fatalf("expected deny, got %#v", d)
	}
}

func TestPatternPolicy_RespectsRegistryAlwaysAllow(t *testing.T) {
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

	p := New(Config{
		DefaultAllow:              false,
		Registry:                 reg,
		RespectRegistryAlwaysAllow: true,
	})
	d := p.Authorize(context.Background(), &auth.Principal{Subject: "u"}, Request{
		Method:   "tools/call",
		ToolName: "ping",
		Profile:  "common",
	})
	if !d.Allow {
		t.Fatalf("expected allow, got %#v", d)
	}
}

