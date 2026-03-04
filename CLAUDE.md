# wa-mcp — WhatsApp MCP Server

WhatsApp MCP server focused on search and PM productivity. Based on eddmann/whatsapp-mcp with FTS5 full-text search, activity digests, unread tracking with two-way WhatsApp sync, and HTTP transport.

## Quick Reference

- **Repo**: github.com/jonzarecki/wa-mcp
- **Language**: Go 1.25, CGO required (SQLite FTS5)
- **Build**: `CGO_ENABLED=1 go build -tags sqlite_fts5 ./cmd/whatsapp-mcp`
- **Run**: Binary outputs QR code on first run. Scan with WhatsApp phone app.
- **Transport**: `MCP_TRANSPORT=stdio` (default) or `MCP_TRANSPORT=http` (for docker-compose)
- **Docker**: `make docker/build` then `make docker/run`

## Architecture

Single Go binary. No Python, no bridge process.

```
cmd/whatsapp-mcp/main.go     — All 9 tools, 2 prompts, 2 resources defined inline
internal/
  config/config.go            — Env config: DB_DIR, MCP_TRANSPORT, MCP_HTTP_ADDR, MCP_API_KEY, TZ
  domain/models.go            — Chat, Message, UnreadChatInfo, CatchUpSummary, etc.
  domain/temporal.go          — Natural timeframe parsing (today, this_week, etc.)
  media/                      — FFmpeg audio conversion
  service/chat_service.go     — ListChats, ListUnreadChats, MarkAsRead
  service/message_service.go  — ListMessages, SearchMessages, CatchUp, SendText, SendMedia
  store/store.go              — SQLite schema + FTS5 index + migrations
  store/queries.go            — SQL queries for chats, messages, unreads
  wa/client.go                — whatsmeow client init
  wa/connection.go            — QR auth, event handlers (Message, HistorySync, Receipt)
  wa/sync.go                  — Message persistence, history sync, read receipt handling
  wa/resolver.go              — Fuzzy recipient matching, contact name resolution
  wa/messaging.go             — Send text/media via whatsmeow
  wa/helpers.go               — Text/media extraction from protobuf
```

## Key Design Decisions

- **FTS5 is the core value** — CGO is required because FTS5 full-text search is the primary differentiator
- **INSERT OR IGNORE for history sync** — real-time messages are authoritative; history sync never overwrites is_read state
- **Two-way read sync** — is_read mirrors actual WhatsApp state via ReceiptTypeReadSelf events and outbound MarkRead()
- **History sync sends UnreadCount per chat** — only available at QR pairing time, not requestable later
- **Fuzzy recipient matching** — send_message accepts names ("Bob") not just JIDs, resolved via DB LIKE query

## Tools (9)

| Tool | ReadOnly | Description |
|------|----------|-------------|
| list_chats | yes | Search conversations, groups-only filter, pagination |
| list_messages | yes | Messages with fuzzy recipient, natural timeframes (today/this_week) |
| search_messages | yes | FTS5 cross-chat: "phrases", -exclusion, prefix*, boolean, ±2 context |
| catch_up | yes | Activity digest: active chats, questions for you, media summary |
| list_unread_chats | yes | Chats with unread counts synced from WhatsApp |
| get_connection_status | yes | Health check + DB stats |
| download_media | yes | Download media from message to local file |
| send_message | no | Text/media, fuzzy recipient, reply threading |
| mark_as_read | no | Mark read in DB + sync to WhatsApp (clears phone badges) |

## Prompts (2)

- **digest_group**: Multi-step workflow for group activity summary
- **catch_up_person**: Cross-chat person catch-up workflow

## Common Tasks

### Update whatsmeow (when 405 errors appear)
```bash
go get go.mau.fi/whatsmeow@latest
go mod tidy
# Fix any context.Context API changes in internal/wa/*.go
# GetGroupInfo, SendMessage, etc. may need ctx as first param
CGO_ENABLED=1 go build -tags sqlite_fts5 ./cmd/whatsapp-mcp
```

### Add a new tool
1. Add tool in `cmd/whatsapp-mcp/main.go` with `srv.AddTool(mcp.NewTool(...))`
2. Include annotations: `mcp.WithReadOnlyHintAnnotation()`, `mcp.WithDestructiveHintAnnotation()`, `mcp.WithIdempotentHintAnnotation()`
3. Add domain types in `internal/domain/models.go` if needed
4. Add queries in `internal/store/queries.go` if needed
5. Run `gofmt -w .` before committing

### Reset unread state
```bash
# Via Docker volume
docker run --rm -v local-automation-mcp_whatsapp-store:/data alpine sh -c \
  "apk add --quiet sqlite && sqlite3 /data/messages.db 'UPDATE messages SET is_read = 1;'"
```

### Force re-pair (new QR)
Delete whatsapp.db, keep messages.db. Restart container — new QR appears.

## Dependencies

- `go.mau.fi/whatsmeow` — WhatsApp Web multidevice API (critical: keep updated to avoid 405 errors)
- `github.com/mark3labs/mcp-go` — MCP protocol SDK (StreamableHTTP, tools, prompts, resources)
- `github.com/mattn/go-sqlite3` — CGO SQLite with FTS5 support
