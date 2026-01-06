# AGENTS.md

Guidance for coding agents working in this repository.

## Project Overview
- **fi-mcp-gateway**: Go-first MCP Hub gateway service.
- **Purpose**: Enterprise-grade context gateway with auth, policy, and pooling.
- **Stack**: Go (1.24+).

## Key Paths
- `cmd/` application entrypoints.
- `internal/` internal packages.

## Common Commands
- `go run ./cmd/...`
- `go test ./...`

## Library Dependencies

- `libs/mcp-go` - Core MCP SDK for server implementations
- `libs/fi-mcp-kit` - Enterprise orchestration toolkit (registry, config generation)

## Code Conventions
- Follow standard Go conventions (Effective Go).

## Configuration
- See `README.md` for environment variables (`FI_MCP_*`).

## Planning
- See `ROADMAP.md` for project status and plans.
