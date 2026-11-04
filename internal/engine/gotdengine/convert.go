package gotdengine

import (
	"strconv"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"strings"
	"time"
)

func peerRef(p tg.PeerClass) engine.PeerRef {
	switch p := p.(type) {
	case *tg.PeerUser:
		return engine.PeerRef{Type: engine.PeerUser, ID: p.UserID}
	case *tg.PeerChat:
		return engine.PeerRef{Type: engine.PeerChat, ID: p.ChatID}
	case *tg.PeerChannel:
		return engine.PeerRef{Type: engine.PeerChannel, ID: p.ChannelID}
	}
	return engine.PeerRef{}
}
func convertMessage(acc engine.AccountID, raw tg.MessageClass) (engine.Message, bool) {
	m, ok := raw.(*tg.Message)
	if !ok || expiredMedia(m.Media) {
		return engine.Message{}, false
	}
	out := engine.Message{Key: engine.MessageKey{Account: acc, Peer: peerRef(m.PeerID), ID: m.ID}, Date: time.Unix(int64(m.Date), 0), Out: m.Out, Text: m.Message, GroupedID: m.GroupedID, Protected: m.Noforwards, TTLPeriod: m.TTLPeriod, Post: m.Post, Pinned: m.Pinned, Silent: m.Silent, Mentioned: m.Mentioned, MediaUnread: m.MediaUnread, Views: m.Views, Forwards: m.Forwards, PostAuthor: m.PostAuthor}
	if m.FromID != nil {
		p := peerRef(m.FromID)
		out.Sender = &p
	}
	// edit_hide controls presentation, not the server revision/order. Never
	// manufacture either timestamp from the update envelope or receipt.
	if m.EditDate > 0 {
		d := time.Unix(int64(m.EditDate), 0)
		out.ServerEditDate = &d
		if !m.EditHide {
			out.EditDate = &d
		}
	}
	if m.TTLPeriod > 0 {
		d := out.Date.Add(time.Duration(m.TTLPeriod) * time.Second)
		out.ExpiresAt = &d
	}
	for _, e := range m.Entities {
		x := engine.Entity{Type: engine.EntityUnknown}
		if v, ok := e.(interface {
			GetOffset() int
			GetLength() int
		}); ok {
			x.Offset = v.GetOffset()
			x.Length = v.GetLength()
		}
		switch e := e.(type) {
		case *tg.MessageEntityBold:
			x.Type = engine.EntityBold
		case *tg.MessageEntityItalic:
			x.Type = engine.EntityItalic
		case *tg.MessageEntityUnderline:
			x.Type = engine.EntityUnderline
		case *tg.MessageEntityStrike:
			x.Type = engine.EntityStrike
		case *tg.MessageEntitySpoiler:
			x.Type = engine.EntitySpoiler
		case *tg.MessageEntityCode:
			x.Type = engine.EntityCode
		case *tg.MessageEntityPre:
			x.Type = engine.EntityPre
			x.Language = e.Language
		case *tg.MessageEntityMention:
			x.Type = engine.EntityMention
		case *tg.MessageEntityMentionName:
			x.Type = engine.EntityMentionName
			x.UserID = e.UserID
		case *tg.MessageEntityTextURL:
			x.Type = engine.EntityTextURL
			x.URL = e.URL
		case *tg.MessageEntityURL:
			x.Type = engine.EntityURL
		case *tg.MessageEntityHashtag:
			x.Type = engine.EntityHashtag
		case *tg.MessageEntityCashtag:
			x.Type = engine.EntityCashtag
		case *tg.MessageEntityBotCommand:
			x.Type = engine.EntityBotCommand
		case *tg.MessageEntityEmail:
			x.Type = engine.EntityEmail
		case *tg.MessageEntityPhone:
			x.Type = engine.EntityPhone
		case *tg.MessageEntityBlockquote:
			x.Type = engine.EntityBlockquote
		case *tg.MessageEntityCustomEmoji:
			x.Type = engine.EntityCustomEmoji
		}
		out.Entities = append(out.Entities, x)
	}
	if m.Media != nil {
		out.Media = &engine.Media{Type: engine.MediaOther, Downloadable: false}
		switch v := m.Media.(type) {
		case *tg.MessageMediaPhoto:
			out.Media.Type = engine.MediaPhoto
			out.Media.TTLSeconds = v.TTLSeconds
		case *tg.MessageMediaDocument:
			out.Media.Type = engine.MediaDocument
			out.Media.TTLSeconds = v.TTLSeconds
			if d, ok := v.Document.(*tg.Document); ok {
				animated, sticker := false, false
				attributes := engine.DocumentAttributes{}
				for _, raw := range d.Attributes {
					switch a := raw.(type) {
					case *tg.DocumentAttributeFilename:
						if v.TTLSeconds == 0 {
							out.Media.Filename = a.FileName
						}
					case *tg.DocumentAttributeSticker:
						sticker = true
						attributes.Sticker = true
					case *tg.DocumentAttributeAnimated:
						animated = true
						attributes.Animated = true
					case *tg.DocumentAttributeAudio:
						attributes.Audio, attributes.Voice = true, a.Voice
						out.Media.Type = engine.MediaAudio
						if a.Voice {
							out.Media.Type = engine.MediaVoice
						}
					case *tg.DocumentAttributeVideo:
						attributes.Video, attributes.RoundMessage = true, a.RoundMessage
						out.Media.Type = engine.MediaVideo
						if a.RoundMessage {
							out.Media.Type = engine.MediaVideoNote
						}
					}
				}
				// Stickers and animations also carry video attributes; their
				// more specific kind must win regardless of attribute order.
				if sticker {
					out.Media.Type = engine.MediaSticker
				} else if animated {
					out.Media.Type = engine.MediaAnimation
				}
				if v.TTLSeconds == 0 && attributes != (engine.DocumentAttributes{}) {
					out.Media.DocumentAttributes = &attributes
				}
			}
		case *tg.MessageMediaWebPage:
			out.Media.Type = engine.MediaWebPage
		}
		convertRichMedia(&out, m.Media)
		convertDownloadableMedia(&out, m)
	}
	if _, ok := m.GetReactions(); ok || len(m.Reactions.Results) > 0 {
		r := convertReactions(out.Key, m.Reactions)
		// Message copies are not authoritative reaction refreshes. Only
		// updateMessageReactions (also returned by getMessagesReactions)
		// can replace an already known snapshot.
		r.Min = true
		out.Reactions = r.Counts
		out.ReactionSnapshot = &r
	}
	if r, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
		out.ReplyTo = &engine.ReplyRef{ID: r.ReplyToMsgID, TopID: r.ReplyToTopID, Quote: r.QuoteText}
		if r.ForumTopic {
			// A reply directly to a topic root omits reply_to_top_id.
			// Ordinary discussion threads must not become forum topics.
			out.TopicID = r.ReplyToTopID
			if out.TopicID == 0 {
				out.TopicID = r.ReplyToMsgID
			}
			if out.TopicID == 0 {
				out.TopicID = 1 // Telegram's General topic.
			}
		}
		if r.ReplyToPeerID != nil {
			p := peerRef(r.ReplyToPeerID)
			out.ReplyTo.Peer = &p
		}
	}
	// Only a content-free type/timer placeholder survives view-once media.
	// Do not retain captions or entity URLs as a parallel copy of the payload.
	if out.Media != nil && out.Media.TTLSeconds > 0 {
		out.Text = "[ephemeral media]"
		out.Entities = nil
		if out.ReplyTo != nil {
			out.ReplyTo.Quote = ""
		}
	}
	out.ContentHash = messageContentHash(out, m.Media)
	// Keep the historical content hash unchanged, but retain independent
	// comparison evidence before protection changes downloadability. Temporary
	// media has no durable attachment/content identity beyond its placeholder.
	if out.Media == nil || out.Media.TTLSeconds == 0 {
		identityMessage := out
		identityMessage.Protected = false
		if out.Media != nil {
			media := *out.Media
			identityMessage.Media = &media
			// Protection also suppresses size/MIME/filename enrichment. Hash
			// the same complete public projection on either side of that policy
			// change without changing the historical adapter hash or read DTO.
			convertDownloadableMedia(&identityMessage, m)
			identityMessage.Media.Downloadable = false
			identityMessage.Media.Remote = nil
		}
		out.ContentIdentity = messageContentHash(identityMessage, m.Media)
	}
	return out, out.Key.Peer.ID > 0 && out.Key.ID > 0
}

