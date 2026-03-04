# Contributing to wa-mcp

Thanks for your interest in contributing! This project aims to be the best WhatsApp MCP server for AI-assisted PM workflows.

## Development Setup

### Prerequisites

- Go 1.25+ with CGO support (needed for SQLite FTS5)
- A C compiler (Xcode CLT on macOS, `gcc` on Linux)
- FFmpeg (optional, for audio message conversion)
- A WhatsApp account for testing

### Build

```bash
make build      # Build binary with FTS5 support
make run        # Build and run (will show QR code on first run)
make tidy       # Tidy go modules
make format     # Format code with gofmt
```

### Docker

```bash
make docker/build   # Build Docker image locally
make docker/run     # Run with volume mount
```

## Code Structure

```
cmd/whatsapp-mcp/main.go   — MCP tool/prompt/resource definitions
internal/
  config/                   — Environment-based configuration
  domain/                   — Data models and types
  media/                    — FFmpeg audio conversion
  service/                  — Business logic (chat, message services)
  store/                    — SQLite schema, migrations, queries
  wa/                       — whatsmeow client, sync, resolver
```

## Adding a New Tool

1. Add the tool definition in `cmd/whatsapp-mcp/main.go` using `srv.AddTool()`
2. Include tool annotations: `ReadOnlyHintAnnotation`, `DestructiveHintAnnotation`, `IdempotentHintAnnotation`
3. Add any new domain types in `internal/domain/models.go`
4. Add queries in `internal/store/queries.go` if needed
5. Update the README tool table
6. Run `make format` before committing

## Pull Request Guidelines

- Keep PRs focused — one feature or fix per PR
- Include a clear description of what and why
- Ensure `go build -tags sqlite_fts5`, `go vet ./...`, and `gofmt` all pass
- Update README if adding tools, prompts, or resources
- Test with a real WhatsApp connection if possible

## Architecture Decisions

- **Single binary** — everything in one Go process, no Python/bridge split
- **CGO required** — FTS5 full-text search is the core value proposition, and it needs CGO-enabled SQLite
- **INSERT OR IGNORE for history sync** — real-time messages are authoritative; history sync never overwrites them
- **Two-way read sync** — unread state mirrors actual WhatsApp, not a separate tracking system
- **Streamable HTTP transport** — runs as a docker-compose service alongside other MCP servers

## Reporting Issues

- **405 "client outdated" errors** — usually means whatsmeow needs updating. Check if there's a newer version at [go.mau.fi/whatsmeow](https://pkg.go.dev/go.mau.fi/whatsmeow)
- **Context.Context errors** — whatsmeow API changes. Look for functions that need `context.Context` as first parameter
- **QR code not appearing** — delete `store/whatsapp.db` and restart to force re-pairing
