package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jonzarecki/wa-mcp/internal/config"
	"github.com/jonzarecki/wa-mcp/internal/domain"
	"github.com/jonzarecki/wa-mcp/internal/media"
	"github.com/jonzarecki/wa-mcp/internal/service"
	"github.com/jonzarecki/wa-mcp/internal/store"
	"github.com/jonzarecki/wa-mcp/internal/wa"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	if cfg.FFmpegPath != "" {
		media.SetFFmpegPath(cfg.FFmpegPath)
	}

	logger.Info("startup",
		"db_dir", cfg.DBDir,
		"log_level", cfg.LogLevelString(),
		"ffmpeg", cfg.FFmpegPath,
		"transport", cfg.MCP.Transport,
		"http_addr", cfg.MCP.HTTPAddr,
	)

	db, err := store.Open(cfg.DBDir)
	if err != nil {
		logger.Error("failed to open store", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	waclient, err := wa.New(db, cfg.DBDir, cfg.LogLevelString(), logger)
	if err != nil {
		logger.Error("failed to init wa client", "err", err)
		os.Exit(1)
	}

	chatService := service.NewChatService(db)
	messageService := service.NewMessageService(db, waclient)

	srv := server.NewMCPServer(
		"wa-mcp",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithPromptCapabilities(true),
		server.WithResourceCapabilities(true, false),
	)

	srv.AddTool(mcp.NewTool(
		"list_chats",
		mcp.WithDescription("List recent WhatsApp conversations with message previews, sorted by most recent activity. Search by contact/group name or phone number to find specific conversations. Supports groups-only filtering and pagination."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("query",
			mcp.Description("Search term to filter chats by name, phone number, or JID. Examples: 'Bob', '447123456789', '44123', 'work group'. Case-insensitive partial match."),
		),
		mcp.WithBoolean("groups_only",
			mcp.Description("Only return group chats (excludes direct/1-on-1 conversations)."),
			mcp.DefaultBool(false),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of chats to return (1-200)"),
			mcp.DefaultNumber(20),
			mcp.Min(1),
			mcp.Max(200),
		),
		mcp.WithNumber("page",
			mcp.Description("Page number for pagination, 0-based. Use with limit to browse through large chat lists."),
			mcp.DefaultNumber(0),
			mcp.Min(0),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opts := domain.ListChatsOptions{
			Query:      mcp.ParseString(req, "query", ""),
			OnlyGroups: mcp.ParseBoolean(req, "groups_only", false),
			Limit:      mcp.ParseInt(req, "limit", 20),
			Page:       mcp.ParseInt(req, "page", 0),
		}
		chats, err := chatService.ListChats(opts)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to list chats",
				"details": err.Error(),
				"hint":    "This may be a database error. Try again or check if the database is accessible.",
			}), nil
		}

		totalCount, _ := db.CountChats(opts.Query)

		return mcp.NewToolResultJSON(map[string]any{
			"success":  true,
			"chats":    chats,
			"total":    totalCount,
			"page":     opts.Page,
			"limit":    opts.Limit,
			"has_more": (opts.Page+1)*opts.Limit < totalCount,
		})
	})

	srv.AddTool(mcp.NewTool(
		"list_messages",
		mcp.WithDescription("List messages from a conversation. Filter by contact/group name and optionally by date range. Returns messages with content, sender, timestamp, and media type."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("recipient", mcp.Description("Contact/group name (e.g., 'Bob'), phone number (e.g., '447123456789'), or JID. Uses fuzzy matching against chat history.")),
		mcp.WithString("timeframe", mcp.Description("Natural time range (instead of after/before): 'last_hour', 'today', 'yesterday', 'last_3_days', 'this_week', 'last_week', 'this_month'. Cannot be combined with after/before.")),
		mcp.WithString("after", mcp.Description("ISO-8601 timestamp (e.g., '2025-01-15T00:00:00Z') - only messages after this time. Cannot be combined with timeframe.")),
		mcp.WithString("before", mcp.Description("ISO-8601 timestamp (e.g., '2025-01-20T23:59:59Z') - only messages before this time. Cannot be combined with timeframe.")),
		mcp.WithNumber("limit", mcp.Description("Maximum messages to return (1-200)"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
		mcp.WithNumber("page", mcp.Description("Page number for pagination, 0-based"), mcp.DefaultNumber(0), mcp.Min(0)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		recipient := mcp.ParseString(req, "recipient", "")

		var chatJID string
		if recipient != "" {
			resolvedJID, err := waclient.ResolveRecipient(recipient)
			if err != nil {
				return mcp.NewToolResultStructuredOnly(map[string]any{
					"success": false,
					"error":   "recipient resolution failed",
					"details": err.Error(),
					"hint":    "Check the recipient identifier. Use list_chats to see available contacts and groups.",
				}), nil
			}
			chatJID = resolvedJID
		}

		opts := domain.ListMessagesOptions{
			Timeframe: mcp.ParseString(req, "timeframe", ""),
			After:     mcp.ParseString(req, "after", ""),
			Before:    mcp.ParseString(req, "before", ""),
			ChatJID:   chatJID,
			Limit:     mcp.ParseInt(req, "limit", 20),
			Page:      mcp.ParseInt(req, "page", 0),
		}
		messages, err := messageService.ListMessages(opts)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to list messages",
				"details": err.Error(),
				"hint":    "Check your filter parameters. Ensure chat_jid is valid and timestamps are in ISO-8601 format. If using timeframe, ensure it's a valid preset (e.g., 'today', 'this_week').",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "messages": messages})
	})

	srv.AddTool(mcp.NewTool(
		"search_messages",
		mcp.WithDescription("Search message content across all conversations. Supports keywords, exact phrases (\"project meeting\"), boolean operators (OR/AND), exclusion (-word), and wildcards (vacat*). Returns matching messages with ±2 surrounding messages for context."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query string. Use simple keywords for best results. Examples: 'vacation', '\"project meeting\"', 'vacation OR holiday'.")),
		mcp.WithString("timeframe", mcp.Description("Natural time range (instead of after/before): 'last_hour', 'today', 'yesterday', 'last_3_days', 'this_week', 'last_week', 'this_month'. Cannot be combined with after/before.")),
		mcp.WithString("after", mcp.Description("ISO-8601 timestamp (e.g., '2025-01-15T00:00:00Z') - only messages after this time. Cannot be combined with timeframe.")),
		mcp.WithString("before", mcp.Description("ISO-8601 timestamp (e.g., '2025-01-20T23:59:59Z') - only messages before this time. Cannot be combined with timeframe.")),
		mcp.WithNumber("limit", mcp.Description("Maximum results to return (1-200)"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(200)),
		mcp.WithNumber("page", mcp.Description("Page number for pagination, 0-based"), mcp.DefaultNumber(0), mcp.Min(0)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opts := domain.SearchMessagesOptions{
			Query:     mcp.ParseString(req, "query", ""),
			Timeframe: mcp.ParseString(req, "timeframe", ""),
			After:     mcp.ParseString(req, "after", ""),
			Before:    mcp.ParseString(req, "before", ""),
			Limit:     mcp.ParseInt(req, "limit", 20),
			Page:      mcp.ParseInt(req, "page", 0),
		}
		messages, err := messageService.SearchMessages(opts)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "search failed",
				"details": err.Error(),
				"hint":    "Try simplifying your search query. Use simple keywords first, then try advanced FTS5 operators if needed. If using timeframe, ensure it's a valid preset (e.g., 'today', 'this_week').",
			}), nil
		}
		return mcp.NewToolResultJSON(map[string]any{"success": true, "messages": messages})
	})

	srv.AddTool(mcp.NewTool(
		"send_message",
		mcp.WithDescription("Send a text message, media file (image/video/audio/document), or both to a WhatsApp contact or group. Supports replying to messages for threaded conversations. Audio files are sent as voice messages."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithString("recipient", mcp.Required(), mcp.Description("Contact/group name (e.g., 'Bob', 'Project Team') or phone number without '+' (e.g., '447123456789').")),
		mcp.WithString("text", mcp.Description("Message text. If media_path provided, becomes caption for the media. If no media_path, sent as text message. Optional for media-only messages.")),
		mcp.WithString("media_path", mcp.Description("Absolute path to media file. Supports images (jpg/png), videos (mp4), audio (ogg/mp3/wav/m4a), documents (pdf/docx). Audio files are sent as voice messages.")),
		mcp.WithString("reply_to_message_id", mcp.Description("Optional message ID to reply to. Creates a quoted/threaded reply. Get message IDs from list_messages or search_messages.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		recipient := mcp.ParseString(req, "recipient", "")
		text := mcp.ParseString(req, "text", "")
		mediaPath := mcp.ParseString(req, "media_path", "")
		replyToMessageID := mcp.ParseString(req, "reply_to_message_id", "")

		if recipient == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "recipient parameter is required",
				"hint":    "Provide a contact name (e.g., 'John'), phone number (e.g., '441234567890'), or group JID (e.g., '123456@g.us'). Use list_chats to find available recipients.",
			}), nil
		}

		if text == "" && mediaPath == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "either 'text' or 'media_path' must be provided",
				"hint":    "Provide message text, a media file path, or both (media with caption).",
			}), nil
		}

		resolvedRecipient, err := waclient.ResolveRecipient(recipient)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "recipient resolution failed",
				"details": err.Error(),
				"hint":    "Check the recipient identifier. Use list_chats to see available contacts and groups.",
			}), nil
		}

		var result *domain.SendResult

		if mediaPath != "" {
			result, err = messageService.SendMedia(resolvedRecipient, mediaPath, text, replyToMessageID)
			if err != nil {
				return mcp.NewToolResultStructuredOnly(map[string]any{
					"success": false,
					"error":   "failed to send media",
					"details": err.Error(),
					"hint":    "Check that the file exists and is readable. For audio files, ensure ffmpeg is installed. Verify WhatsApp connection with get_connection_status.",
				}), nil
			}
		} else {
			result, err = messageService.SendText(resolvedRecipient, text, replyToMessageID)
			if err != nil {
				return mcp.NewToolResultStructuredOnly(map[string]any{
					"success": false,
					"error":   "failed to send message",
					"details": err.Error(),
					"hint":    "Check WhatsApp connection with get_connection_status. Ensure recipient format is correct and WhatsApp is connected.",
				}), nil
			}
		}

		return mcp.NewToolResultJSON(result)
	})

	srv.AddTool(mcp.NewTool(
		"download_media",
		mcp.WithDescription("Download media (image, video, audio, document) from a message to local storage. Returns the file path where the media was saved."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID that contains the media to download")),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat identifier from the message object (the chat_jid field).")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		messageID := mcp.ParseString(req, "message_id", "")
		chatJID := mcp.ParseString(req, "chat_jid", "")

		if messageID == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "message_id parameter is required",
				"hint":    "Provide the message ID from list_messages or search_messages that contains media.",
			}), nil
		}
		if chatJID == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "chat_jid parameter is required",
				"hint":    "Provide the chat JID where the message is located. Get this from the message or list_chats.",
			}), nil
		}

		result, err := messageService.DownloadMedia(messageID, chatJID)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to download media",
				"details": err.Error(),
				"hint":    "Ensure the message contains media (check media_type field). The media may have expired or been deleted from WhatsApp servers.",
			}), nil
		}
		return mcp.NewToolResultJSON(result)
	})

	srv.AddTool(mcp.NewTool(
		"get_connection_status",
		mcp.WithDescription("Check WhatsApp connection status and server health."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		status := map[string]any{
			"connected":      false,
			"logged_in":      false,
			"server_running": true,
		}

		if waclient.WA != nil {
			status["connected"] = waclient.WA.IsConnected()
			status["logged_in"] = waclient.WA.IsLoggedIn()

			if waclient.WA.Store != nil && waclient.WA.Store.ID != nil {
				status["device"] = map[string]any{
					"user":   waclient.WA.Store.ID.User,
					"device": waclient.WA.Store.ID.Device,
				}
			}
		}

		var chatCount, messageCount int
		_ = db.Messages.QueryRow("SELECT COUNT(*) FROM chats").Scan(&chatCount)
		_ = db.Messages.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messageCount)

		status["database"] = map[string]any{
			"chats":    chatCount,
			"messages": messageCount,
		}

		return mcp.NewToolResultJSON(map[string]any{"status": status})
	})

	srv.AddTool(mcp.NewTool(
		"catch_up",
		mcp.WithDescription("Get a summary of recent WhatsApp activity showing active conversations, total messages, questions directed at you, and media received."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("timeframe",
			mcp.Description("Time range to summarize: 'last_hour', 'today', 'yesterday', 'last_3_days', 'this_week', 'last_week', 'this_month'"),
			mcp.DefaultString("today"),
		),
		mcp.WithBoolean("groups_only",
			mcp.Description("Only return group chat activity (excludes direct/1-on-1 conversations)."),
			mcp.DefaultBool(false),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		opts := domain.CatchUpOptions{
			Timeframe:  mcp.ParseString(req, "timeframe", "today"),
			OnlyGroups: mcp.ParseBoolean(req, "groups_only", false),
		}

		summary, err := messageService.CatchUp(opts)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to generate catch up summary",
				"details": err.Error(),
				"hint":    "Ensure timeframe is valid (e.g., 'today', 'this_week', 'last_hour').",
			}), nil
		}

		return mcp.NewToolResultJSON(map[string]any{
			"success": true,
			"summary": summary,
		})
	})

	srv.AddTool(mcp.NewTool(
		"list_unread_chats",
		mcp.WithDescription("List chats with unread messages, ordered by unread count. Shows which conversations need your attention."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithBoolean("groups_only",
			mcp.Description("Only return group chats with unreads."),
			mcp.DefaultBool(false),
		),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		onlyGroups := mcp.ParseBoolean(req, "groups_only", false)
		chats, err := db.ListUnreadChats(onlyGroups)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to list unread chats",
				"details": err.Error(),
			}), nil
		}

		total := 0
		for _, c := range chats {
			total += c.UnreadCount
		}

		return mcp.NewToolResultJSON(map[string]any{
			"success":        true,
			"unread_chats":   chats,
			"total_unread":   total,
			"chats_with_unread": len(chats),
		})
	})

	srv.AddTool(mcp.NewTool(
		"mark_as_read",
		mcp.WithDescription("Mark messages in a chat as read. Use after reviewing a conversation or digest to clear unread state."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description("Chat JID to mark as read. Get from list_unread_chats or list_chats.")),
		mcp.WithString("before", mcp.Description("Optional ISO-8601 timestamp — only mark messages before this time. Defaults to now.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		chatJID := mcp.ParseString(req, "chat_jid", "")
		if chatJID == "" {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "chat_jid is required",
			}), nil
		}

		beforeStr := mcp.ParseString(req, "before", "")
		var before time.Time
		if beforeStr != "" {
			var err error
			before, err = time.Parse(time.RFC3339, beforeStr)
			if err != nil {
				return mcp.NewToolResultStructuredOnly(map[string]any{
					"success": false,
					"error":   "invalid 'before' timestamp",
					"details": err.Error(),
					"hint":    "Use ISO-8601 format: 2025-01-15T00:00:00Z",
				}), nil
			}
		} else {
			before = time.Now()
		}

		count, err := db.MarkAsRead(chatJID, before)
		if err != nil {
			return mcp.NewToolResultStructuredOnly(map[string]any{
				"success": false,
				"error":   "failed to mark as read",
				"details": err.Error(),
			}), nil
		}

		return mcp.NewToolResultJSON(map[string]any{
			"success":       true,
			"messages_read": count,
			"chat_jid":      chatJID,
		})
	})

	// --- MCP Prompts ---

	srv.AddPrompt(mcp.NewPrompt("digest_group",
		mcp.WithPromptDescription("Get a digest of recent activity in a WhatsApp group, then optionally mark as read"),
		mcp.WithArgument("group_name",
			mcp.ArgumentDescription("Name of the group to digest"),
			mcp.RequiredArgument(),
		),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		name := req.Params.Arguments["group_name"]
		return mcp.NewGetPromptResult(
			"Digest group: "+name,
			[]mcp.PromptMessage{mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(
				"Get a digest of the WhatsApp group \""+name+"\".\n\n"+
					"Steps:\n"+
					"1. Use list_chats with query=\""+name+"\" and groups_only=true to find the group JID\n"+
					"2. Use list_messages with the group JID and timeframe=\"today\" (or this_week for more context)\n"+
					"3. Summarize: key topics discussed, decisions made, questions asked, action items\n"+
					"4. Ask me if I want to mark these messages as read using mark_as_read"))},
		), nil
	})

	srv.AddPrompt(mcp.NewPrompt("catch_up_person",
		mcp.WithPromptDescription("Find everything a person said recently across all WhatsApp chats"),
		mcp.WithArgument("person_name",
			mcp.ArgumentDescription("Name of the person to catch up on"),
			mcp.RequiredArgument(),
		),
	), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		name := req.Params.Arguments["person_name"]
		return mcp.NewGetPromptResult(
			"Catch up on: "+name,
			[]mcp.PromptMessage{mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(
				"Find all recent messages from \""+name+"\" across all my WhatsApp chats.\n\n"+
					"Steps:\n"+
					"1. Use list_chats with query=\""+name+"\" to find their chat(s)\n"+
					"2. Use list_messages for each matching chat with timeframe=\"this_week\"\n"+
					"3. Also use search_messages with query=\""+name+"\" to catch mentions in group chats\n"+
					"4. Summarize what they've been saying, any questions for me, and any action items"))},
		), nil
	})

	// --- MCP Resources ---

	srv.AddResource(mcp.NewResource(
		"whatsapp://guides/search-syntax",
		"FTS5 Search Syntax Guide",
		mcp.WithResourceDescription("Reference for search_messages query syntax — FTS5 full-text search operators"),
		mcp.WithMIMEType("text/plain"),
	), func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "text/plain",
				Text: "FTS5 Search Syntax for search_messages tool:\n\n" +
					"Simple keywords:     vacation\n" +
					"Exact phrases:       \"project meeting\"\n" +
					"Boolean OR:          vacation OR holiday\n" +
					"Boolean AND:         budget AND meeting (default: AND)\n" +
					"Exclusion:           meeting -cancelled\n" +
					"Prefix wildcard:     vacat* (matches vacation, vacations, etc.)\n" +
					"Combine:             \"action items\" -done\n\n" +
					"Time filtering (combine with any query):\n" +
					"  timeframe: today, yesterday, this_week, last_week, last_3_days, last_hour, this_month\n" +
					"  after/before: ISO-8601 timestamps (e.g., 2025-01-15T00:00:00Z)\n\n" +
					"Results include ±2 surrounding messages for context.",
			},
		}, nil
	})

	srv.AddResource(mcp.NewResource(
		"whatsapp://guides/timeframes",
		"Valid Timeframe Presets",
		mcp.WithResourceDescription("List of valid timeframe values for list_messages, search_messages, and catch_up tools"),
		mcp.WithMIMEType("text/plain"),
	), func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "text/plain",
				Text: "Valid timeframe presets for list_messages, search_messages, and catch_up:\n\n" +
					"  last_hour    — messages from the last 60 minutes\n" +
					"  today        — messages since midnight today\n" +
					"  yesterday    — messages from yesterday (midnight to midnight)\n" +
					"  last_3_days  — messages from the last 3 days\n" +
					"  this_week    — messages since Monday of this week\n" +
					"  last_week    — messages from the previous week (Mon-Sun)\n" +
					"  this_month   — messages since the 1st of this month\n\n" +
					"Alternatively, use after/before with ISO-8601 timestamps for custom ranges.",
			},
		}, nil
	})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.WhatsApp.QRTimeout)
		defer cancel()
		if err := waclient.ConnectWithQR(ctx); err != nil {
			logger.Error("WA connect error", "err", err)
		}
	}()

	stopped := make(chan struct{})
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigc
		if waclient != nil && waclient.WA != nil && waclient.WA.IsConnected() {
			waclient.WA.Disconnect()
		}
		_ = db.Close()
		close(stopped)
	}()

	switch strings.ToLower(cfg.MCP.Transport) {
	case "http":
		httpOpts := []server.StreamableHTTPOption{
			server.WithEndpointPath("/mcp"),
			server.WithStateLess(true),
		}
		httpSrv := server.NewStreamableHTTPServer(srv, httpOpts...)

		handler := http.Handler(httpSrv)
		if cfg.MCP.APIKey != "" {
			apiKey := cfg.MCP.APIKey
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth := r.Header.Get("Authorization")
				if auth != "Bearer "+apiKey {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				httpSrv.ServeHTTP(w, r)
			})
		}

		httpServer := &http.Server{Addr: cfg.MCP.HTTPAddr, Handler: handler}
		go func() {
			logger.Info("MCP HTTP server starting", "addr", cfg.MCP.HTTPAddr)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("MCP HTTP error", "err", err)
			}
			sigc <- syscall.SIGINT
		}()

		<-stopped
		_ = httpServer.Close()

	default:
		go func() {
			if err := server.ServeStdio(srv); err != nil {
				logger.Error("MCP stdio error", "err", err)
			}
			sigc <- syscall.SIGINT
		}()

		<-stopped
	}

	logger.Info("shutdown complete")
}
