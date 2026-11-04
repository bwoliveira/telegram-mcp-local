package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/account"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Events are the five persisted changes a webhook may subscribe to.
var Events = []string{"message", "edit", "delete", "reaction", "read"}

// Identity intentionally has no phone number or credential fields (D11).
type Identity struct {
	ID       engine.AccountID `json:"id"`
	SelfID   int64            `json:"self_id,omitempty"`
	Username string           `json:"username,omitempty"`
}

type Chat struct {
	ID   string          `json:"id"`
	Type engine.PeerType `json:"type"`
	Kind engine.PeerKind `json:"kind,omitempty"`
	Name string          `json:"name,omitempty"`
}

// Event is the versioned delivery contract. ID/At retain their public names
// for the panel. Account and Receipt are source-compatibility placeholders only:
// neither can be serialized. Identity is the Telegram account on the wire.
type Event struct {
	Schema     int           `json:"schema"`
	Kind       string        `json:"event"`
	ID         string        `json:"delivery_id"`
	EventID    string        `json:"event_id"`
	OccurredAt time.Time     `json:"occurred_at"`
	At         time.Time     `json:"sent_at"`
	Source     engine.Source `json:"source"`
	CatchUp    bool          `json:"catch_up"`
	Test       bool          `json:"test"`
	Identity   *Identity     `json:"account"`
	Chat       *Chat         `json:"chat"`
	Message    *Message      `json:"message,omitempty"`
	Delete     *Deletion     `json:"delete,omitempty"`
	Reaction   *Reaction     `json:"reaction,omitempty"`
	Read       *Read         `json:"read,omitempty"`
	Account    *account.Info `json:"-"` // Deprecated: use Identity.
	Receipt    *Receipt      `json:"-"` // Deprecated: Telegram has no delivered receipts.
	chat       string
	fromMe     bool
	// Some reaction deltas had no identifiable author. Do not infer that
	// the entire delta is ours from only the authors safe to serialize.
	reactionPartial bool
}

// Message is a safe projection, not an engine.Message alias. In particular no
// Media.Remote, local path or secret can be added accidentally by serialization.
// Legacy Go fields remain for panel compilation but never enter the payload.
type Message struct {
	MessageID  int                   `json:"id"`
	ChatID     string                `json:"chat_id"`
	Timestamp  time.Time             `json:"date"`
	EditDate   *time.Time            `json:"edit_date"`
	FromMe     bool                  `json:"from_me"`
	SenderID   string                `json:"sender_id,omitempty"`
	SenderName string                `json:"sender_name,omitempty"`
	Text       string                `json:"text,omitempty"`
	Entities   []engine.Entity       `json:"entities,omitempty"`
	Media      *Media                `json:"media,omitempty"`
	ReplyTo    *Reference            `json:"reply_to,omitempty"`
	Forward    *Forward              `json:"forward,omitempty"`
	TopicID    int                   `json:"topic_id"`
	Location   any                   `json:"location,omitempty"`
	Poll       any                   `json:"poll,omitempty"`
	Service    *engine.ServiceAction `json:"service,omitempty"`
	Mentioned  bool                  `json:"mentioned"`
	Protected  bool                  `json:"protected"`
	TTLPeriod  int                   `json:"ttl_period"`
	ExpiresAt  *time.Time            `json:"expires_at"`
	ID         string                `json:"-"`
	ChatJID    string                `json:"-"`
	ChatName   string                `json:"-"`
	SenderJID  string                `json:"-"`
	Group      bool                  `json:"-"`
	Reaction   *Reaction             `json:"-"`
	Forwarded  bool                  `json:"-"`
	Edited     bool                  `json:"-"`
	Revoked    bool                  `json:"-"`
}

type Media struct {
	Type            string  `json:"type"`
	Ephemeral       bool    `json:"ephemeral,omitempty"`
	MimeType        string  `json:"mime_type,omitempty"`
	Filename        string  `json:"filename,omitempty"`
	Bytes           uint64  `json:"bytes,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	Caption         string  `json:"-"` // Deprecated: captions are Message.Text.
}

type Reference struct {
	MessageID int    `json:"id"`
	SenderID  string `json:"sender_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Peer      string `json:"peer,omitempty"`
	TopID     int    `json:"top_id,omitempty"`
	ID        string `json:"-"`
	SenderJID string `json:"-"`
}

type Forward struct {
	From        string    `json:"from,omitempty"`
	FromName    string    `json:"from_name,omitempty"`
	Date        time.Time `json:"date"`
	ChannelPost int       `json:"channel_post,omitempty"`
}

type Deletion struct {
	MessageIDs    []int  `json:"message_ids"`
	UnresolvedIDs []int  `json:"unresolved_ids"`
	Reason        string `json:"reason"`
}

type PeerReaction struct {
	By    string    `json:"by"`
	Emoji string    `json:"emoji"`
	Date  time.Time `json:"date"`
}

