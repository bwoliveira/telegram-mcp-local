// Package engine is the contract between the Telegram client (frente A,
// built on gotd) and the rest of the daemon: the MCP tools, the panel, the
// webhooks and the fakeengine used by every test.
//
// This file is the S0 contract of Etapa 1: types and interfaces only, no
// behaviour. The semantics live in docs/contratos/engine.md (and store.md,
// lifecycle.md, security.md, webhooks.md). Where this file and those
// documents disagree, the documents win and this file is fixed.
//
// Two rules shape every signature here:
//
//   - The engine never hands out gotd types. The fakeengine, the MCP layer
//     and the panel compile without importing gotd, and access hashes,
//     file references and session bytes never leave the engine and the store.
//   - Local reads (chat list, messages, search, context, stats, triage) are
//     answered by internal/store, not by the engine. The engine only does
//     what needs the network, and writes everything it learns into the store
//     through Persister before it emits an Event.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// AccountID is the local slot of an account: "primary", or the id the
// daemon's account list gives it. It is not the Telegram user id.
type AccountID string

// PeerType is the Telegram namespace of a peer id. User, basic-group and
// channel ids are separate namespaces: an id is meaningless without its type.
type PeerType string

const (
	PeerUser    PeerType = "user"    // private chat, bot, or Saved Messages (self)
	PeerChat    PeerType = "chat"    // basic group
	PeerChannel PeerType = "channel" // supergroup, gigagroup or broadcast channel
)

// PeerRef identifies a peer inside one account. Its text form, used as
// chat_id by the MCP tools, the /api routes, the webhooks and internal/state,
// is "<type>:<id>", for example "user:777000", "chat:4815162342" or
// "channel:1234567890". The id is the bare Telegram id (no Bot API -100
// prefix). Access hashes never appear in it: the engine reads them from the
// store.
type PeerRef struct {
	Type PeerType `json:"type"`
	ID   int64    `json:"id"`
}

// PeerKind is what a person would call the conversation. It is the `kind`
// field of list_chats.
type PeerKind string

const (
	KindSelf       PeerKind = "self" // Saved Messages
	KindUser       PeerKind = "user"
	KindBot        PeerKind = "bot"
	KindGroup      PeerKind = "group"      // basic group
	KindSupergroup PeerKind = "supergroup" // includes gigagroups and forums
	KindChannel    PeerKind = "channel"    // broadcast channel
)

// MessageKey is the only identity of a stored message: account + peer type +
// peer id + message id. A message id alone is never a key (store.md §2).
type MessageKey struct {
	Account AccountID `json:"account"`
	Peer    PeerRef   `json:"peer"`
	ID      int       `json:"id"`
}

// AccessState says whether the account can still reach a peer.
type AccessState string

const (
	AccessOK      AccessState = "ok"
	AccessLeft    AccessState = "left"    // the account left
	AccessKicked  AccessState = "kicked"  // removed by an admin
	AccessBanned  AccessState = "banned"  // banned from the channel
	AccessLost    AccessState = "lost"    // CHANNEL_PRIVATE / CHAT_FORBIDDEN, cause unknown
	AccessDeleted AccessState = "deleted" // the chat or the user account was deleted
)

// Peer is what the engine knows about a user, group or channel. It never
// carries the access hash.
type Peer struct {
	Ref          PeerRef     `json:"ref"`
	Kind         PeerKind    `json:"kind"`
	Title        string      `json:"title,omitempty"` // groups and channels
	FirstName    string      `json:"first_name,omitempty"`
	LastName     string      `json:"last_name,omitempty"`
	Username     string      `json:"username,omitempty"` // primary @username, without "@"
	Usernames    []string    `json:"usernames,omitempty"`
	Phone        string      `json:"phone,omitempty"` // only when Telegram shows it
	IsContact    bool        `json:"is_contact,omitempty"`
	Forum        bool        `json:"forum,omitempty"`
	Protected    bool        `json:"protected,omitempty"` // noforwards: see lifecycle.md §6
	Participants int         `json:"participants,omitempty"`
	Access       AccessState `json:"access"`
	MigratedTo   *PeerRef    `json:"migrated_to,omitempty"` // basic group upgraded to a supergroup
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

// EntityType is a Telegram message entity, lower-case and without the
// "messageEntity" prefix.
type EntityType string

const (
	EntityMention     EntityType = "mention"      // "@username" in the text
	EntityMentionName EntityType = "mention_name" // a name linked to a user id
	EntityHashtag     EntityType = "hashtag"
	EntityCashtag     EntityType = "cashtag"
	EntityBotCommand  EntityType = "bot_command"
	EntityURL         EntityType = "url"
	EntityTextURL     EntityType = "text_url"
	EntityEmail       EntityType = "email"
	EntityPhone       EntityType = "phone"
	EntityBold        EntityType = "bold"
	EntityItalic      EntityType = "italic"
	EntityUnderline   EntityType = "underline"
	EntityStrike      EntityType = "strike"
	EntitySpoiler     EntityType = "spoiler"
	EntityCode        EntityType = "code"
	EntityPre         EntityType = "pre"
	EntityBlockquote  EntityType = "blockquote"
	EntityCustomEmoji EntityType = "custom_emoji"
	EntityUnknown     EntityType = "unknown"
)

// Entity is one formatting or mention span. Offset and Length count UTF-16
// code units, as Telegram does, never bytes or runes.
type Entity struct {
	Type     EntityType `json:"type"`
	Offset   int        `json:"offset"`
	Length   int        `json:"length"`
	UserID   int64      `json:"user_id,omitempty"` // mention_name
	URL      string     `json:"url,omitempty"`     // text_url
	Language string     `json:"language,omitempty"`
}

// MediaType is the kind of attachment, the media_type of the tools.
type MediaType string

const (
	MediaPhoto     MediaType = "photo"
	MediaVideo     MediaType = "video"
	MediaAudio     MediaType = "audio"
	MediaVoice     MediaType = "voice"
	MediaVideoNote MediaType = "video_note"
	MediaDocument  MediaType = "document"
	MediaSticker   MediaType = "sticker"
	MediaAnimation MediaType = "animation"
	MediaContact   MediaType = "contact"
	MediaGeo       MediaType = "geo"
	MediaVenue     MediaType = "venue"
	MediaLiveGeo   MediaType = "live_location"
	MediaPoll      MediaType = "poll"
	MediaWebPage   MediaType = "webpage"
	MediaDice      MediaType = "dice"
	MediaStory     MediaType = "story"
	MediaOther     MediaType = "unsupported"
)

// Media describes an attachment. Remote is the engine's own encoding of the
// file location (id, access hash, file reference, DC): opaque to every other
// package, stored by the store, never logged, exported or sent to a webhook.
type Media struct {
	Type            MediaType `json:"type"`
	MimeType        string    `json:"mime_type,omitempty"`
	Filename        string    `json:"filename,omitempty"`
	Size            int64     `json:"bytes,omitempty"`
	DurationSeconds float64   `json:"duration_seconds,omitempty"`
	Width           int       `json:"width,omitempty"`
	Height          int       `json:"height,omitempty"`
	// TTLSeconds > 0 is self-destructing ("view once") media. It is never
	// downloaded, transcribed or exported (lifecycle.md §2).
	TTLSeconds int  `json:"ttl_seconds,omitempty"`
	Spoiler    bool `json:"spoiler,omitempty"`
	// Downloadable is false for TTL media, protected chats, expired media and
	// kinds without a file.
	Downloadable bool         `json:"downloadable"`
	Remote       []byte       `json:"-"`
	Contact      *ContactInfo `json:"contact,omitempty"`
	Dice         *DiceInfo    `json:"dice,omitempty"`
	Story        *StoryInfo   `json:"story,omitempty"`
	WebPage      *WebPageInfo `json:"webpage,omitempty"`
	// DocumentAttributes retains only Telegram's non-sensitive classification
	// flags, never raw TL attributes, file references, titles or access hashes.
	// MIME types and filenames are not evidence of these semantics.
	DocumentAttributes *DocumentAttributes `json:"document_attributes,omitempty"`
}

// DocumentAttributes records presence of Telegram document attribute variants.
// Voice and RoundMessage are meaningful only with Audio and Video respectively.
// The flags are omitted entirely for self-destructing media.
type DocumentAttributes struct {
	Audio        bool `json:"audio,omitempty"`
	Voice        bool `json:"voice,omitempty"`
	Video        bool `json:"video,omitempty"`
	RoundMessage bool `json:"round_message,omitempty"`
	Sticker      bool `json:"sticker,omitempty"`
	Animated     bool `json:"animated,omitempty"`
}

// Rich media metadata contains only fields supplied by Telegram, never file credentials.
type ContactInfo struct {
	Phone     string `json:"phone,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	UserID    int64  `json:"user_id,omitempty"`
	VCard     string `json:"vcard,omitempty"`
}
type DiceInfo struct {
	Emoji string `json:"emoji"`
	Value int    `json:"value"`
}
type StoryInfo struct {
	Peer       PeerRef `json:"peer"`
	ID         int     `json:"id"`
	ViaMention bool    `json:"via_mention,omitempty"`
}
type WebPageInfo struct {
	ID          int64  `json:"id,omitempty,string"`
	URL         string `json:"url,omitempty"`
	DisplayURL  string `json:"display_url,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	Author      string `json:"author,omitempty"`
	Pending     bool   `json:"pending,omitempty"`
}

// Location is a geo point, venue or live location.
type Location struct {
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	AccuracyMeters int     `json:"accuracy_meters,omitempty"`
	Title          string  `json:"name,omitempty"`
	Address        string  `json:"address,omitempty"`
	LivePeriod     int     `json:"live_period_seconds,omitempty"`
	Provider       string  `json:"provider,omitempty"`
	VenueID        string  `json:"venue_id,omitempty"`
	VenueType      string  `json:"venue_type,omitempty"`
}

// PollOption is one answer. Key is the base64 of Telegram's option bytes.
type PollOption struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Voters  int    `json:"voters"`
	Chosen  bool   `json:"chosen,omitempty"`
	Correct bool   `json:"correct,omitempty"`
}

