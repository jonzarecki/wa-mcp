# Roadmap

## Planned

### Archive-aware unread filtering
`list_unread_chats` currently includes archived chats, inflating unread counts vs what the phone's main chat list shows. Fix:
- Add `archived BOOLEAN DEFAULT 0` column to `chats` table
- Handle `events.Archive` app state events to track archive/unarchive
- Set `EmitAppStateEventsOnFullSync = true` (already done) so archive state syncs on reconnect
- Add `include_archived` param to `list_unread_chats` (default `false`)
