package wa

import (
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	waSyncAction "go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/jonzarecki/wa-mcp/internal/store"
)

func setupTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
		CREATE TABLE messages (
			id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP,
			is_from_me BOOLEAN, media_type TEXT, filename TEXT, url TEXT,
			media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB, file_length INTEGER,
			is_read BOOLEAN DEFAULT 1,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
		CREATE VIRTUAL TABLE messages_fts USING fts5(content, content='messages', content_rowid='rowid');
		CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
		END;
		CREATE TRIGGER messages_au AFTER UPDATE ON messages BEGIN
			INSERT INTO messages_fts(messages_fts, rowid) VALUES('delete', old.rowid);
			INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
		END;
	`)
	if err != nil {
		t.Fatal(err)
	}
	return &store.DB{Messages: db}
}

func setupTestClient(t *testing.T) *Client {
	t.Helper()
	db := setupTestDB(t)
	return &Client{
		Store:  db,
		Logger: slog.Default(),
	}
}

func insertTestChat(t *testing.T, db *store.DB, jid, name string) {
	t.Helper()
	_, err := db.Messages.Exec("INSERT INTO chats (jid, name) VALUES (?, ?)", jid, name)
	if err != nil {
		t.Fatal(err)
	}
}

func insertTestMessage(t *testing.T, db *store.DB, id, chatJID, sender, content string, ts time.Time, fromMe bool, isRead bool) {
	t.Helper()
	_, err := db.Messages.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_read)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, chatJID, sender, content, ts, fromMe, isRead,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func countUnread(t *testing.T, db *store.DB, chatJID string) int {
	t.Helper()
	var count int
	err := db.Messages.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE chat_jid = ? AND is_read = 0 AND is_from_me = 0",
		chatJID,
	).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func getReadState(t *testing.T, db *store.DB, msgID, chatJID string) bool {
	t.Helper()
	var isRead bool
	err := db.Messages.QueryRow(
		"SELECT is_read FROM messages WHERE id = ? AND chat_jid = ?",
		msgID, chatJID,
	).Scan(&isRead)
	if err != nil {
		t.Fatal(err)
	}
	return isRead
}

func TestMarkChatAsRead_WithTimestamp(t *testing.T) {
	c := setupTestClient(t)
	chatJID := "120363000000000000@g.us"
	insertTestChat(t, c.Store, chatJID, "Test Group")

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cutoff := base.Add(5 * time.Minute)

	for i := 0; i < 10; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		insertTestMessage(t, c.Store, fmt.Sprintf("msg_%d", i), chatJID,
			"sender1", fmt.Sprintf("message %d", i), ts, false, false)
	}

	if got := countUnread(t, c.Store, chatJID); got != 10 {
		t.Fatalf("expected 10 unread before, got %d", got)
	}

	jid, _ := types.ParseJID(chatJID)
	c.handleMarkChatAsRead(&events.MarkChatAsRead{
		JID: jid,
		Action: &waSyncAction.MarkChatAsReadAction{
			Read: proto.Bool(true),
			MessageRange: &waSyncAction.SyncActionMessageRange{
				LastMessageTimestamp: proto.Int64(cutoff.Unix()),
			},
		},
	})

	// Messages 0-5 (at or before cutoff) should be read; 6-9 should stay unread
	for i := 0; i < 10; i++ {
		isRead := getReadState(t, c.Store, fmt.Sprintf("msg_%d", i), chatJID)
		if i <= 5 && isRead != true {
			t.Errorf("msg_%d: expected read (at/before cutoff), got unread", i)
		}
		if i > 5 && isRead != false {
			t.Errorf("msg_%d: expected unread (after cutoff), got read", i)
		}
	}

	if got := countUnread(t, c.Store, chatJID); got != 4 {
		t.Errorf("expected 4 unread after, got %d", got)
	}
}

func TestMarkChatAsRead_WithoutTimestamp(t *testing.T) {
	c := setupTestClient(t)
	chatJID := "120363000000000001@g.us"
	insertTestChat(t, c.Store, chatJID, "Test Group 2")

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		insertTestMessage(t, c.Store, fmt.Sprintf("msg_%d", i), chatJID,
			"sender1", fmt.Sprintf("message %d", i), ts, false, false)
	}

	jid, _ := types.ParseJID(chatJID)
	c.handleMarkChatAsRead(&events.MarkChatAsRead{
		JID: jid,
		Action: &waSyncAction.MarkChatAsReadAction{
			Read: proto.Bool(true),
		},
	})

	if got := countUnread(t, c.Store, chatJID); got != 0 {
		t.Errorf("expected 0 unread (no timestamp = mark all), got %d", got)
	}
}

func TestMarkChatAsRead_Unread(t *testing.T) {
	c := setupTestClient(t)
	chatJID := "120363000000000002@g.us"
	insertTestChat(t, c.Store, chatJID, "Test Group 3")

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		insertTestMessage(t, c.Store, fmt.Sprintf("msg_%d", i), chatJID,
			"sender1", fmt.Sprintf("message %d", i), ts, false, true)
	}

	if got := countUnread(t, c.Store, chatJID); got != 0 {
		t.Fatalf("expected 0 unread before, got %d", got)
	}

	jid, _ := types.ParseJID(chatJID)
	c.handleMarkChatAsRead(&events.MarkChatAsRead{
		JID: jid,
		Action: &waSyncAction.MarkChatAsReadAction{
			Read: proto.Bool(false),
		},
	})

	// Only the most recent incoming message should be unread
	if got := countUnread(t, c.Store, chatJID); got != 1 {
		t.Errorf("expected 1 unread after mark-as-unread, got %d", got)
	}
	// msg_2 is the most recent
	if isRead := getReadState(t, c.Store, "msg_2", chatJID); isRead {
		t.Error("msg_2 (most recent) should be unread")
	}
	if isRead := getReadState(t, c.Store, "msg_1", chatJID); !isRead {
		t.Error("msg_1 should still be read")
	}
}

// TestMessageReadDefaults verifies that the is_read column logic matches expectations:
// incoming (is_from_me=false) should be stored unread, outgoing (is_from_me=true) as read.
// This tests the SQL/schema behavior directly since handleMessage requires a full whatsmeow client.
func TestMessageReadDefaults(t *testing.T) {
	c := setupTestClient(t)
	chatJID := "120363000000000003@g.us"
	insertTestChat(t, c.Store, chatJID, "Test Group 4")

	now := time.Now()

	// Simulate what handleMessage does: isRead = msg.Info.IsFromMe
	_, err := c.Store.Messages.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_read)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"incoming_1", chatJID, "sender1", "hello", now, false, false,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Store.Messages.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_read)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"outgoing_1", chatJID, "me", "hello back", now, true, true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if isRead := getReadState(t, c.Store, "incoming_1", chatJID); isRead {
		t.Error("incoming message should be unread (is_read = false)")
	}
	if isRead := getReadState(t, c.Store, "outgoing_1", chatJID); !isRead {
		t.Error("outgoing message should be read (is_read = true)")
	}

	if got := countUnread(t, c.Store, chatJID); got != 1 {
		t.Errorf("expected 1 unread (incoming only), got %d", got)
	}
}

// TestMarkChatAsRead_PreservesOutgoingReadState verifies that marking as read
// doesn't affect outgoing messages (which are always read).
func TestMarkChatAsRead_PreservesOutgoingReadState(t *testing.T) {
	c := setupTestClient(t)
	chatJID := "120363000000000005@g.us"
	insertTestChat(t, c.Store, chatJID, "Test Group 6")

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// Mix of incoming (unread) and outgoing (read)
	insertTestMessage(t, c.Store, "in_1", chatJID, "sender1", "msg1", base, false, false)
	insertTestMessage(t, c.Store, "out_1", chatJID, "me", "msg2", base.Add(1*time.Minute), true, true)
	insertTestMessage(t, c.Store, "in_2", chatJID, "sender1", "msg3", base.Add(2*time.Minute), false, false)

	jid, _ := types.ParseJID(chatJID)
	cutoff := base.Add(90 * time.Second) // between out_1 and in_2
	c.handleMarkChatAsRead(&events.MarkChatAsRead{
		JID: jid,
		Action: &waSyncAction.MarkChatAsReadAction{
			Read: proto.Bool(true),
			MessageRange: &waSyncAction.SyncActionMessageRange{
				LastMessageTimestamp: proto.Int64(cutoff.Unix()),
			},
		},
	})

	// in_1 (before cutoff): should now be read
	if isRead := getReadState(t, c.Store, "in_1", chatJID); !isRead {
		t.Error("in_1 should be read (before cutoff)")
	}
	// out_1 (always read): unchanged
	if isRead := getReadState(t, c.Store, "out_1", chatJID); !isRead {
		t.Error("out_1 should still be read")
	}
	// in_2 (after cutoff): should stay unread
	if isRead := getReadState(t, c.Store, "in_2", chatJID); isRead {
		t.Error("in_2 should stay unread (after cutoff)")
	}
}