// Poll is a poll and, when loaded, its results.
type Poll struct {
	ID          int64        `json:"id,string"`
	Question    string       `json:"question"`
	Options     []PollOption `json:"options"`
	Closed      bool         `json:"closed,omitempty"`
	Anonymous   bool         `json:"anonymous"`
	Multiple    bool         `json:"multiple_choice,omitempty"`
	Quiz        bool         `json:"quiz,omitempty"`
	TotalVoters int          `json:"total_voters"`
	// Partial says the results were not (all) loaded from Telegram.
	Partial bool `json:"partial,omitempty"`
}

// ReactionCount is one reaction and how many chose it. Emoji is the emoji
// itself, "custom:<document id>" for custom emoji, or "paid".
type ReactionCount struct {
	Emoji  string `json:"emoji"`
	Count  int    `json:"count"`
	Chosen bool   `json:"chosen,omitempty"` // by this account
}

// PeerReaction is who reacted with what, when Telegram tells it (private
// chats and small groups).
type PeerReaction struct {
	By    PeerRef   `json:"by"`
	Emoji string    `json:"emoji"`
	Date  time.Time `json:"date"`
	Big   bool      `json:"big,omitempty"`
	My    bool      `json:"my,omitempty"`
}

// ReplyRef is what a message answers.
type ReplyRef struct {
	ID      int      `json:"id"`
	Peer    *PeerRef `json:"peer,omitempty"`   // reply to another chat
	TopID   int      `json:"top_id,omitempty"` // thread or forum topic root
	Quote   string   `json:"quote,omitempty"`
	ToStory bool     `json:"to_story,omitempty"`
}

// ForwardInfo is where a forwarded message came from.
type ForwardInfo struct {
	From      *PeerRef  `json:"from,omitempty"`
	FromName  string    `json:"from_name,omitempty"` // hidden or anonymous author
	Date      time.Time `json:"date"`
	ChannelID int       `json:"channel_post,omitempty"`
}