// Hash only content and stable attachment identity, never counters, reactions,
// edit presentation, file references or access hashes. Equal-looking replacement
// files/photos remain real edits; a refreshed download credential does not.
func messageContentHash(m engine.Message, raw tg.MessageMediaClass) string {
	identity := ""
	if m.Media == nil || m.Media.TTLSeconds == 0 {
		switch v := raw.(type) {
		case *tg.MessageMediaDocument:
			if d, ok := v.Document.(*tg.Document); ok {
				identity = "document:" + strconv.FormatInt(d.ID, 10)
			}
		case *tg.MessageMediaPhoto:
			if p, ok := v.Photo.(*tg.Photo); ok {
				identity = "photo:" + strconv.FormatInt(p.ID, 10)
			}
		}
	}
	return engine.MessageContentHash(m, identity)
}

// Telegram represents expired photo/document media by removing the payload.
// Treat that as deletion, not a fresh attachment whose caption survives forever.
func expiredMedia(raw tg.MessageMediaClass) bool {
	switch m := raw.(type) {
	case *tg.MessageMediaPhoto:
		_, photo := m.Photo.(*tg.Photo)
		_, video := m.Video.(*tg.Document)
		return !photo && !video
	case *tg.MessageMediaDocument:
		_, ok := m.Document.(*tg.Document)
		return !ok
	}
	return false
}

