# wa-mcp — WhatsApp MCP Server

A WhatsApp [Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server with FTS5 full-text search, activity digests, unread tracking, and HTTP transport.

Connect your personal WhatsApp account to AI assistants (Claude, Cursor, etc.) for searching messages, getting catch-up digests, tracking unread conversations, and sending messages.

## Features

- **FTS5 full-text search** across all conversations — boolean operators, exact phrases, exclusion, wildcards
- **`catch_up` digest** — active chats, message counts, questions directed at you, media summary
- **Unread tracking** — `list_unread_chats` + `mark_as_read` for inbox-zero workflows
- **Fuzzy recipient matching** — send to "Bob" not `972501234567@s.whatsapp.net`
- **Natural timeframes** — `today`, `this_week`, `last_3_days` instead of ISO-8601 timestamps
- **MCP Prompts** — guided workflows for group digests, person catch-up, action item extraction
- **Streamable HTTP transport** — runs as a docker-compose service alongside your MCP stack
- **stdio transport** — also works as a direct Cursor/Claude Desktop integration

## Quick Start

### Docker Compose (HTTP mode)

```yaml
whatsapp:
  image: ghcr.io/jonzarecki/wa-mcp:latest
  container_name: whatsapp-mcp
  restart: always
  ports:
    - "8085:8085"
  environment:
    - MCP_TRANSPORT=http
    - MCP_HTTP_ADDR=:8085
    - TZ=UTC
    - LOG_LEVEL=INFO
  volumes:
    - whatsapp-store:/app/store
```

Then add to your MCP client:

```json
{
  "mcpServers": {
    "whatsapp": {
      "url": "http://localhost:8085/mcp",
      "type": "http"
    }
  }
}
```

### Cursor / Claude Desktop (stdio mode)

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "path/to/wa-mcp"
    }
  }
}
```

### First Run

On first run, a QR code appears in the terminal/logs. Scan it with WhatsApp on your phone (Settings > Linked Devices > Link a Device). Session persists across restarts.

## MCP Tools

| Tool | Description |
|------|-------------|
| `list_chats` | List conversations, search by name/phone, groups-only filter, pagination |
| `list_messages` | Messages from a chat with fuzzy recipient, natural timeframes, date range |
| `search_messages` | FTS5 cross-chat search: boolean, phrases, wildcards, exclusion, ±2 context |
| `send_message` | Send text/media with fuzzy recipient matching, reply threading |
| `download_media` | Download media from a message to local storage |
| `get_connection_status` | WhatsApp connection health + database stats |
| `catch_up` | Activity digest: active chats, questions for you, media summary |
| `list_unread_chats` | Chats with unread message counts |
| `mark_as_read` | Mark messages in a chat as read |

## MCP Prompts

| Prompt | Description |
|--------|-------------|
| `digest_group` | Get a summary of recent group activity |
| `catch_up_person` | Find everything a person said across all chats |
| `extract_action_items` | Find commitments and asks from conversations |
| `search_topic` | Search all conversations for a topic using FTS5 |

## MCP Resources

| Resource | Description |
|----------|-------------|
| `whatsapp://guides/search-syntax` | FTS5 search operators reference |
| `whatsapp://guides/timeframes` | Valid timeframe presets |

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `DB_DIR` | `store` | Directory for SQLite databases and media |
| `LOG_LEVEL` | `INFO` | Logging level (DEBUG, INFO, WARN, ERROR) |
| `FFMPEG_PATH` | `ffmpeg` | Path to ffmpeg for audio conversion |
| `MCP_TRANSPORT` | `stdio` | Transport mode: `stdio` or `http` |
| `MCP_HTTP_ADDR` | `:8085` | HTTP listen address (when transport=http) |
| `MCP_API_KEY` | — | Optional Bearer token for HTTP auth |
| `TZ` | `UTC` | Timezone for timestamp display |

## Building from Source

Requires Go 1.24+ and a C compiler (CGO is needed for FTS5).

```bash
make build    # Build binary with FTS5 support
make run      # Build and run
make tidy     # Tidy go modules
```

## Credits

This project builds on the work of several WhatsApp MCP implementations:

- **[eddmann/whatsapp-mcp](https://github.com/eddmann/whatsapp-mcp)** — Base codebase, FTS5 search engine, `catch_up` digest, fuzzy recipient matching, natural timeframes
- **[felipeadeildo/whatsapp-mcp](https://github.com/felipeadeildo/whatsapp-mcp)** — MCP prompts and resources concept
- **[lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp)** — Original WhatsApp MCP ecosystem and community
- **[GOWA](https://github.com/aldinokemal/go-whatsapp-web-multidevice)** — Tool annotation patterns

## License

MIT — see [LICENSE](LICENSE).