// ServiceAction is a service message: a join, a title change, a pin, a call.
// Type is the action name without the "messageAction" prefix, lower snake.
type ServiceAction struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// Message is one Telegram message as the engine reads it.
type Message struct {
	Key      MessageKey `json:"key"`
	ChatName string     `json:"chat_name,omitempty"`
	Date     time.Time  `json:"date"`
	EditDate *time.Time `json:"edit_date,omitempty"` // visible edited marker
	// ServerEditDate preserves Telegram's revision even when edit_hide hides
	// EditDate. ContentHash distinguishes hidden payload edits from reactions.
	// ContentIdentity additionally hashes stable attachment identity and content
	// independently of protection-derived downloadability. It contains no file
	// credentials. These fields are internal ingest state, never read/export JSON.
	ServerEditDate  *time.Time     `json:"-"`
	ContentHash     string         `json:"-"`
	ContentIdentity string         `json:"-"`
	Out             bool           `json:"out"`              // sent by this account (from_me)
	Sender          *PeerRef       `json:"sender,omitempty"` // nil for anonymous channel posts
	SenderName      string         `json:"sender_name,omitempty"`
	PostAuthor      string         `json:"post_author,omitempty"`
	Text            string         `json:"text,omitempty"` // also the caption of media
	Entities        []Entity       `json:"entities,omitempty"`
	Media           *Media         `json:"media,omitempty"`
	Location        *Location      `json:"location,omitempty"`
	Poll            *Poll          `json:"poll,omitempty"`
	ReplyTo         *ReplyRef      `json:"reply_to,omitempty"`
	Forward         *ForwardInfo   `json:"forward,omitempty"`
	TopicID         int            `json:"topic_id,omitempty"`
	GroupedID       int64          `json:"grouped_id,omitempty,string"` // album
	Service         *ServiceAction `json:"service,omitempty"`
	// Mentioned is Telegram's own flag: the message mentions this account or
	// replies to one of its messages.
	Mentioned   bool            `json:"mentioned,omitempty"`
	MediaUnread bool            `json:"media_unread,omitempty"`
	Silent      bool            `json:"silent,omitempty"`
	Post        bool            `json:"post,omitempty"` // channel post
	Pinned      bool            `json:"pinned,omitempty"`
	Protected   bool            `json:"protected,omitempty"` // noforwards
	Reactions   []ReactionCount `json:"reactions,omitempty"`
	// ReactionSnapshot carries optional Telegram state during ingest only.
	// It is not part of message read/export JSON.
	ReactionSnapshot *ReactionUpdate `json:"-"`
	Views            int             `json:"views,omitempty"`
	Forwards         int             `json:"forwards,omitempty"`
	// TTLPeriod is the auto-delete timer in seconds; ExpiresAt is when the
	// message must be gone from every local copy (lifecycle.md §2).
	TTLPeriod int        `json:"ttl_period,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Dialog is one entry of the chat list with the server's counters.
type Dialog struct {
	Peer            PeerRef    `json:"peer"`
	FolderID        int        `json:"folder_id"` // 1 = Archived
	Pinned          bool       `json:"pinned,omitempty"`
	TopMessageID    int        `json:"top_message_id"`
	ReadInboxMaxID  int        `json:"read_inbox_max_id"`
	ReadOutboxMaxID int        `json:"read_outbox_max_id"`
	UnreadCount     int        `json:"unread_count"`
	UnreadMentions  int        `json:"unread_mentions_count"`
	UnreadReactions int        `json:"unread_reactions_count"`
	UnreadMark      bool       `json:"unread_mark,omitempty"`
	MuteUntil       *time.Time `json:"muted_until,omitempty"`
	TTLPeriod       int        `json:"ttl_period,omitempty"`
}

// Participant is one member of a group or channel.
type Participant struct {
	Peer     PeerRef    `json:"peer"`
	Name     string     `json:"name,omitempty"`
	Username string     `json:"username,omitempty"`
	Role     string     `json:"role"` // creator, admin, member, restricted, banned, left
	JoinedAt *time.Time `json:"joined_at,omitempty"`
	// Telegram permission flag bitsets, independent of the transport library.
	AdminRights  uint32 `json:"admin_rights,omitempty"`
	BannedRights uint32 `json:"banned_rights,omitempty"`
	BannedUntil  int    `json:"banned_until,omitempty"`
	CanEdit      bool   `json:"can_edit,omitempty"`
}

// Group is a snapshot of a group or channel, the get_group answer.
type Group struct {
	Peer         Peer          `json:"peer"`
	About        string        `json:"about,omitempty"`
	Owner        *PeerRef      `json:"owner,omitempty"`
	CreatedAt    *time.Time    `json:"created_at,omitempty"`
	LinkedChat   *PeerRef      `json:"linked_chat,omitempty"`
	Participants []Participant `json:"participants,omitempty"`
	Size         int           `json:"size"`
	// Partial says Telegram did not return every participant (limits,
	// hidden member lists, broadcast channels without admin rights).
	Partial    bool      `json:"partial,omitempty"`
	AmAdmin    bool      `json:"am_admin,omitempty"`
	SnapshotAt time.Time `json:"snapshot_at"`
}

// ---------------------------------------------------------------------------
// Account, status and login
// ---------------------------------------------------------------------------

// Mode is the write gate. ReadOnly is the default of every new install and
// is enforced by the engine itself, below the MCP layer (security.md §7).
type Mode string

const (
	ModeReadOnly  Mode = "read_only"
	ModeReadWrite Mode = "read_write"
)

// Account is the Telegram account a slot is logged into.
type Account struct {
	ID            AccountID `json:"account"`
	Authenticated bool      `json:"authenticated"`
	SelfID        int64     `json:"self_id,omitempty"`
	Phone         string    `json:"phone,omitempty"`
	Username      string    `json:"username,omitempty"`
	FirstName     string    `json:"first_name,omitempty"`
	LastName      string    `json:"last_name,omitempty"`
	Premium       bool      `json:"premium,omitempty"`
	DC            int       `json:"dc,omitempty"`
}

// ConnState is the state of the client, the `state` of Status.
type ConnState string

const (
	StateStarting      ConnState = "starting"
	StateNotAuthorized ConnState = "not_authorized" // waiting for a login
	StateConnecting    ConnState = "connecting"
	StateConnected     ConnState = "connected"
	StateReconnecting  ConnState = "reconnecting"
	StateResyncing     ConnState = "resyncing" // checkpoint held, replaying (store.md §5)
	StatePaused        ConnState = "paused"    // Disconnect: session kept, no network
	StateLoggedOut     ConnState = "logged_out"
	StateRevoked       ConnState = "revoked" // session ended outside the app
	StateError         ConnState = "error"
	StateStopped       ConnState = "stopped"
)

// UpdatesStatus is the updates checkpoint as the status shows it.
type UpdatesStatus struct {
	Pts          int        `json:"pts"`
	Qts          int        `json:"qts"`
	Seq          int        `json:"seq"`
	Date         time.Time  `json:"date"`
	Channels     int        `json:"channels"`
	LastUpdateAt *time.Time `json:"last_update_at,omitempty"`
	Held         bool       `json:"checkpoint_held,omitempty"`
	OpenGaps     int        `json:"open_gaps,omitempty"` // Recoverable update gaps; gates readiness.
	// InaccessibleGaps retains unfilled access-loss evidence, including legacy
	// rows. It warns about coverage but does not gate update readiness.
	InaccessibleGaps int `json:"inaccessible_gaps,omitempty"`
	// CoverageGaps counts ALL unfilled evidence, including recoverable gaps.
	// TerminalGaps cannot be repaired by update replay and must not advertise
	// ongoing synchronization. Neither counter establishes complete history.
	CoverageGaps int                      `json:"coverage_gaps,omitempty"`
	TerminalGaps int                      `json:"terminal_gaps,omitempty"`
	GapCauses    map[GapRecoveryCause]int `json:"gap_causes,omitempty"`
}

// HistoryProgress exposes stable JSON names consumed by the panel's live elements.
type HistoryProgress struct {
	Messages      int64     `json:"messages_synced"`
	Conversations int       `json:"conversations"`
	LastAt        time.Time `json:"last_at,omitempty"`
	Running       *PeerRef  `json:"running,omitempty"`
}

// LifecycleEvent is one line of the recent-events list in the status report.
// Data never holds secrets, codes, tokens or message text.
type LifecycleEvent struct {
	At    time.Time      `json:"at"`
	Event string         `json:"event"`
	Data  map[string]any `json:"data,omitempty"`
}

// Status is what the engine knows about its own state right now.
type Status struct {
	State          ConnState        `json:"state"`
	Since          time.Time        `json:"since"`
	LastError      string           `json:"last_error,omitempty"`
	Restarts       int              `json:"restarts"`
	PausedFor      string           `json:"paused_for,omitempty"`
	Mode           Mode             `json:"mode"`
	FloodWaitUntil *time.Time       `json:"flood_wait_until,omitempty"`
	Updates        UpdatesStatus    `json:"updates"`
	History        HistoryProgress  `json:"history"`
	Recent         []LifecycleEvent `json:"recent_events,omitempty"`
}

// Diagnostics is the engine report checked by health and telegram_status.
type Diagnostics struct {
	Library        string        `json:"library"` // "gotd/td v0.163.0", or "fakeengine"
	Layer          int           `json:"layer"`
	SessionPresent bool          `json:"session_present"`
	Authorized     bool          `json:"authorized"`
	Connected      bool          `json:"connected"`
	DC             int           `json:"dc,omitempty"`
	ServerTimeSkew time.Duration `json:"server_time_skew"`
	// RemoteUpdates is updates.getState; Local is the stored checkpoint. A
	// large distance means the engine is behind.
	RemoteUpdates *UpdatesStatus `json:"remote_updates,omitempty"`
	LocalUpdates  UpdatesStatus  `json:"local_updates"`
	// ThisSession is the current authorization from account.getAuthorizations.
	ThisSession *Authorization `json:"this_session,omitempty"`
	Sessions    int            `json:"sessions,omitempty"`
	CheckedAt   time.Time      `json:"checked_at"`
}

// Authorization is one session of the account, without its hash.
type Authorization struct {
	Device     string    `json:"device"`
	App        string    `json:"app"`
	Platform   string    `json:"platform,omitempty"`
	Country    string    `json:"country,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
	Current    bool      `json:"current,omitempty"`
}

// LoginMethod chooses how a login starts.
type LoginMethod string

const (
	LoginQR    LoginMethod = "qr"
	LoginPhone LoginMethod = "phone"
)

// LoginState is the current step in the Telegram login flow.
type LoginState string

const (
	LoginIdle      LoginState = "idle"
	LoginStarting  LoginState = "starting"
	LoginShowQR    LoginState = "qr"       // QRURL is ready to be drawn
	LoginWantCode  LoginState = "code"     // the person types the code Telegram sent
	LoginWantPass  LoginState = "password" // 2FA cloud password
	LoginSyncing   LoginState = "syncing"  // logged in, first dialogs and history
	LoginDone      LoginState = "done"
	LoginFailed    LoginState = "error"
	LoginCancelled LoginState = "cancelled"
)

