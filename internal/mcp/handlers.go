package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"io"
	"strings"
	"time"
)

// arguments is the shared decoding of every tool input.
type arguments struct {
	PreviewFingerprint string   `json:"preview_fingerprint"`
	IdempotencyKey     string   `json:"idempotency_key"`
	TopicID            int      `json:"topic_id"`
	MaxID              int      `json:"max_id"`
	Remote             bool     `json:"remote"`
	IncludeMigrated    *bool    `json:"include_migrated"`
	Revoke             bool     `json:"revoke"`
	Search             string   `json:"search"`
	Query              string   `json:"query"`
	ChatJID            string   `json:"chat_id"`
	GroupJID           string   `json:"group_id"`
	MessageID          string   `json:"message_id"`
	To                 string   `json:"to"`
	Text               string   `json:"text"`
	Type               string   `json:"type"`
	URL                string   `json:"url"`
	Caption            string   `json:"caption"`
	Filename           string   `json:"filename"`
	Since              string   `json:"since"`
	Until              string   `json:"until"`
	Order              string   `json:"order"`
	Limit              int      `json:"limit"`
	Offset             int      `json:"offset"`
	Count              int      `json:"count"`
	Rounds             int      `json:"rounds"`
	Chats              int      `json:"chats"`
	Emoji              *string  `json:"emoji"`
	Confirm            bool     `json:"confirm"`
	ConfirmRevoke      bool     `json:"confirm_revoke"`
	Live               *bool    `json:"live"`
	Queries            []string `json:"queries"`
	Full               bool     `json:"full"`
	Latitude           *float64 `json:"latitude"`
	Longitude          *float64 `json:"longitude"`
	Name               string   `json:"name"`
	Address            string   `json:"address"`
	Question           string   `json:"question"`
	Options            []string `json:"options"`
	Multiple           bool     `json:"multiple_choice"`
	Action             string   `json:"action"`
	Language           string   `json:"language"`
	Refresh            bool     `json:"refresh"`
	Link               bool     `json:"link"`
	Model              string   `json:"model"`

	MaxSilenceHours float64 `json:"max_silence_hours"`

	ReplyTo  string   `json:"reply_to"`
	Mentions []string `json:"mentions"`
	DryRun   bool     `json:"dry_run"`
	ForMe    bool     `json:"for_me"`
	Typing   *bool    `json:"typing"`
	Audio    bool     `json:"audio"`
	Receipts *bool    `json:"receipts"`

	Fields          []string `json:"fields"`
	MaxContentChars int      `json:"max_content_chars"`
	CountOnly       bool     `json:"count_only"`
	Before          *int     `json:"before"`
	After           *int     `json:"after"`
	GroupBy         string   `json:"group_by"`
	Direction       string   `json:"direction"`
	MediaType       string   `json:"media_type"`
	ExcludeGroups   bool     `json:"exclude_groups"`

	IncludeBots          bool     `json:"include_bots"`
	IncludeGroups        bool     `json:"include_groups"`
	IncludeGroupMentions bool     `json:"include_group_mentions"`
	IncludeMuted         *bool    `json:"include_muted"`
	IncludeArchived      bool     `json:"include_archived"`
	IncludeHandled       bool     `json:"include_handled"`
	IgnoreClosing        *bool    `json:"ignore_closing"`
	MinAgeHours          float64  `json:"min_age_hours"`
	OnlyUnanswered       bool     `json:"only_unanswered"`
	PerChat              int      `json:"per_chat"`
	Note                 string   `json:"note"`
	Clear                bool     `json:"clear"`
	Participants         []string `json:"participants"`
	Description          *string  `json:"description"`
	Reset                bool     `json:"reset"`

	OlderThan   int `json:"older_than_days"`
	MinMegabyte int `json:"min_megabytes"`
}

const (
	readTimeout = 60 * time.Second
	sendTimeout = 2 * time.Minute
	liveTimeout = 90 * time.Second
)

