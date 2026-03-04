Help the user set up wa-mcp for local development or Docker deployment.

## Steps

1. **Check prerequisites**: Go 1.25+, CGO support (C compiler), ffmpeg (optional)
   - On macOS: `xcode-select --install` for CGO
   - On Linux: `apt install gcc`
   - Check with: `go version` and `cc --version`

2. **Build locally**:
   ```bash
   CGO_ENABLED=1 go build -tags sqlite_fts5 -o bin/wa-mcp ./cmd/whatsapp-mcp
   ```
   If build fails with FTS5 errors, CGO is not enabled or C compiler is missing.

3. **First run** (stdio mode):
   ```bash
   ./bin/wa-mcp
   ```
   QR code appears — scan with WhatsApp > Settings > Linked Devices > Link a Device.
   Wait for "history sync persisted messages" log lines to finish.

4. **Docker deployment** (HTTP mode):
   ```bash
   make docker/build
   docker run -d --name wa-mcp -p 8085:8085 \
     -e MCP_TRANSPORT=http -e MCP_HTTP_ADDR=:8085 -e TZ=UTC \
     -v wa-mcp-store:/app/store \
     ghcr.io/jonzarecki/wa-mcp:latest
   docker logs -f wa-mcp  # scan QR from logs
   ```

5. **Connect to MCP client**:
   - Cursor/Claude Desktop (stdio): `{"mcpServers": {"whatsapp": {"command": "path/to/wa-mcp"}}}`
   - Cursor/Claude Desktop (HTTP): `{"mcpServers": {"whatsapp": {"url": "http://localhost:8085/mcp", "type": "http"}}}`

6. **Verify**: Try calling `list_chats` or `catch_up` tool.

## Troubleshooting

- **405 "client outdated"**: `go get go.mau.fi/whatsmeow@latest && go mod tidy`, fix context.Context changes, rebuild
- **QR not showing**: Delete `store/whatsapp.db`, restart
- **FTS5 not available**: Ensure `CGO_ENABLED=1` and `-tags sqlite_fts5` in build command
- **Port 8085 refused**: Check `MCP_TRANSPORT=http` is set, container is running