// Login is the state of the login the panel is driving.
type Login struct {
	State  LoginState  `json:"state"`
	Method LoginMethod `json:"method,omitempty"`
	// QRURL is the tg://login?token=... URL. It is a credential while valid:
	// never logged, never serialised, only rendered by GET /api/pair/qr.png.
	QRURL       string     `json:"-"`
	QRVersion   int        `json:"qr_version"`
	QRExpiresAt *time.Time `json:"qr_expires_at,omitempty"`
	Phone       string     `json:"phone,omitempty"`
	// CodeType is how Telegram sent the code: app, sms, call, flash_call,
	// missed_call, email, fragment.
	CodeType      string     `json:"code_type,omitempty"`
	CodeLength    int        `json:"code_length,omitempty"`
	ResendAfter   *time.Time `json:"resend_after,omitempty"`
	PasswordHint  string     `json:"password_hint,omitempty"`
	Synced        int64      `json:"messages_synced"`
	Conversations int        `json:"conversations"`
	Error         string     `json:"error,omitempty"`
	StartedAt     time.Time  `json:"started_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at,omitempty"`
}

// LoginRequest starts a login.
type LoginRequest struct {
	Method LoginMethod `json:"method"`
	Phone  string      `json:"phone,omitempty"` // international format, phone method only
}

// Secret is a value that must never reach a log, an error message, a JSON
// answer or a webhook: login codes, 2FA passwords, api_hash, tokens. Its
// formatting methods are the only method bodies in this contract.
type Secret string

const redacted = "[redacted]"

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redacted)) }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (s Secret) Reveal() string             { return string(s) }
func (s Secret) Empty() bool                { return s == "" }

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// WriteOptions is embedded in every write request.
type WriteOptions struct {
	// DryRun makes the engine resolve and validate everything, return a
	// Preview and touch nothing on Telegram. The MCP layer sets it for every
	// call without confirm (security.md §8).
	DryRun bool `json:"dry_run"`
	// IdempotencyKey lets a retried call (after a timeout) not send twice:
	// the engine derives Telegram's random_id from it. Empty: one per call.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// ExpectFingerprint is the Preview.Fingerprint the person approved. The
	// engine recomputes it after resolving the request and refuses to run a
	// different action (security.md §8). The MCP layer enforces the 10-minute,
	// single-use window.
	ExpectFingerprint string `json:"preview_fingerprint,omitempty"`
}

// Preview is what a write would do. The same validation runs for a preview
// and for the real call, so a preview that succeeds predicts the call.
type Preview struct {
	// Parameters is the complete human-visible, resolved MCP action intent.
	// MCP binds approvals to this exact canonical JSON, independently of the
	// engine's state-sensitive fingerprint. It never contains staging paths.
	Parameters map[string]any `json:"parameters,omitempty"`

	Action   string       `json:"action"` // send_text, send_media, edit, delete, react, forward, mark_read, typing, organise, participants, update_group, invite_link, leave
	Target   Peer         `json:"target"`
	Text     string       `json:"text,omitempty"`
	Entities []Entity     `json:"entities,omitempty"` // resolved mentions, UTF-16
	Quoted   *Message     `json:"quoted,omitempty"`
	Affects  []MessageKey `json:"affects,omitempty"`
	Users    []Peer       `json:"users,omitempty"`
	Warnings []string     `json:"warnings,omitempty"`
	// Reversible is false for deletes, leaves, bans and link revokes.
	Reversible bool `json:"reversible"`
	// Fingerprint is the SHA-256 of the canonical request; security.md §8
	// binds the confirming call to it.
	Fingerprint string `json:"fingerprint"`
}

// WriteResult is the answer of every write.
type WriteResult struct {
	DryRun   bool         `json:"dry_run"`
	Preview  *Preview     `json:"preview,omitempty"`
	Done     bool         `json:"done"`
	Messages []MessageKey `json:"messages,omitempty"` // new messages (send, forward)
	Affected int          `json:"affected,omitempty"`
	// Note says what Telegram's acceptance means ("accepted by Telegram; not
	// a delivery or read confirmation").
	Note string `json:"note,omitempty"`
}

// Mention links part of the text to a user with a mention_name entity.
// Text is the substring to link; empty means "@username", or the user's
// first name appended to the text when there is no username.
type Mention struct {
	User PeerRef `json:"user"`
	Text string  `json:"text,omitempty"`
}

type SendTextRequest struct {
	Peer      PeerRef   `json:"peer"`
	Text      string    `json:"text"`
	ReplyTo   int       `json:"reply_to,omitempty"`
	TopicID   int       `json:"topic_id,omitempty"`
	Mentions  []Mention `json:"mentions,omitempty"`
	NoWebPage bool      `json:"no_webpage,omitempty"`
	Silent    bool      `json:"silent,omitempty"`
	WriteOptions
}

type SendMediaRequest struct {
	Peer PeerRef   `json:"peer"`
	Kind MediaType `json:"kind"` // photo, video, audio, voice, document, sticker, animation
	// Path is a local file the MCP layer already fetched and size-checked.
	Path     string    `json:"path"`
	Caption  string    `json:"caption,omitempty"`
	Mentions []Mention `json:"mentions,omitempty"`
	Filename string    `json:"filename,omitempty"`
	MimeType string    `json:"mime_type,omitempty"`
	ReplyTo  int       `json:"reply_to,omitempty"`
	TopicID  int       `json:"topic_id,omitempty"`
	Spoiler  bool      `json:"spoiler,omitempty"`
	WriteOptions
}

type SendLocationRequest struct {
	Peer     PeerRef  `json:"peer"`
	Location Location `json:"location"`
	ReplyTo  int      `json:"reply_to,omitempty"`
	WriteOptions
}

type SendPollRequest struct {
	Peer     PeerRef  `json:"peer"`
	Question string   `json:"question"`
	Options  []string `json:"options"` // 2 to 12
	// Multiple allows more than one answer; Telegram has no "at most N".
	Multiple  bool  `json:"multiple_choice,omitempty"`
	Anonymous *bool `json:"anonymous,omitempty"` // nil = Telegram's default (anonymous)
	WriteOptions
}

type EditRequest struct {
	Key      MessageKey `json:"key"`
	Text     string     `json:"text"`
	Mentions []Mention  `json:"mentions,omitempty"`
	WriteOptions
}

type DeleteRequest struct {
	Peer PeerRef `json:"peer"`
	IDs  []int   `json:"ids"`
	// Revoke deletes for everyone; false deletes only for this account.
	Revoke bool `json:"revoke"`
	// ConfirmRevoke is a separate, explicit approval for an irreversible
	// delete-for-everyone. It is confirmation metadata, not action identity:
	// Revoke already binds the scope into the preview fingerprint.
	ConfirmRevoke bool `json:"confirm_revoke,omitempty"`
	WriteOptions
}

// DeleteRevokeWarning tells preview clients which explicit approval is required.
const DeleteRevokeWarning = "deletes for everyone; repeat with confirm_revoke:true after explicit approval"

type ReactRequest struct {
	Key   MessageKey `json:"key"`
	Emoji string     `json:"emoji"` // "" removes this account's reaction
	Big   bool       `json:"big,omitempty"`
	WriteOptions
}

type ForwardRequest struct {
	From       PeerRef `json:"from"`
	IDs        []int   `json:"ids"`
	To         PeerRef `json:"to"`
	TopicID    int     `json:"topic_id,omitempty"`
	DropAuthor bool    `json:"drop_author,omitempty"`
	Silent     bool    `json:"silent,omitempty"`
	WriteOptions
}

type MarkReadRequest struct {
	Peer  PeerRef `json:"peer"`
	MaxID int     `json:"max_id,omitempty"` // 0 = up to the newest message
	WriteOptions
}

// TypingAction is a chat action of messages.setTyping.
type TypingAction string

const (
	TypingText   TypingAction = "typing"
	TypingAudio  TypingAction = "record_audio"
	TypingCancel TypingAction = "cancel"
)

type TypingRequest struct {
	Peer    PeerRef      `json:"peer"`
	Action  TypingAction `json:"action"`
	TopicID int          `json:"topic_id,omitempty"`
	WriteOptions
}

// OrganiseAction is an organise_chat action.
type OrganiseAction string

