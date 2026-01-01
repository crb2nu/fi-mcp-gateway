# Project Roadmap

> Last Updated: January 2026

## Current Status

Bootstrap in progress:
- Server-bound WS entrypoint (`/ws?server=...`) ✅
- Tool-name routing (`server__tool`) ✅
- Connection pooling & idle reaping ✅
- Auth & Policy hooks ✅
- Prometheus Metrics (`/metrics`) ✅

## Remaining Gaps

| Feature | Description | Status |
|---------|-------------|--------|
| K8s Probes | Liveness/Readiness probes in deployment manifests | Pending |
| Context Injection | Inject user context into backend requests | Pending |
| Smart Routing | Content-based routing beyond tool name | Pending |

## References

| Document | Purpose |
|----------|---------|
| [README.md](README.md) | Project documentation |
| [AGENTS.md](AGENTS.md) | Agent guidance |