func (s *Server) call(ctx context.Context, params callParams) map[string]any {
	if result, handled := s.mcpMediaTool(ctx, params.Name, params.Arguments); handled {
		return result
	}
	var a arguments
	if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
		if err := decodeArguments(params.Name, params.Arguments, &a); err != nil {
			return toolError("could not read the tool arguments: %v", err)
		}
	}
	handler, ok := s.handlers()[params.Name]
	if !ok {
		return toolError("unknown tool %q", params.Name)
	}
	if confirmedTelegramWrite(params.Name, a) && s.eng != nil && s.eng.Mode() == engine.ModeReadOnly {
		message := engine.ErrReadOnly.Error()
		if a.PreviewFingerprint == "" {
			message += "; preview_fingerprint is also required before execution"
		}
		return toolError("%s", message)
	}
	if s.readStore != nil {
		if h := s.triageAnalysisHandlers()[params.Name]; h != nil {
			return h(ctx, a)
		}
		if h := s.telegramReadHandlers()[params.Name]; h != nil {
			return h(ctx, a)
		}
	}
	if localTools[params.Name] && s.index == nil {
		return toolError("not implemented yet: Telegram local store adapter is not available; inherited local index is not configured")
	}
	if stateTools[params.Name] && s.state == nil {
		return toolError("not implemented yet: local state is not configured")
	}
	result := handler(ctx, a)
	if localTools[params.Name] {
		return inheritedResult(result)
	}
	return result
}

func confirmedTelegramWrite(name string, a arguments) bool {
	if !telegramMutation(name) || !a.Confirm || a.DryRun {
		return false
	}
	return name != "get_group_invite_link" || a.Reset || a.Revoke
}

// handlers maps each tool to the method that answers it.
func (s *Server) handlers() map[string]func(context.Context, arguments) map[string]any {
	return map[string]func(context.Context, arguments) map[string]any{
		"health":              s.health,
		"telegram_status":     s.status,
		"list_chats":          s.listChats,
		"get_chat_messages":   s.chatMessages,
		"search_messages":     s.searchMessages,
		"list_contacts":       s.listContacts,
		"list_groups":         s.listGroups,
		"get_group":           s.getGroup,
		"send_text_message":   s.sendText,
		"send_media_message":  s.sendMedia,
		"download_media":      s.downloadMedia,
		"transcribe_audio":    s.transcribeAudio,
		"save_transcript":     s.saveTranscript,
		"sync_history":        s.syncHistory,
		"delete_message":      s.deleteMessage,
		"edit_message":        s.editMessage,
		"react_to_message":    s.react,
		"resolve_contact":     s.resolveContacts,
		"get_profile_picture": s.profilePicture,
		"send_location":       s.sendLocation,
		"send_poll":           s.sendPoll,
		"get_poll_results":    s.pollResults,
		"organise_chat":       s.organiseChat,

		"forward_message": s.forwardMessage,
		"mark_chat_read":  s.markChatRead,
		"send_typing":     s.sendTyping,

		"get_message_context": s.messageContext,
		"message_stats":       s.messageStats,
		"export_messages":     s.exportMessages,

		"list_unread":     s.listUnread,
		"list_unanswered": s.listUnanswered,
		"list_mentions":   s.listMentions,
		"mark_handled":    s.markHandled,
		"snooze_chat":     s.snoozeChat,

		"manage_group_participants": s.manageParticipants,
		"update_group":              s.updateGroup,
		"get_group_invite_link":     s.groupInviteLink,
		"leave_group":               s.leaveGroup,

		"media_stats": s.mediaStats,
		"purge_media": s.purgeMedia,
	}
}

func limit(n, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return min(n, 500)
}

func parseTime(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return value, nil
	}
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return value, nil
	}
	return "", fmt.Errorf("%s must be an RFC 3339 time such as 2026-09-01T00:00:00Z", name)
}

// ---- reading ----