const (
	OrganiseArchive   OrganiseAction = "archive"
	OrganiseUnarchive OrganiseAction = "unarchive"
	OrganisePin       OrganiseAction = "pin"
	OrganiseUnpin     OrganiseAction = "unpin"
	OrganiseMute      OrganiseAction = "mute"
	OrganiseUnmute    OrganiseAction = "unmute"
)

type OrganiseRequest struct {
	Peer      PeerRef        `json:"peer"`
	Action    OrganiseAction `json:"action"`
	MuteUntil *time.Time     `json:"mute_until,omitempty"` // nil with mute = forever
	WriteOptions
}

// ParticipantsAction is a manage_group_participants action.
type ParticipantsAction string

const (
	ParticipantsAdd     ParticipantsAction = "add"
	ParticipantsRemove  ParticipantsAction = "remove"
	ParticipantsBan     ParticipantsAction = "ban"
	ParticipantsPromote ParticipantsAction = "promote"
	ParticipantsDemote  ParticipantsAction = "demote"
)

type ParticipantsRequest struct {
	Group  PeerRef            `json:"group"`
	Action ParticipantsAction `json:"action"`
	Users  []PeerRef          `json:"users"`
	WriteOptions
}

// ParticipantsResult says what happened to each user: Telegram refuses some
// and accepts others in one call.
type ParticipantsResult struct {
	WriteResult
	PerUser map[string]string `json:"per_user,omitempty"` // "user:<id>" -> "ok" or the Telegram error
}

type UpdateGroupRequest struct {
	Group PeerRef `json:"group"`
	Title *string `json:"title,omitempty"`
	About *string `json:"about,omitempty"`
	WriteOptions
}

// InviteLinkRequest without Revoke is a read: it returns the primary link
// from the full chat and DryRun is ignored. With Revoke it is a write.
type InviteLinkRequest struct {
	Group  PeerRef `json:"group"`
	Revoke bool    `json:"revoke,omitempty"`
	WriteOptions
}