// appendMessage shares the same lifecycle policy across updates, history and
// gap recovery. fallback is authoritative only for peer-less messageEmpty rows
// returned by getHistory; update deletions without a peer stay account-scoped.
func appendMessage(b *engine.Batch, raw tg.MessageClass, edit bool, fallback engine.PeerRef) {
	if raw == nil || raw.GetID() <= 0 {
		return
	}
	appendMessageMigration(b, raw)
	var deleted bool
	var peer engine.PeerRef
	switch m := raw.(type) {
	case *tg.Message:
		deleted = expiredMedia(m.Media)
		peer = peerRef(m.PeerID)
	case *tg.MessageEmpty:
		deleted = true
		peer = peerRef(m.PeerID)
		if peer.ID == 0 {
			peer = fallback
		}
	}
	if deleted {
		d := engine.Deletion{IDs: []int{raw.GetID()}}
		if peer.ID > 0 {
			d.Peer = &peer
		}
		b.Deletes = append(b.Deletes, d)
		return
	}
	if m, ok := convertMessage(b.Account, raw); ok {
		if edit {
			b.Edits = append(b.Edits, m)
		} else {
			b.Messages = append(b.Messages, m)
		}
	}
}
func peerUpserts(users []tg.UserClass, chats []tg.ChatClass) []engine.PeerUpsert {
	var out []engine.PeerUpsert
	for _, raw := range users {
		if u, ok := raw.(*tg.User); ok {
			kind := engine.KindUser
			if u.Self {
				kind = engine.KindSelf
			} else if u.Bot {
				kind = engine.KindBot
			}
			out = append(out, engine.PeerUpsert{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: u.ID}, Kind: kind, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username, IsContact: u.Contact, Access: engine.AccessOK}, AccessHash: u.AccessHash, HashMin: u.Min})
		}
	}
	for _, raw := range chats {
		switch c := raw.(type) {
		case *tg.Chat:
			p := engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChat, ID: c.ID}, Kind: engine.KindGroup, Title: c.Title, Protected: c.Noforwards, Access: engine.AccessOK}
			if next, ok := c.MigratedTo.(*tg.InputChannel); ok && next.ChannelID > 0 {
				ref := engine.PeerRef{Type: engine.PeerChannel, ID: next.ChannelID}
				p.MigratedTo = &ref
			}
			out = append(out, engine.PeerUpsert{Peer: p})
		case *tg.Channel:
			kind := engine.KindChannel
			if c.Megagroup {
				kind = engine.KindSupergroup
			}
			access := engine.AccessOK
			if c.Left {
				access = engine.AccessLeft
			}
			out = append(out, engine.PeerUpsert{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel, ID: c.ID}, Kind: kind, Title: c.Title, Username: c.Username, Forum: c.Forum, Protected: c.Noforwards, Access: access}, AccessHash: c.AccessHash, HashMin: c.Min})
		}
	}
	return out
}
func peerText(p engine.PeerRef) string { return string(p.Type) + ":" + itoa(p.ID) }
func title(p engine.Peer) string {
	if p.Title != "" {
		return p.Title
	}
	return strings.TrimSpace(p.FirstName + " " + p.LastName)
}