type message struct {
	Protected  bool                   `json:"protected,omitempty"`
	Ephemeral  bool                   `json:"ephemeral,omitempty"`
	TTLPeriod  int                    `json:"ttl_period,omitempty"`
	ExpiresAt  *time.Time             `json:"expires_at,omitempty"`
	TopicID    int                    `json:"topic_id,omitempty"`
	Pinned     bool                   `json:"pinned,omitempty"`
	ID         string                 `json:"id"`
	ChatJID    string                 `json:"chat_id"`
	ChatName   string                 `json:"chat_name,omitempty"`
	Timestamp  time.Time              `json:"timestamp"`
	FromMe     bool                   `json:"from_me"`
	SenderJID  string                 `json:"sender_id,omitempty"`
	SenderName string                 `json:"sender_name,omitempty"`
	Text       string                 `json:"text,omitempty"`
	MediaType  string                 `json:"media_type,omitempty"`
	MimeType   string                 `json:"mime_type,omitempty"`
	Filename   string                 `json:"filename,omitempty"`
	QuotedID   string                 `json:"quoted_message_id,omitempty"`
	Poll       *engine.Poll           `json:"poll,omitempty"`
	Location   *engine.Location       `json:"location,omitempty"`
	Media      *engine.Media          `json:"media,omitempty"`
	Reactions  []engine.ReactionCount `json:"reactions,omitempty"`
	ReactionTo string                 `json:"reaction_to,omitempty"`
	Reaction   string                 `json:"reaction,omitempty"`
	Forwarded  bool                   `json:"forwarded,omitempty"`
	Edited     bool                   `json:"edited,omitempty"`
	Revoked    bool                   `json:"revoked,omitempty"`
	Starred    bool                   `json:"starred,omitempty"`
	Snippet    string                 `json:"snippet,omitempty"`
	Transcript *state.Transcript      `json:"transcript,omitempty"`
	// TextTruncated says max_content_chars cut the text or the transcript.
	TextTruncated bool `json:"text_truncated,omitempty"`
}

// name fills in who wrote a message and where, from the address book, when
// the inherited index stored an identifier or nothing.
func (s *Server) name(ctx context.Context, m *message) {
	if s.index == nil {
		return
	}
	if m.ChatName == "" || index.IsIdentifier(m.ChatName) {
		m.ChatName = s.index.Names.Name(ctx, m.ChatJID)
	}
	if m.FromMe {
		m.SenderName = ""
		return
	}
	if known := s.index.Names.Known(ctx, m.SenderJID); known != "" {
		m.SenderName = known
	} else if m.SenderName == "" || index.IsIdentifier(m.SenderName) {
		m.SenderName = s.index.Names.Name(ctx, m.SenderJID)
	}
}

func (s *Server) attachTranscripts(ctx context.Context, msgs []message) {
	if s.state == nil {
		return
	}
	for i := range msgs {
		if msgs[i].MediaType != "audio" {
			continue
		}
		if t, err := s.state.Transcript(ctx, msgs[i].ChatJID, msgs[i].ID); err == nil {
			msgs[i].Transcript = &t
		}
	}
}

var localTools = map[string]bool{"list_chats": true, "get_chat_messages": true, "search_messages": true, "list_contacts": true, "list_groups": true, "message_stats": true, "export_messages": true, "list_unread": true, "list_unanswered": true, "list_mentions": true, "mark_handled": true, "snooze_chat": true, "transcribe_audio": true, "save_transcript": true}
var stateTools = map[string]bool{"transcribe_audio": true, "save_transcript": true, "list_unanswered": true, "mark_handled": true, "snooze_chat": true}

const inheritedWarning = "Inherited local index: Telegram store migration and coverage are not implemented yet. These are local legacy data, not verified Telegram history."

func decodeArguments(tool string, raw []byte, a *arguments) error {
	allowed := map[string]bool{}
	for _, rawDef := range definitions() {
		def := rawDef.(map[string]any)
		if def["name"] != tool {
			continue
		}
		for name := range def["inputSchema"].(map[string]any)["properties"].(map[string]any) {
			allowed[name] = true
		}
		break
	}
	if allowed == nil || len(allowed) == 0 {
		known := false
		for _, rawDef := range definitions() {
			if rawDef.(map[string]any)["name"] == tool {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown tool %q", tool)
		}
	}
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(raw, &supplied); err != nil {
		return err
	}
	for name := range supplied {
		if !allowed[name] {
			return fmt.Errorf("tool %q does not accept argument %q", tool, name)
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(a); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("tool %q has multiple argument values", tool)
		}
		return err
	}
	return nil
}
func inheritedResult(r map[string]any) map[string]any {
	if r["isError"] == true {
		return r
	}
	contents, ok := r["content"].([]any)
	if !ok || len(contents) == 0 {
		return r
	}
	first, ok := contents[0].(map[string]any)
	if !ok {
		return r
	}
	text, _ := first["text"].(string)
	var body map[string]any
	if json.Unmarshal([]byte(text), &body) != nil {
		return r
	}
	body["source"] = "legacy_local_index"
	body["telegram_verified"] = false
	body["migration_warning"] = inheritedWarning
	return textResult(body, false)
}