type InviteLinkResult struct {
	WriteResult
	Link             string     `json:"link,omitempty"`
	Revoked          bool       `json:"revoked,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RequiresApproval bool       `json:"requires_approval,omitempty"`
}

type LeaveRequest struct {
	Group PeerRef `json:"group"`
	WriteOptions
}

// ---------------------------------------------------------------------------
// Network reads
// ---------------------------------------------------------------------------

// ResolveRequest turns what a person typed ("Ana", "@ana", "t.me/ana",
// "+5511...", "user:123") into peers.
type ResolveRequest struct {
	Query string     `json:"query"`
	Kinds []PeerKind `json:"kinds,omitempty"`
	// Network allows contacts.search / contacts.resolveUsername /
	// contacts.resolvePhone when the store has no exact match.
	Network bool `json:"network,omitempty"`
	Limit   int  `json:"limit,omitempty"`
}

// Candidate is one possible answer of Resolve.
type Candidate struct {
	Peer   Peer   `json:"peer"`
	Exact  bool   `json:"exact"`
	Source string `json:"source"` // store, contacts, username, phone, link
}

// ContactResolution is one answer of resolve_contact (check_numbers).
type ContactResolution struct {
	Query  string `json:"query"`
	Found  bool   `json:"found"`
	Peer   *Peer  `json:"peer,omitempty"`
	Reason string `json:"reason,omitempty"` // privacy, not_found, invalid
}

// RefreshRequest refreshes dialogs and their counters into the store.
// No Peers: messages.getDialogs up to Limit. With Peers: getPeerDialogs.
type RefreshRequest struct {
	Peers []PeerRef `json:"peers,omitempty"`
	Limit int       `json:"limit,omitempty"`
}

type RefreshResult struct {
	Dialogs int `json:"dialogs"`
	Peers   int `json:"peers"`
}

type GroupRequest struct {
	Group  PeerRef `json:"group"`
	Search string  `json:"search,omitempty"`
	Limit  int     `json:"limit,omitempty"`
}

type SearchRequest struct {
	Query string    `json:"query"`
	Peer  *PeerRef  `json:"peer,omitempty"` // nil = messages.searchGlobal
	Since time.Time `json:"since,omitempty"`
	Until time.Time `json:"until,omitempty"`
	Media MediaType `json:"media_type,omitempty"`
	Limit int       `json:"limit,omitempty"`
}

type SearchResult struct {
	Messages []Message `json:"messages"`
	// Partial says Telegram's search could not apply every filter, or stopped.
	Partial bool `json:"partial,omitempty"`
}

// WindowRequest fetches messages around one id with getHistory
// (offset_id + add_offset), for get_message_context when the store lacks them.
type WindowRequest struct {
	Key    MessageKey `json:"key"`
	Before int        `json:"before"`
	After  int        `json:"after"`
}

type PollRequest struct {
	Key        MessageKey `json:"key"`
	WithVoters bool       `json:"with_voters,omitempty"`
	Limit      int        `json:"limit,omitempty"`
}

type PollResults struct {
	Poll   Poll              `json:"poll"`
	Voters map[string][]Peer `json:"voters,omitempty"` // option key -> voters (non-anonymous polls)
}

type DownloadRequest struct {
	Key      MessageKey `json:"key"`
	Dir      string     `json:"dir"` // the media dir; the engine picks the file name
	MaxBytes int64      `json:"max_bytes,omitempty"`
	Thumb    bool       `json:"thumb,omitempty"`
}

type PhotoRequest struct {
	Peer PeerRef `json:"peer"`
	Full bool    `json:"full,omitempty"`
	Dir  string  `json:"dir"`
}

// Download is a file the engine wrote under the media dir (0600).
type Download struct {
	Path      string    `json:"path"`
	Bytes     int64     `json:"bytes"`
	MediaType MediaType `json:"media_type"`
	MimeType  string    `json:"mime_type,omitempty"`
	Filename  string    `json:"filename,omitempty"`
	SHA256    string    `json:"sha256"`
	Cached    bool      `json:"cached,omitempty"`
}

// NativeTranscript is messages.transcribeAudio, used only on explicit opt-in.
type NativeTranscript struct {
	Text    string `json:"text"`
	Pending bool   `json:"pending,omitempty"`
	ID      int64  `json:"transcription_id,string"`
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

type BackfillRequest struct {
	Peer PeerRef `json:"peer"`
	// Count is messages per getHistory page (1..100); Rounds caps the pages.
	Count  int        `json:"count,omitempty"`
	Rounds int        `json:"rounds,omitempty"`
	Until  *time.Time `json:"until,omitempty"` // stop at messages older than this
}

type BackfillResult struct {
	Peer       PeerRef    `json:"peer"`
	Fetched    int        `json:"fetched"`
	Added      int        `json:"messages_added"` // stable status field name
	Rounds     int        `json:"rounds"`
	Complete   bool       `json:"complete"` // reached the start of the accessible history
	OldestID   int        `json:"oldest_id,omitempty"`
	OldestDate *time.Time `json:"oldest_date,omitempty"`
	FloodUntil *time.Time `json:"flood_wait_until,omitempty"`
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// EventKind is what happened.
type EventKind string

const (
	EventMessage    EventKind = "message"
	EventEdit       EventKind = "edit"
	EventDelete     EventKind = "delete"
	EventReaction   EventKind = "reaction"
	EventRead       EventKind = "read"       // the other side read our messages
	EventConnection EventKind = "connection" // ConnState changed
	EventAuth       EventKind = "auth"       // logged in, logged out, revoked
	EventCoverage   EventKind = "coverage"   // a gap was found or filled
	EventAccess     EventKind = "access"     // access to a peer changed
)

// Source is where a change came from.
type Source string

const (
	SourceUpdate     Source = "update"     // live update
	SourceDifference Source = "difference" // getDifference / getChannelDifference
	SourceBackfill   Source = "backfill"   // getHistory
	SourceSearch     Source = "search"     // messages.search, context windows
	SourceSend       Source = "send"       // the answer of our own write
	SourceRefresh    Source = "refresh"    // getDialogs, getFullChat
)

// Deletion is messages that are gone. Peer is nil when Telegram did not say
// which chat (updateDeleteMessages outside channels): then IDs are
// account-wide and the store resolves them (store.md §4).
type Deletion struct {
	Peer *PeerRef `json:"peer,omitempty"`
	IDs  []int    `json:"ids"`
}

// ReactionUpdate is the full reaction state of a message.
type ReactionUpdate struct {
	Key    MessageKey      `json:"key"`
	Counts []ReactionCount `json:"counts"`
	Recent []PeerReaction  `json:"recent,omitempty"`
	Min    bool            `json:"min,omitempty"` // Telegram omitted account-specific chosen state.
}

// PollUpdate changes existing copies of a poll without replacing message content.
type PollUpdate struct {
	ID          int64        `json:"id,string"`
	Definition  *Poll        `json:"definition,omitempty"`
	Results     []PollOption `json:"results,omitempty"`
	TotalVoters *int         `json:"total_voters,omitempty"`
	Min         bool         `json:"min,omitempty"`
}

// ReactionEvent is a reaction change, with who added or removed what when
// the store could tell by comparing the recent lists.
type ReactionEvent struct {
	Version int64           `json:"-"` // Durable per-message committed state revision.
	Key     MessageKey      `json:"key"`
	Counts  []ReactionCount `json:"counts"`
	Added   []PeerReaction  `json:"added,omitempty"`
	Removed []PeerReaction  `json:"removed,omitempty"`
}

// ReadUpdate is a read marker. Outbox true: the other side read our messages
// up to MaxID (updateReadHistoryOutbox / updateReadChannelOutbox).
type ReadUpdate struct {
	Peer        PeerRef `json:"peer"`
	MaxID       int     `json:"max_id"`
	Outbox      bool    `json:"outbox"`
	StillUnread int     `json:"still_unread,omitempty"`
	// MessageIDs are our messages this marker newly covers (outbox only),
	// as far as the store knows them, capped at 100.
	MessageIDs []int `json:"message_ids,omitempty"`
}

// ConnectionChange is a ConnState transition.
type ConnectionChange struct {
	From   ConnState `json:"from"`
	To     ConnState `json:"to"`
	Reason string    `json:"reason,omitempty"`
}

// AuthState is the login state of the slot.
type AuthState string

const (
	AuthNone       AuthState = "none"
	AuthAuthorized AuthState = "authorized"
	AuthLoggedOut  AuthState = "logged_out"
	AuthRevoked    AuthState = "revoked"
)

type AuthChange struct {
	State  AuthState `json:"state"`
	Reason string    `json:"reason,omitempty"` // e.g. AUTH_KEY_UNREGISTERED
}

// GapReason is why part of the history may be missing.
type GapReason string

const (
	GapChannelTooLong    GapReason = "channel_too_long"    // updateChannelTooLong, OnChannelTooLong
	GapDifferenceTooLong GapReason = "difference_too_long" // unavailable update stream: server truncation or missing prior checkpoint
	GapStateLost         GapReason = "state_lost"          // OnLoadUserStateFailed / OnLoadChannelStateFailed
	GapAccessLost        GapReason = "access_lost"         // OnChannelInaccessible
)

// GapRecoveryCause explains why replay is pending or cannot repair coverage.
// It supplements (never replaces) the original GapReason.
type GapRecoveryCause string

const (
	GapRecoveryReplayPending     GapRecoveryCause = "replay_pending"
	GapRecoveryUnknownBaseline   GapRecoveryCause = "unknown_baseline"
	GapRecoveryAccessLost        GapRecoveryCause = "access_lost"
	GapRecoveryDifferenceTooLong GapRecoveryCause = "difference_too_long"
	GapRecoveryChannelTooLong    GapRecoveryCause = "channel_too_long"
	GapRecoveryStreamTruncated   GapRecoveryCause = "stream_truncated"
)

// GapRecovery is replay feasibility, not a statement that history is complete.
// Terminal evidence remains unfilled even after the live update stream is ready.
type GapRecovery string

const (
	GapRecoverable GapRecovery = "recoverable"
	GapTerminal    GapRecovery = "terminal"
)

// GapMark records a possible hole. Peer nil = the whole account.
type GapMark struct {
	Peer     *PeerRef   `json:"peer,omitempty"`
	Reason   GapReason  `json:"reason"`
	FromID   int        `json:"from_id,omitempty"`
	ToID     int        `json:"to_id,omitempty"`
	Detected time.Time  `json:"detected_at"`
	Filled   *time.Time `json:"filled_at,omitempty"`
	// Recovery classification is derived from durable evidence at read time.
	// Omitted means unknown (older providers), never implicitly recoverable.
	Recovery      GapRecovery      `json:"recovery,omitempty"`
	RecoveryCause GapRecoveryCause `json:"recovery_cause,omitempty"`

	// Recovery provenance is internal durable evidence, never inferred from a
	// later checkpoint or a detection timestamp. A nil anchor is terminal missing
	// evidence: adopting getState's head cannot reconstruct the lost interval.
	RecoveryID     int64         `json:"-"`
	RecoveryAnchor *UpdatesState `json:"-"`
	RecoveryCursor *UpdatesState `json:"-"`
}

// DifferenceCheckpoint joins an authoritative response's replay progress to its
// checkpoint in one transaction. From is the actual RPC baseline; Gaps is the
// pre-RPC snapshot. Final alone is not proof without a validated anchor chain.
type DifferenceCheckpoint struct {
	From, State UpdatesState
	Channel     int64
	Gaps        []GapMark
	Final       bool
}

type AccessChange struct {
	Peer   PeerRef     `json:"peer"`
	State  AccessState `json:"state"`
	Reason string      `json:"reason,omitempty"`
}

// Event is one thing that happened, after the store committed it. Exactly
// one of the payload pointers is set, the one Kind names.
type Event struct {
	Kind EventKind `json:"event"`
	// ID is stable for the same change across replays and restarts
	// (engine.md §6); webhooks derive delivery_id from it.
	ID         string    `json:"event_id"`
	Seq        uint64    `json:"seq"` // monotonic per process
	Account    AccountID `json:"account"`
	OccurredAt time.Time `json:"occurred_at"` // Telegram's date when it has one
	ReceivedAt time.Time `json:"received_at"`
	Source     Source    `json:"source"`
	// CatchUp marks changes recovered by getDifference after the engine was
	// offline, rather than received live.
	CatchUp bool     `json:"catch_up,omitempty"`
	Peer    *PeerRef `json:"peer,omitempty"`

	Message    *Message          `json:"message,omitempty"` // message, edit
	Delete     *Deletion         `json:"delete,omitempty"`
	Reaction   *ReactionEvent    `json:"reaction,omitempty"`
	Read       *ReadUpdate       `json:"read,omitempty"`
	Connection *ConnectionChange `json:"connection,omitempty"`
	Auth       *AuthChange       `json:"auth,omitempty"`
	Coverage   *GapMark          `json:"coverage,omitempty"`
	Access     *AccessChange     `json:"access,omitempty"`
}

// SubscribeOptions configures one subscriber.
type SubscribeOptions struct {
	Kinds []EventKind // empty = all
	// Buffer is the channel size. A full buffer drops the oldest event for
	// this subscriber only and counts it (engine.md §6).
	Buffer int
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	ErrNotAuthorized   = errors.New("engine: the account is not logged in")
	ErrReadOnly        = errors.New("engine: read-only mode: writes are disabled; enable them in the panel")
	ErrPaused          = errors.New("engine: disconnected by the user; reconnect in the panel")
	ErrRevoked         = errors.New("engine: the session was ended outside this app; log in again")
	ErrLoginRunning    = errors.New("engine: a login is already in progress")
	ErrNoLogin         = errors.New("engine: no login is waiting for a code or password")
	ErrPeerNotFound    = errors.New("engine: no such chat or person")
	ErrMessageNotFound = errors.New("engine: no such message")
	ErrNoAccess        = errors.New("engine: the account has no access to this chat")
	ErrPermission      = errors.New("engine: the account lacks the rights for this action")
	// ErrProtectedContent refuses download, transcription, export or
	// forward of TTL media, protected chats and expired messages.
	ErrProtectedContent = errors.New("engine: protected or self-destructing content")
	ErrNotSupported     = errors.New("engine: not supported by Telegram or this engine")
	ErrInvalid          = errors.New("engine: invalid request")
	ErrAccountMismatch  = errors.New("engine: a different Telegram account than this slot's")
	// ErrPreviewMismatch: the request no longer resolves to what was previewed.
	ErrPreviewMismatch = errors.New("engine: the action changed since its preview; preview it again")
	// ErrCheckpointHeld is returned by the updates-state adapter while a
	// failed Apply holds the checkpoint (store.md §5).
	ErrCheckpointHeld = errors.New("engine: updates checkpoint held after a failed apply")
)

// AmbiguousError is returned when a query matches several peers or messages
// and the call would act on one of them.
type AmbiguousError struct {
	Query      string
	Candidates []Candidate
}

func (e *AmbiguousError) Error() string { return "engine: ambiguous: " + e.Query }

// FloodWaitError is a FLOOD_WAIT the engine did not sleep through because it
// is longer than Config.FloodWaitMax or the context deadline.
type FloodWaitError struct {
	Method string
	Until  time.Time
}

func (e *FloodWaitError) Error() string { return "engine: FLOOD_WAIT on " + e.Method }

// RPCError is a Telegram error passed through with its type
// ("CHAT_ADMIN_REQUIRED"), so the tools can show Telegram's own reason. Kind
// is the sentinel it maps to (ErrPermission, ErrNoAccess, ...), or nil.
type RPCError struct {
	Code   int
	Type   string
	Method string
	Kind   error
}

func (e *RPCError) Error() string { return "telegram: " + e.Type + " on " + e.Method }
func (e *RPCError) Unwrap() error { return e.Kind }

// ---------------------------------------------------------------------------
// The interfaces
// ---------------------------------------------------------------------------

// Engine is what the MCP server sees. It cannot change the mode, log out,
// disconnect or drive a login: those are Controller's, which only the
// daemon and the panel hold (security.md §7).
type Engine interface {
	// Status, account and checks.
	Status() Status
	Mode() Mode
	Account(ctx context.Context) (Account, error)
	Diagnostics(ctx context.Context) (Diagnostics, error)

	// Network reads. Everything they learn is written to the store first.
	Resolve(ctx context.Context, req ResolveRequest) ([]Candidate, error)
	ResolveContacts(ctx context.Context, queries []string) ([]ContactResolution, error)
	RefreshDialogs(ctx context.Context, req RefreshRequest) (RefreshResult, error)
	RefreshGroup(ctx context.Context, req GroupRequest) (Group, error)
	SearchRemote(ctx context.Context, req SearchRequest) (SearchResult, error)
	FetchWindow(ctx context.Context, req WindowRequest) ([]Message, error)
	PollResults(ctx context.Context, req PollRequest) (PollResults, error)
	InviteLink(ctx context.Context, req InviteLinkRequest) (InviteLinkResult, error)
	Download(ctx context.Context, req DownloadRequest) (Download, error)
	ProfilePhoto(ctx context.Context, req PhotoRequest) (Download, error)
	TranscribeNative(ctx context.Context, key MessageKey) (NativeTranscript, error)

	// History.
	Backfill(ctx context.Context, req BackfillRequest) (BackfillResult, error)

	// Writes. With DryRun true each returns a Preview and makes no write
	// RPC, in either mode (in ModeReadOnly the preview carries a warning).
	// With DryRun false, ModeReadOnly returns ErrReadOnly before any RPC, and
	// a non-empty ExpectFingerprint that no longer matches returns
	// ErrPreviewMismatch.
	SendText(ctx context.Context, req SendTextRequest) (WriteResult, error)
	SendMedia(ctx context.Context, req SendMediaRequest) (WriteResult, error)
	SendLocation(ctx context.Context, req SendLocationRequest) (WriteResult, error)
	SendPoll(ctx context.Context, req SendPollRequest) (WriteResult, error)
	Edit(ctx context.Context, req EditRequest) (WriteResult, error)
	Delete(ctx context.Context, req DeleteRequest) (WriteResult, error)
	React(ctx context.Context, req ReactRequest) (WriteResult, error)
	Forward(ctx context.Context, req ForwardRequest) (WriteResult, error)
	MarkRead(ctx context.Context, req MarkReadRequest) (WriteResult, error)
	SetTyping(ctx context.Context, req TypingRequest) (WriteResult, error)
	Organise(ctx context.Context, req OrganiseRequest) (WriteResult, error)
	ManageParticipants(ctx context.Context, req ParticipantsRequest) (ParticipantsResult, error)
	UpdateGroup(ctx context.Context, req UpdateGroupRequest) (WriteResult, error)
	Leave(ctx context.Context, req LeaveRequest) (WriteResult, error)
}

// ConfirmedDeleter is retained for daemon compatibility. New callers carry
// explicit revocation approval in DeleteRequest.ConfirmRevoke.
type ConfirmedDeleter interface {
	DeleteConfirmed(context.Context, DeleteRequest, bool) (WriteResult, error)
}

// HistoryProgressReader reports local progress without causing a backfill.
type HistoryProgressReader interface {
	HistoryProgress(context.Context, PeerRef) (BackfillResult, error)
}

// ReadyWaiter is optionally implemented by controllers with asynchronous startup.
type ReadyWaiter interface {
	WaitReady(context.Context) error
}

// CursorReader exposes the durable account-scoped history checkpoint.
type CursorReader interface {
	Cursor(context.Context, AccountID, PeerRef) (Cursor, bool, error)
}

// Controller is the engine as the daemon and the panel hold it.
type Controller interface {
	Engine

	// Run keeps the client connected and synchronised until ctx ends. It waits
	// while not logged in and reconnects with backoff.
	Run(ctx context.Context) error
	// Changes signals, without blocking, every Status change (tray, panel).
	Changes() <-chan struct{}
	// Subscribe delivers events committed from now on, until ctx ends.
	Subscribe(ctx context.Context, opts SubscribeOptions) <-chan Event

	// Login (panel P04-P06, API A07-A09).
	StartLogin(ctx context.Context, req LoginRequest) error
	SubmitCode(ctx context.Context, code Secret) error
	SubmitPassword(ctx context.Context, password Secret) error
	ResendCode(ctx context.Context) error
	CancelLogin()
	Login() Login

	// The three separate actions of lifecycle.md §4.
	// Disconnect stops network and sync and keeps the session and history.
	Disconnect(ctx context.Context, reason string) error
	// Connect undoes Disconnect.
	Connect(ctx context.Context) error
	// LogOut calls auth.logOut and deletes the local session; history stays.
	LogOut(ctx context.Context) error

	// SetMode is reachable only from the panel (session + CSRF) and the CLI.
	SetMode(ctx context.Context, m Mode) error
}

// ---------------------------------------------------------------------------
// What the engine needs from others
// ---------------------------------------------------------------------------

// UpdatesState is the global updates checkpoint (gotd updates.State).
type UpdatesState struct {
	Pts  int `json:"pts"`
	Qts  int `json:"qts"`
	Date int `json:"date"`
	Seq  int `json:"seq"`
}

// UpdatesPatch is a partial checkpoint write (SetPts, SetQts, SetDate,
// SetSeq, SetDateSeq). Nil fields are left as they are.
type UpdatesPatch struct {
	Pts  *int `json:"pts,omitempty"`
	Qts  *int `json:"qts,omitempty"`
	Date *int `json:"date,omitempty"`
	Seq  *int `json:"seq,omitempty"`
}

// PeerUpsert is a peer with its access hash, as seen in one update or answer.
type PeerUpsert struct {
	Peer       Peer  `json:"peer"`
	AccessHash int64 `json:"-"`
	// HashMin says AccessHash came from a "min" constructor and must not
	// replace a full hash already stored (store.md §3).
	HashMin bool `json:"-"`
}

// ParticipantsSnapshot replaces the stored membership and metadata of a group.
type ParticipantsSnapshot struct {
	Group        PeerRef       `json:"group"`
	Participants []Participant `json:"participants"`
	Size         int           `json:"size"`
	Partial      bool          `json:"partial"`
	About        string        `json:"about,omitempty"`
	Owner        *PeerRef      `json:"owner,omitempty"`
	CreatedAt    *time.Time    `json:"created_at,omitempty"`
	LinkedChat   *PeerRef      `json:"linked_chat,omitempty"`
	AmAdmin      bool          `json:"am_admin"`
	At           time.Time     `json:"at"`
}

// Cursor is the backfill position of one peer (store.md §6).
type Cursor struct {
	Peer       PeerRef    `json:"peer"`
	NewestID   int        `json:"newest_id"`
	OldestID   int        `json:"oldest_id"`
	OldestDate *time.Time `json:"oldest_date,omitempty"`
	Complete   bool       `json:"complete"`
	State      string     `json:"state"` // idle, running, flood_wait, no_access, error
	FloodUntil *time.Time `json:"flood_wait_until,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	Fetched    int64      `json:"fetched"`
}

