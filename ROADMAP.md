# Project Roadmap

> Last Updated: January 2026

## Current Status

Bootstrap in progress:
- Server-bound WS entrypoint (`/ws?server=...`) ✅
- Tool-name routing (`server__tool`) ✅
- Connection pooling & idle reaping ✅
- Auth & Policy hooks ✅
- Prometheus Metrics (`/metrics`) ✅
- Context Injection (User/Tenant headers) ✅
- K8s Probes (/health, /ready) ✅

## Remaining Gaps

| Feature | Description | Status |
|---------|-------------|--------|
| Smart Routing | Content-based routing beyond tool name | Pending ([Issue](https://gitlab.flexinfer.ai/services/fi-mcp-gateway/-/issues/1)) |
| Hub Transport | Route requests to remote hub servers | Pending ([Issue](https://gitlab.flexinfer.ai/services/fi-mcp-gateway/-/issues/2)) |

## References

| Document | Purpose |
|----------|---------|
| [README.md](README.md) | Project documentation |
| [AGENTS.md](AGENTS.md) | Agent guidance |
