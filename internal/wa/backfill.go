package wa

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// BackfillOptions configures an on-demand history backfill request.
type BackfillOptions struct {
	ChatJID        string
	Count          int
	MaxRequests    int
	WaitPerRequest time.Duration
}

// BackfillResult reports what happened during backfill.
type BackfillResult struct {
	ChatJID       string `json:"chat_jid"`
	RequestsSent  int    `json:"requests_sent"`
	ResponsesSeen int    `json:"responses_seen"`
	MessagesAdded int    `json:"messages_added"`
}

type onDemandResponse struct {
	messages int
	endType  waHistorySync.Conversation_EndOfHistoryTransferType
}

// BackfillChat requests older messages for a chat from WhatsApp's servers.
// Best-effort: WhatsApp may not return the full history.
//
// WhatsApp only responds to on-demand history sync (PDO) requests on freshly-connected
// sessions. The method performs a disconnect+reconnect cycle before sending the request.
// This causes ~4s of downtime on the WebSocket; queued messages are delivered after reconnect.
func (c *Client) BackfillChat(ctx context.Context, opts BackfillOptions) (BackfillResult, error) {
	chatStr := strings.TrimSpace(opts.ChatJID)
	if chatStr == "" {
		return BackfillResult{}, fmt.Errorf("chat JID is required")
	}
	chat, err := types.ParseJID(chatStr)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("invalid chat JID: %w", err)
	}
	chatStr = chat.String()

	if opts.Count <= 0 {
		opts.Count = 50
	}
	if opts.MaxRequests <= 0 {
		opts.MaxRequests = 3
	}
	if opts.WaitPerRequest <= 0 {
		opts.WaitPerRequest = 60 * time.Second
	}

	if !c.WA.IsConnected() || !c.WA.IsLoggedIn() {
		return BackfillResult{}, fmt.Errorf("not connected to WhatsApp")
	}

	beforeCount, _ := c.Store.CountMessages(chatStr)

	var mu sync.Mutex
	var waitCh chan onDemandResponse

	handlerID := c.WA.AddEventHandler(func(evt interface{}) {
		hs, ok := evt.(*events.HistorySync)
		if !ok || hs == nil || hs.Data == nil {
			return
		}
		if hs.Data.GetSyncType() != waHistorySync.HistorySync_ON_DEMAND {
			return
		}
		for _, conv := range hs.Data.GetConversations() {
			if strings.TrimSpace(conv.GetID()) != chatStr {
				continue
			}
			mu.Lock()
			ch := waitCh
			mu.Unlock()
			if ch == nil {
				return
			}
			resp := onDemandResponse{
				messages: len(conv.GetMessages()),
				endType:  conv.GetEndOfHistoryTransferType(),
			}
			select {
			case ch <- resp:
			default:
			}
			return
		}
	})
	defer c.WA.RemoveEventHandler(handlerID)

	// WhatsApp servers only respond to PDO requests on fresh sessions.
	// Reconnect to ensure we get a response.
	c.Logger.Info("backfill: reconnecting for fresh session", "chat", chatStr)
	c.WA.Disconnect()
	time.Sleep(1 * time.Second)
	if err := c.WA.Connect(); err != nil {
		return BackfillResult{}, fmt.Errorf("reconnect failed: %w", err)
	}
	time.Sleep(3 * time.Second)

	if !c.WA.IsConnected() || !c.WA.IsLoggedIn() {
		return BackfillResult{}, fmt.Errorf("failed to reconnect")
	}

	result := BackfillResult{ChatJID: chatStr}

	for i := 0; i < opts.MaxRequests; i++ {
		oldest, err := c.Store.GetOldestMessageInfo(chatStr)
		if err != nil {
			return result, fmt.Errorf("no messages for %s in local DB (need at least 1 as anchor)", chatStr)
		}

		reqInfo := &types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				IsFromMe: oldest.FromMe,
			},
			ID:        types.MessageID(oldest.MsgID),
			Timestamp: oldest.Timestamp,
		}

		ch := make(chan onDemandResponse, 4)
		mu.Lock()
		waitCh = ch
		mu.Unlock()

		result.RequestsSent++
		c.Logger.Info("backfill: requesting older messages",
			"chat", chatStr, "request", i+1, "count", opts.Count,
			"anchor_id", oldest.MsgID, "anchor_ts", oldest.Timestamp.Format(time.RFC3339))

		reqMsg := c.WA.BuildHistorySyncRequest(reqInfo, opts.Count)
		if _, err := c.WA.SendPeerMessage(ctx, reqMsg); err != nil {
			return result, fmt.Errorf("send peer message failed: %w", err)
		}

		var resp onDemandResponse
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case resp = <-ch:
			result.ResponsesSeen++
		case <-time.After(opts.WaitPerRequest):
			c.Logger.Warn("backfill: timed out waiting for response", "chat", chatStr, "request", i+1)
			break
		}

		mu.Lock()
		if waitCh == ch {
			waitCh = nil
		}
		mu.Unlock()

		c.Logger.Info("backfill: on-demand response", "messages", resp.messages, "end_type", resp.endType.String())

		// handleHistorySync (registered in connection.go) persists messages via INSERT OR IGNORE
		time.Sleep(500 * time.Millisecond)

		newOldest, err := c.Store.GetOldestMessageInfo(chatStr)
		if err == nil && newOldest.MsgID == oldest.MsgID && resp.messages <= 0 {
			c.Logger.Info("backfill: no older messages added, stopping", "chat", chatStr)
			break
		}

		if resp.endType == waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY {
			c.Logger.Info("backfill: reached start of chat history", "chat", chatStr)
			break
		}
	}

	afterCount, _ := c.Store.CountMessages(chatStr)
	result.MessagesAdded = afterCount - beforeCount

	c.Logger.Info("backfill: complete",
		"chat", chatStr,
		"requests_sent", result.RequestsSent,
		"responses_seen", result.ResponsesSeen,
		"messages_added", result.MessagesAdded,
	)

	return result, nil
}