// DownloadRecord notes a file written under the media dir.
type DownloadRecord struct {
	Key    MessageKey `json:"key"`
	Path   string     `json:"path"`
	Bytes  int64      `json:"bytes"`
	SHA256 string     `json:"sha256"`
	At     time.Time  `json:"at"`
}

// Batch is one atomic write to the store. Everything in it commits in a
// single SQLite transaction, or nothing does.
type Batch struct {
	Account      AccountID              `json:"account"`
	Source       Source                 `json:"source"`
	CatchUp      bool                   `json:"catch_up,omitempty"`
	Received     time.Time              `json:"received"`
	Peers        []PeerUpsert           `json:"peers,omitempty"`
	Dialogs      []Dialog               `json:"dialogs,omitempty"`
	Messages     []Message              `json:"messages,omitempty"` // new or full copies
	Edits        []Message              `json:"edits,omitempty"`
	Deletes      []Deletion             `json:"deletes,omitempty"`
	Reactions    []ReactionUpdate       `json:"reactions,omitempty"`
	Polls        []PollUpdate           `json:"polls,omitempty"`
	Reads        []ReadUpdate           `json:"reads,omitempty"`
	Participants []ParticipantsSnapshot `json:"participants,omitempty"`
	Access       []AccessChange         `json:"access,omitempty"`
	Gaps         []GapMark              `json:"gaps,omitempty"`
	FilledGaps   []GapMark              `json:"filled_gaps,omitempty"`
	// Cursor is the backfill position after this page; it commits with the
	// page's messages (store.md invariant I3).
	Cursor    *Cursor          `json:"cursor,omitempty"`
	Downloads []DownloadRecord `json:"downloads,omitempty"`
}

