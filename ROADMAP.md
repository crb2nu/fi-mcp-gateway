# fi-mcp-gateway Roadmap

> Last Updated: 2026-07-02
> Tier: 2 (see workspace AGENTS.md "Portfolio Tiers")
> Tracking Issue: none (not in the active-tracking set; backlog linked below)

## Current Status

Go MCP gateway: server-bound WS entrypoint (`/ws?server=...`), tool-name
routing (`server__tool`), connection pooling + idle reaping, auth/policy
hooks, Prometheus metrics, K8s probes — all landed by early 2026. Deployed
and healthy as the `loom-gateway` Deployment in the loom-hub stack. Code is
LOW-ACTIVITY: the last touch was a module tidy for the readonly smoke on
2026-06-02; the last substantive work was Feb 2026 (rate-limiter nil/panic
fixes, backend DNS normalization, registry-URL routing preference).

Evidence: last 20 default-branch commits (window 2026-01-18 → 2026-06-02);
`kubectl get deploy loom-gateway -n loom-hub` → 2/2 available, image
`registry.harbor.lan/mcp/fi-mcp-gateway:20260602-233717`; Flux image
automation (fi-mcp-gateway-imagepolicy/-imagerepository in platform/gitops);
default-branch pipeline success 2026-06-02
(.loom/62-functional-health-baseline-2026-07-02.md).

Grooming 2026-07-02: roadmap feature issues #1 (Smart Routing) and #2 (Hub
Transport) relabeled P3 — parked until the gateway needs them.

- **Plan store**: plan-workspace-portfolio-refresh-2026-h2-roadmaps-quality-baselin-f3db23
- **Deployed**: k3s `loom-hub/loom-gateway` (2 replicas) via Flux (image automation on timestamp tags)
- **CI**: platform/gitops go template (+ tech-radar scanner)

## Now

- None in flight — gateway is stable in production; maintain mode.

## Next

- [ ] Smart Routing — content-based routing beyond tool name (#1, P3)
- [ ] Hub Transport — route requests to remote hub servers (#2, P3)

## Later

- Revisit tier/investment if the loom-hub MCP surface grows beyond what
  static routing handles

## Backlog

Full backlog: [P1 issues](https://gitlab.flexinfer.ai/services/fi-mcp-gateway/-/issues?label_name[]=P1) ·
[P2](https://gitlab.flexinfer.ai/services/fi-mcp-gateway/-/issues?label_name[]=P2) ·
[P3](https://gitlab.flexinfer.ai/services/fi-mcp-gateway/-/issues?label_name[]=P3) ·
[Milestones](https://gitlab.flexinfer.ai/services/fi-mcp-gateway/-/milestones)