type Reaction struct {
	MessageID int                    `json:"message_id"`
	Counts    []engine.ReactionCount `json:"counts"`
	Added     []PeerReaction         `json:"added,omitempty"`
	Removed   []PeerReaction         `json:"removed,omitempty"`
	ToID      int                    `json:"to,omitempty"`
	Emoji     string                 `json:"emoji"`
	By        string                 `json:"by,omitempty"`
	To        string                 `json:"-"` // Deprecated panel source compatibility.
}

// MarshalJSON preserves emoji:"" for a known removal while omitting the
// compatibility shorthand entirely for anonymous count-only updates.
func (r Reaction) MarshalJSON() ([]byte, error) {
	type wire Reaction
	var emoji *string
	if r.By != "" && r.ToID != 0 {
		emoji = &r.Emoji
	}
	return json.Marshal(struct {
		wire
		Emoji *string `json:"emoji,omitempty"`
	}{wire(r), emoji})
}

// Receipt exists only to keep the old panel source compiling; never delivered.
type Receipt struct {
	ChatJID    string
	SenderJID  string
	MessageIDs []string
	Type       string
	Timestamp  time.Time
}

type Read struct {
	MaxID      int   `json:"max_id"`
	MessageIDs []int `json:"message_ids"`
}

func peerText(p engine.PeerRef) string { return fmt.Sprintf("%s:%d", p.Type, p.ID) }

func knownPeer(p engine.PeerRef) bool {
	return p.ID > 0 && (p.Type == engine.PeerUser || p.Type == engine.PeerChat || p.Type == engine.PeerChannel)
}

func deliveryID(hookID, eventID string) string {
	h := sha256.Sum256([]byte(hookID + "|" + eventID))
	return "d_" + hex.EncodeToString(h[:])[:32]
}

// BindEngine subscribes synchronously, so changes committed as soon as this
// returns are not missed. Cancellation/rebinding closes the old subscription.
// The goroutine holds only the event channel, never write capabilities.
func (m *Manager) BindEngine(ctx context.Context, eng engine.Controller) {
	m.mu.Lock()
	if m.unbind != nil {
		m.unbind()
	}
	ctx, cancel := context.WithCancel(ctx)
	m.unbind = cancel
	m.engineAccount = eng.Account
	m.resolveLocal = eng.Resolve
	m.mu.Unlock()
	events := eng.Subscribe(ctx, engine.SubscribeOptions{Kinds: []engine.EventKind{
		engine.EventMessage, engine.EventEdit, engine.EventDelete, engine.EventReaction, engine.EventRead,
	}, Buffer: maxQueue})
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				if ctx.Err() != nil {
					return
				}
				if ev, ok := project(event); ok {
					m.enrich(ctx, &ev)
					m.Dispatch(ctx, ev)
				}
			}
		}
	}()
}

func project(e engine.Event) (Event, bool) {
	ev := Event{Schema: 1, Kind: string(e.Kind), EventID: e.ID, OccurredAt: e.OccurredAt, Source: e.Source, CatchUp: e.CatchUp, Identity: &Identity{ID: e.Account}}
	if e.ID == "" || e.Account == "" {
		return ev, false
	}
	peer := e.Peer
	switch e.Kind {
	case engine.EventMessage, engine.EventEdit:
		if e.Message == nil {
			return ev, false
		}
		peer = &e.Message.Key.Peer
		ev.Message = projectMessage(*e.Message)
		ev.fromMe = e.Message.Out
	case engine.EventDelete:
		if e.Delete == nil || len(e.Delete.IDs) == 0 {
			return ev, false
		}
		peer = e.Delete.Peer
		ev.Delete = &Deletion{MessageIDs: []int{}, UnresolvedIDs: []int{}, Reason: "deleted"}
		if peer == nil {
			ev.Delete.UnresolvedIDs = append(ev.Delete.UnresolvedIDs, e.Delete.IDs...)
		} else {
			ev.Delete.MessageIDs = append(ev.Delete.MessageIDs, e.Delete.IDs...)
		}
		if e.Source == engine.Source("ttl") {
			ev.Delete.Reason = "ttl"
		}
		// The frozen engine contract does not encode revoke reasons. Do not
		// invent one: SourceSend identifies our own deletion for filtering, but
		// does not prove revoke-for-everyone versus delete-for-me.
		if e.Source == engine.SourceSend {
			ev.fromMe = true
		}
	case engine.EventReaction:
		if e.Reaction == nil {
			return ev, false
		}
		r := e.Reaction
		peer = &r.Key.Peer
		ev.Reaction = &Reaction{MessageID: r.Key.ID, Counts: append([]engine.ReactionCount{}, r.Counts...)}
		own := len(r.Added)+len(r.Removed) > 0
		for _, p := range r.Added {
			if !knownPeer(p.By) {
				ev.reactionPartial = true
				own = false
				continue
			}
			ev.Reaction.Added = append(ev.Reaction.Added, PeerReaction{By: peerText(p.By), Emoji: p.Emoji, Date: p.Date})
			own = own && p.My
		}
		for _, p := range r.Removed {
			if !knownPeer(p.By) {
				ev.reactionPartial = true
				own = false
				continue
			}
			ev.Reaction.Removed = append(ev.Reaction.Removed, PeerReaction{By: peerText(p.By), Emoji: p.Emoji, Date: p.Date})
			own = own && p.My
		}
		ev.fromMe = own || e.Source == engine.SourceSend
		if len(r.Added)+len(r.Removed) == 1 {
			if len(r.Added) == 1 && knownPeer(r.Added[0].By) {
				ev.Reaction.ToID = r.Key.ID
				ev.Reaction.Emoji = r.Added[0].Emoji
				ev.Reaction.By = peerText(r.Added[0].By)
			} else if len(r.Removed) == 1 && knownPeer(r.Removed[0].By) {
				ev.Reaction.ToID = r.Key.ID
				ev.Reaction.By = peerText(r.Removed[0].By)
			}
		}
	case engine.EventRead:
		if e.Read == nil || !e.Read.Outbox {
			return ev, false
		}
		peer = &e.Read.Peer
		ids := e.Read.MessageIDs
		if len(ids) > 100 {
			ids = ids[:100]
		}
		ev.Read = &Read{MaxID: e.Read.MaxID, MessageIDs: append([]int{}, ids...)}
	default:
		return ev, false
	}
	if peer != nil {
		ev.chat = peerText(*peer)
		ev.Chat = &Chat{ID: ev.chat, Type: peer.Type}
		// User and channel namespaces do not reveal kind (self/bot/user or
		// supergroup/channel). Local peer enrichment supplies it without guessing.
		if peer.Type == engine.PeerChat {
			ev.Chat.Kind = engine.KindGroup
		}
		if e.Message != nil {
			ev.Chat.Name = e.Message.ChatName
		}
	}
	return ev, true
}

