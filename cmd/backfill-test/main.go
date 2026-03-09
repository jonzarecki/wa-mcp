package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

const storeDir = "store"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: backfill-test <chat-jid> [count] [requests]\n")
		fmt.Fprintf(os.Stderr, "  chat-jid:  e.g. 120363405191791100@g.us\n")
		fmt.Fprintf(os.Stderr, "  count:     messages per request (default 50)\n")
		fmt.Fprintf(os.Stderr, "  requests:  number of requests (default 3)\n")
		fmt.Fprintf(os.Stderr, "\nDebug mode (no backfill, just diagnostics):\n")
		fmt.Fprintf(os.Stderr, "  backfill-test --debug\n")
		os.Exit(1)
	}

	debugMode := os.Args[1] == "--debug"
	chatJIDStr := ""
	count := 50
	maxRequests := 3

	if !debugMode {
		chatJIDStr = os.Args[1]
		if len(os.Args) >= 3 {
			fmt.Sscanf(os.Args[2], "%d", &count)
		}
		if len(os.Args) >= 4 {
			fmt.Sscanf(os.Args[3], "%d", &maxRequests)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --- Debug checkpoint 1: Open whatsmeow device store ---
	logger.Info("checkpoint 1: opening whatsmeow device store", "dir", storeDir)
	waStore.SetOSInfo("wa-mcp-backfill-test", [3]uint32{1, 0, 0})

	waDBURI := fmt.Sprintf("file:%s/whatsapp.db?_foreign_keys=on", storeDir)
	container, err := sqlstore.New(ctx, "sqlite3", waDBURI, waLog.Noop)
	if err != nil {
		logger.Error("FAIL: cannot open whatsmeow store", "err", err)
		os.Exit(1)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		logger.Error("FAIL: no device found — wa-mcp has never paired", "err", err)
		os.Exit(1)
	}
	logger.Info("checkpoint 1: PASS — device found",
		"device_id", deviceStore.ID,
		"platform", deviceStore.Platform,
	)

	// --- Debug checkpoint 2: Create client and connect ---
	logger.Info("checkpoint 2: creating whatsmeow client and connecting")
	client := whatsmeow.NewClient(deviceStore, waLog.Noop)
	client.EmitAppStateEventsOnFullSync = true

	var histSyncReceived int
	var mu sync.Mutex
	responseCh := make(chan struct{}, 10)

	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Connected:
			logger.Info("EVENT: Connected to WhatsApp")
		case *events.HistorySync:
			syncType := v.Data.GetSyncType()
			convCount := len(v.Data.GetConversations())
			msgCount := 0
			for _, c := range v.Data.GetConversations() {
				msgCount += len(c.GetMessages())
			}
			logger.Info("EVENT: HistorySync received",
				"sync_type", syncType.String(),
				"conversations", convCount,
				"total_messages", msgCount,
			)
			if syncType == waHistorySync.HistorySync_ON_DEMAND {
				mu.Lock()
				histSyncReceived++
				mu.Unlock()
				select {
				case responseCh <- struct{}{}:
				default:
				}
			}
		case *events.LoggedOut:
			logger.Error("EVENT: LoggedOut — session invalid")
		}
	})

	if err := client.Connect(); err != nil {
		logger.Error("FAIL: cannot connect", "err", err)
		os.Exit(1)
	}
	defer client.Disconnect()

	// Wait for connection
	time.Sleep(3 * time.Second)

	if !client.IsConnected() {
		logger.Error("FAIL: not connected after 3s")
		os.Exit(1)
	}
	if !client.IsLoggedIn() {
		logger.Error("FAIL: connected but not logged in")
		os.Exit(1)
	}
	logger.Info("checkpoint 2: PASS — connected and logged in",
		"own_jid", client.Store.ID.String(),
	)

	// --- Debug checkpoint 3: Open messages DB and find oldest message ---
	logger.Info("checkpoint 3: opening messages DB")
	msgDB, err := sql.Open("sqlite3", fmt.Sprintf("file:%s/messages.db?_foreign_keys=on", storeDir))
	if err != nil {
		logger.Error("FAIL: cannot open messages.db", "err", err)
		os.Exit(1)
	}
	defer msgDB.Close()

	var totalMsgs int
	var totalChats int
	msgDB.QueryRow("SELECT COUNT(*) FROM messages").Scan(&totalMsgs)
	msgDB.QueryRow("SELECT COUNT(*) FROM chats").Scan(&totalChats)
	logger.Info("checkpoint 3: PASS — messages DB open",
		"total_messages", totalMsgs,
		"total_chats", totalChats,
	)

	if debugMode {
		// List chats with message counts
		rows, err := msgDB.Query(`
			SELECT c.jid, c.name, COUNT(m.id) as msg_count,
				MIN(m.timestamp) as oldest, MAX(m.timestamp) as newest
			FROM chats c
			LEFT JOIN messages m ON c.jid = m.chat_jid
			GROUP BY c.jid
			ORDER BY msg_count DESC
			LIMIT 20
		`)
		if err == nil {
			defer rows.Close()
			fmt.Println("\n--- Top 20 chats by message count ---")
			fmt.Printf("%-45s %-30s %8s %-20s %-20s\n", "JID", "Name", "MsgCount", "Oldest", "Newest")
			for rows.Next() {
				var jid, name string
				var msgCnt int
				var oldest, newest sql.NullString
				rows.Scan(&jid, &name, &msgCnt, &oldest, &newest)
				o, n := "", ""
				if oldest.Valid {
					o = oldest.String[:19]
				}
				if newest.Valid {
					n = newest.String[:19]
				}
				if len(name) > 28 {
					name = name[:28] + ".."
				}
				fmt.Printf("%-45s %-30s %8d %-20s %-20s\n", jid, name, msgCnt, o, n)
			}
		}
		logger.Info("debug mode complete — exiting without backfill")
		os.Exit(0)
	}

	// --- Checkpoint 4: Find oldest message for the target chat ---
	chatJID, err := types.ParseJID(chatJIDStr)
	if err != nil {
		logger.Error("FAIL: invalid chat JID", "jid", chatJIDStr, "err", err)
		os.Exit(1)
	}

	var oldestMsgID string
	var oldestFromMe bool
	var oldestTS string
	err = msgDB.QueryRow(
		`SELECT id, is_from_me, timestamp FROM messages WHERE chat_jid = ? ORDER BY timestamp ASC LIMIT 1`,
		chatJID.String(),
	).Scan(&oldestMsgID, &oldestFromMe, &oldestTS)
	if err != nil {
		logger.Error("FAIL: no messages found for chat — need at least 1 message as anchor",
			"chat", chatJID.String(), "err", err)
		os.Exit(1)
	}

	var chatMsgCount int
	msgDB.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", chatJID.String()).Scan(&chatMsgCount)

	parsedTS, _ := time.Parse("2006-01-02 15:04:05Z07:00", oldestTS)
	if parsedTS.IsZero() {
		parsedTS, _ = time.Parse(time.RFC3339, oldestTS)
	}
	if parsedTS.IsZero() {
		parsedTS, _ = time.Parse("2006-01-02 15:04:05", oldestTS)
	}

	logger.Info("checkpoint 4: PASS — found anchor message",
		"chat", chatJID.String(),
		"messages_in_db", chatMsgCount,
		"oldest_msg_id", oldestMsgID,
		"oldest_from_me", oldestFromMe,
		"oldest_timestamp", parsedTS.Format(time.RFC3339),
	)

	// --- Checkpoint 5: Send on-demand history sync request ---
	for i := 0; i < maxRequests; i++ {
		// Re-query oldest in case previous request added older messages
		if i > 0 {
			err = msgDB.QueryRow(
				`SELECT id, is_from_me, timestamp FROM messages WHERE chat_jid = ? ORDER BY timestamp ASC LIMIT 1`,
				chatJID.String(),
			).Scan(&oldestMsgID, &oldestFromMe, &oldestTS)
			if err != nil {
				logger.Error("FAIL: cannot re-query oldest message", "err", err)
				break
			}
			parsedTS, _ = time.Parse("2006-01-02 15:04:05Z07:00", oldestTS)
			if parsedTS.IsZero() {
				parsedTS, _ = time.Parse(time.RFC3339, oldestTS)
			}
			if parsedTS.IsZero() {
				parsedTS, _ = time.Parse("2006-01-02 15:04:05", oldestTS)
			}
		}

		msgInfo := &types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chatJID,
				IsFromMe: oldestFromMe,
			},
			ID:        types.MessageID(oldestMsgID),
			Timestamp: parsedTS,
		}

		logger.Info("checkpoint 5: sending on-demand history sync request",
			"request_num", i+1,
			"count", count,
			"anchor_id", oldestMsgID,
			"anchor_ts", parsedTS.Format(time.RFC3339),
		)

		reqMsg := client.BuildHistorySyncRequest(msgInfo, count)
		resp, err := client.SendPeerMessage(ctx, reqMsg)
		if err != nil {
			logger.Error("FAIL: SendPeerMessage failed", "err", err)
			break
		}
		logger.Info("checkpoint 5: request sent successfully",
			"response_id", resp.ID,
			"response_ts", resp.Timestamp.Format(time.RFC3339),
		)

		// Wait for ON_DEMAND response
		logger.Info("waiting for ON_DEMAND history sync response (up to 60s)...")
		select {
		case <-responseCh:
			logger.Info("checkpoint 5: PASS — received ON_DEMAND history sync response!")
		case <-time.After(60 * time.Second):
			logger.Warn("TIMEOUT: no ON_DEMAND response after 60s — this is the known unreliability issue")
			logger.Info("Note: the request was sent and acked. The primary phone may not have responded.")
			break
		case <-ctx.Done():
			logger.Info("interrupted")
			break
		}

		// Check if new messages were added
		var newCount int
		msgDB.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", chatJID.String()).Scan(&newCount)
		added := newCount - chatMsgCount
		logger.Info("checkpoint 5: message count check",
			"before", chatMsgCount,
			"after", newCount,
			"added", added,
		)

		if added == 0 {
			logger.Info("no new messages added — stopping")
			break
		}
		chatMsgCount = newCount
	}

	// --- Final summary ---
	var finalCount int
	msgDB.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", chatJID.String()).Scan(&finalCount)
	totalAdded := finalCount - chatMsgCount

	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("BACKFILL TEST COMPLETE\n")
	fmt.Printf("Chat:          %s\n", chatJID.String())
	fmt.Printf("Messages (before): %d\n", chatMsgCount)
	fmt.Printf("Messages (after):  %d\n", finalCount)
	fmt.Printf("Total added:       %d\n", totalAdded)
	fmt.Printf("ON_DEMAND responses received: %d\n", histSyncReceived)
	fmt.Println(strings.Repeat("=", 60))
}