// Applied is what a Batch actually changed. Replaying a batch that is
// already stored returns an empty Applied, and the engine emits no event for
// it (store.md invariant I2).
type Applied struct {
	Inserted          []MessageKey     `json:"inserted,omitempty"`
	Edited            []MessageKey     `json:"edited,omitempty"`
	Deleted           []MessageKey     `json:"deleted,omitempty"`
	UnresolvedDeletes []int            `json:"unresolved_deletes,omitempty"`
	Reactions         []ReactionEvent  `json:"reactions,omitempty"`
	Reads             []ReadUpdate     `json:"reads,omitempty"`
	Access            []AccessChange   `json:"access,omitempty"`
	Gaps              []GapMark        `json:"gaps,omitempty"`
	Skipped           int              `json:"skipped,omitempty"` // stale copies, tombstoned ids, expired
	Versions          map[string]int64 `json:"-"`                 // key text -> row version, for event ids
}

// Persister is what the engine needs from internal/store (frente B). The
// gotd updates.StateStorage and ChannelAccessHasher adapters are written on
// top of it inside the engine.
type Persister interface {
	Apply(ctx context.Context, b Batch) (Applied, error)

	LoadUpdatesState(ctx context.Context, acc AccountID) (UpdatesState, bool, error)
	SaveUpdatesState(ctx context.Context, acc AccountID, st UpdatesState) error
	// PatchUpdatesState fails when no state row exists, as gotd requires.
	PatchUpdatesState(ctx context.Context, acc AccountID, p UpdatesPatch) error
	LoadChannelPts(ctx context.Context, acc AccountID, channelID int64) (int, bool, error)
	SaveChannelPts(ctx context.Context, acc AccountID, channelID int64, pts int) error
	ForEachChannelPts(ctx context.Context, acc AccountID, fn func(channelID int64, pts int) error) error
	DropChannelState(ctx context.Context, acc AccountID, channelID int64) error
	// ResetUpdates forgets the checkpoint (new login of the slot).
	ResetUpdates(ctx context.Context, acc AccountID) error

	// AccessHash returns only full hashes.
	AccessHash(ctx context.Context, acc AccountID, p PeerRef) (int64, bool, error)
	LookupPeer(ctx context.Context, acc AccountID, p PeerRef) (Peer, bool, error)
	FindPeers(ctx context.Context, acc AccountID, query string, limit int) ([]Peer, error)

	Message(ctx context.Context, key MessageKey) (Message, bool, error)
	// ResolveMessageIDs maps account-wide ids (non-channel) to their keys.
	ResolveMessageIDs(ctx context.Context, acc AccountID, ids []int) ([]MessageKey, error)
	Cursor(ctx context.Context, acc AccountID, p PeerRef) (Cursor, bool, error)

	// SelfID is the Telegram user bound to the slot, 0 before the first
	// login. A login of another user fails with ErrAccountMismatch.
	SelfID(ctx context.Context, acc AccountID) (int64, error)
	BindSelf(ctx context.Context, acc AccountID, self Account) error
	SetAuthState(ctx context.Context, acc AccountID, st AuthState, reason string) error
}

// SessionStore keeps the MTProto session (auth key) in internal/secrets
// (frente S3). Its shape is gotd's session.Storage plus Delete.
type SessionStore interface {
	LoadSession(ctx context.Context) ([]byte, error) // ErrNoSession when absent
	StoreSession(ctx context.Context, data []byte) error
	DeleteSession(ctx context.Context) error
}

// ErrNoSession is what SessionStore.LoadSession returns when there is none.
var ErrNoSession = errors.New("engine: no stored session")

// DeviceInfo is what Telegram lists under Devices for this session.
type DeviceInfo struct {
	Model      string `json:"device_model"`
	System     string `json:"system_version"`
	AppVersion string `json:"app_version"`
	LangCode   string `json:"lang_code"`
}

// Config builds an engine.
type Config struct {
	Account AccountID
	APIID   int
	APIHash Secret
	Store   Persister
	Session SessionStore
	Mode    Mode // zero value means ModeReadOnly
	Device  DeviceInfo
	Logger  *slog.Logger
	Clock   func() time.Time
	// FloodWaitMax is the longest FLOOD_WAIT the engine sleeps through
	// inline; longer waits return *FloodWaitError.
	FloodWaitMax time.Duration
	// BackfillConcurrency caps parallel getHistory calls (default 1).
	BackfillConcurrency int
	// MaxChannelDifference caps parallel getChannelDifference calls
	// (gotd Config.MaxChannelDifferenceConcurrency).
	MaxChannelDifference int
}

// Factory builds an engine; internal/engine/gotd and the fakeengine each
// provide one.
type Factory func(cfg Config) (Controller, error)