func projectMessage(m engine.Message) *Message {
	out := &Message{MessageID: m.Key.ID, ID: strconv.Itoa(m.Key.ID), ChatID: peerText(m.Key.Peer), Timestamp: m.Date, EditDate: m.EditDate,
		FromMe: m.Out, SenderName: m.SenderName, Text: m.Text, Entities: append([]engine.Entity(nil), m.Entities...), TopicID: m.TopicID,
		Location: m.Location, Poll: m.Poll, Mentioned: m.Mentioned, Protected: m.Protected, TTLPeriod: m.TTLPeriod, ExpiresAt: m.ExpiresAt}
	if out.ExpiresAt == nil && m.TTLPeriod > 0 {
		expires := m.Date.Add(time.Duration(m.TTLPeriod) * time.Second)
		out.ExpiresAt = &expires
	}
	if m.Sender != nil {
		out.SenderID = peerText(*m.Sender)
	}
	if m.Media != nil {
		media := m.Media
		out.Media = &Media{Type: string(media.Type)}
		if media.TTLSeconds > 0 {
			out.Media.Ephemeral = true
		} else {
			out.Media.MimeType = media.MimeType
			out.Media.Filename = media.Filename
			if media.Size > 0 {
				out.Media.Bytes = uint64(media.Size)
			}
			out.Media.DurationSeconds = media.DurationSeconds
			out.Media.Width = media.Width
			out.Media.Height = media.Height
		}
	}
	if m.ReplyTo != nil {
		quote := []rune(m.ReplyTo.Quote)
		if len(quote) > 200 {
			quote = quote[:200]
		}
		out.ReplyTo = &Reference{MessageID: m.ReplyTo.ID, Text: string(quote), TopID: m.ReplyTo.TopID}
		if m.ReplyTo.Peer != nil {
			out.ReplyTo.Peer = peerText(*m.ReplyTo.Peer)
		}
	}
	if m.Forward != nil {
		out.Forward = &Forward{FromName: m.Forward.FromName, Date: m.Forward.Date, ChannelPost: m.Forward.ChannelID}
		if m.Forward.From != nil {
			out.Forward.From = peerText(*m.Forward.From)
		}
	}
	// Service.Data is an arbitrary map owned by the engine. A whitelist-free
	// pass-through could export credentials/file references; expose the type only.
	if m.Service != nil {
		out.Service = &engine.ServiceAction{Type: m.Service.Type}
	}
	return out
}

// enrich holds only local read capabilities; no network lookup is allowed.
func (m *Manager) enrich(ctx context.Context, ev *Event) {
	if ev.Chat == nil {
		return
	}
	m.mu.Lock()
	resolve := m.resolveLocal
	m.mu.Unlock()
	if resolve == nil {
		return
	}
	peers, err := resolve(ctx, engine.ResolveRequest{Query: ev.Chat.ID, Network: false, Limit: 1})
	if err != nil || len(peers) != 1 || peerText(peers[0].Peer.Ref) != ev.Chat.ID {
		return
	}
	p := peers[0].Peer
	ev.Chat.Kind = p.Kind
	if ev.Chat.Name == "" {
		ev.Chat.Name = p.Title
		if ev.Chat.Name == "" {
			ev.Chat.Name = p.FirstName
			if p.LastName != "" {
				ev.Chat.Name += " " + p.LastName
			}
		}
	}
}
