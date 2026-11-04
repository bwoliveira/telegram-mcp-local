package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// TranscriptWriter persists through the authoritative message transaction. The
// legacy state.db is deliberately not used for Telegram transcripts: only the
// message store can enforce cascade deletion, TTL and FTS replacement together.
// *store.Store already satisfies this interface.
type TranscriptWriter interface {
	SaveTranscript(context.Context, engine.MessageKey, store.Transcript) error
}

// MediaTranscription is the shared MCP/HTTP DTO. Pending applies only to an
// explicitly requested native Telegram transcription; it is never saved as text.
type MediaTranscription struct {
	Transcript       state.Transcript `json:"transcript"`
	Cached           bool             `json:"cached"`
	Pending          bool             `json:"pending,omitempty"`
	Context          []map[string]any `json:"context"`
	Review           string           `json:"review"`
	UntrustedContent string           `json:"untrusted_content"`
}

var mediaTranscriptLocks [64]sync.Mutex

func mediaTranscriptLock(k engine.MessageKey) *sync.Mutex {
	// Fixed stripes bound memory regardless of how many messages are corrected.
	var h uint64
	for _, b := range []byte(fmt.Sprint(k.Account, "/", k.Peer.Type, "/", k.Peer.ID, "/", k.ID)) {
		h = h*31 + uint64(b)
	}
	return &mediaTranscriptLocks[h%uint64(len(mediaTranscriptLocks))]
}

func (s *Server) mediaStore() ReadStore {
	if s.readStore != nil {
		return s.readStore
	}
	st, _ := s.eng.(ReadStore)
	return st
}

func (s *Server) mediaIdentity(ctx context.Context, k engine.MessageKey) error {
	// Validate before resolving the account; never normalize malformed identifiers.
	if k.ID <= 0 || int64(k.ID) > 2147483647 {
		return engine.ErrInvalid
	}
	if _, err := parsePeer(peerID(k.Peer)); err != nil {
		return engine.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.eng == nil {
		return engine.ErrNotSupported
	}
	acc, err := s.eng.Account(ctx)
	if err != nil {
		return err
	}
	if k.Account == "" || acc.ID == "" || k.Account != acc.ID {
		return engine.ErrAccountMismatch
	}
	return nil
}

func (s *Server) mediaMessage(ctx context.Context, k engine.MessageKey, needAccess bool) (engine.Message, []engine.Message, error) {
	if err := s.mediaIdentity(ctx, k); err != nil {
		return engine.Message{}, nil, err
	}
	st := s.mediaStore()
	if st == nil {
		return engine.Message{}, nil, engine.ErrNotSupported
	}
	msgs, err := st.ReadMessages(ctx, k.Account)
	if err != nil {
		return engine.Message{}, nil, err
	}
	var m engine.Message
	found := false
	now := s.clock()
	for _, item := range msgs {
		if item.Key.Account != k.Account {
			return m, nil, engine.ErrAccountMismatch
		}
		if item.Key == k {
			m = item
			found = true
		}
	}
	if !found {
		return m, nil, engine.ErrMessageNotFound
	}
	if (m.ExpiresAt != nil && !m.ExpiresAt.After(now)) || (m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(now)) {
		return m, nil, engine.ErrMessageNotFound
	}
	if m.Protected || (m.Media != nil && m.Media.TTLSeconds > 0) {
		return m, nil, engine.ErrProtectedContent
	}
	peers, err := st.ReadPeers(ctx, k.Account)
	if err != nil {
		return m, nil, err
	}
	for _, p := range peers {
		if p.Ref == k.Peer {
			if p.Protected {
				return m, nil, engine.ErrProtectedContent
			}
			if needAccess && p.Access != "" && p.Access != engine.AccessOK {
				return m, nil, engine.ErrNoAccess
			}
		}
	}
	return m, msgs, nil
}

func mediaAudio(m engine.Message) error {
	if m.Media == nil || (m.Media.Type != engine.MediaAudio && m.Media.Type != engine.MediaVoice) {
		return engine.ErrNotSupported
	}
	return nil
}

func (s *Server) readMediaTranscript(ctx context.Context, k engine.MessageKey) (state.Transcript, bool, error) {
	reader, ok := s.mediaStore().(TranscriptReader)
	if !ok {
		return state.Transcript{}, false, engine.ErrNotSupported
	}
	return reader.ReadTranscript(ctx, k)
}

func transcriptContext(key engine.MessageKey, msgs []engine.Message, now time.Time) []map[string]any {
	selected := make([]engine.Message, 0)
	for _, m := range msgs {
		if m.Key.Peer != key.Peer || m.Key == key || (m.ExpiresAt != nil && !m.ExpiresAt.After(now)) || (m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(now)) || (m.Media != nil && m.Media.TTLSeconds > 0) {
			continue
		}
		selected = append(selected, m)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Key.ID < selected[j].Key.ID })
	before, after := make([]engine.Message, 0), make([]engine.Message, 0)
	for _, m := range selected {
		if m.Key.ID < key.ID {
			before = append(before, m)
		} else {
			after = append(after, m)
		}
	}
	if len(before) > 12 {
		before = before[len(before)-12:]
	}
	if len(after) > 4 {
		after = after[:4]
	}
	out := make([]map[string]any, 0, len(before)+len(after))
	for _, m := range append(before, after...) {
		out = append(out, peopleMessage(m, m.SenderName))
	}
	return out
}

func mediaPrompt(context []map[string]any) string {
	// Context is passed only as one argv value, never interpreted as shell/JSON.
	var b strings.Builder
	for _, m := range context {
		if text, ok := m["text"].(string); ok {
			b.WriteString(text)
			b.WriteByte('\n')
		}
	}
	text := b.String()
	if len(text) > 8192 {
		end := 8192
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end]
	}
	return text
}

// TranscribeMedia is the account-scoped transcription API for HTTP and MCP.
// The default is exclusively local. Missing ASR is reported before downloading.
// Cached results work offline, while a fresh result is saved through store FKs.
func (s *Server) TranscribeMedia(ctx context.Context, key engine.MessageKey, language string, refresh, native bool) (MediaTranscription, error) {
	out := MediaTranscription{Review: reviewGuidance, UntrustedContent: UntrustedContent}
	m, msgs, err := s.mediaMessage(ctx, key, false)
	if err != nil {
		return out, err
	}
	if err = mediaAudio(m); err != nil {
		return out, err
	}
	out.Context = transcriptContext(key, msgs, s.clock())
	if !refresh {
		tr, found, err := s.readMediaTranscript(ctx, key)
		if err != nil {
			return out, err
		}
		if found {
			out.Transcript, out.Cached = tr, true
			return out, nil
		}
	}
	if _, ok := s.mediaStore().(TranscriptWriter); !ok {
		return out, engine.ErrNotSupported
	}
	if !native && (s.asr == nil || !s.asr.Status().Ready) {
		return out, ErrNoEngine
	}
	// Access is required only when obtaining a new transcript, not reading cache.
	if _, _, err = s.mediaMessage(ctx, key, true); err != nil {
		return out, err
	}
	tr := state.Transcript{ChatJID: peerID(key.Peer), MessageID: strconv.Itoa(key.ID), Language: language}
	if native {
		n, e := s.eng.TranscribeNative(ctx, key)
		if e != nil {
			return out, e
		}
		if n.Pending {
			out.Pending = true
			return out, nil
		}
		tr.Text, tr.Raw, tr.Model, tr.Source = n.Text, n.Text, "Telegram (explicit opt-in)", "telegram"
	} else {
		d, e := s.DownloadMedia(ctx, key)
		if e != nil {
			return out, e
		}
		text, e := s.asr.Transcribe(ctx, d.Path, language, mediaPrompt(out.Context))
		if e != nil {
			return out, e
		}
		tr.Text, tr.Raw, tr.Model, tr.Source = text, text, localasr.Label, "local"
	}
	mu := mediaTranscriptLock(key)
	mu.Lock()
	defer mu.Unlock()
	// Refresh/correction never destroys the first recognition, including when
	// another request saved a correction while whisper was running.
	prev, found, err := s.readMediaTranscript(ctx, key)
	if err != nil {
		return out, err
	}
	if found {
		tr.Raw = prev.Raw
		if tr.Raw == "" {
			tr.Raw = prev.Text
		}
	}
	if err = s.persistMediaTranscript(ctx, key, tr); err != nil {
		return out, err
	}
	saved, found, err := s.readMediaTranscript(ctx, key)
	if err != nil {
		return out, err
	}
	if !found {
		return out, engine.ErrMessageNotFound
	}
	out.Transcript = saved
	return out, nil
}

func (s *Server) persistMediaTranscript(ctx context.Context, k engine.MessageKey, t state.Transcript) error {
	writer, ok := s.mediaStore().(TranscriptWriter)
	if !ok {
		return engine.ErrNotSupported
	}
	if _, _, err := s.mediaMessage(ctx, k, true); err != nil {
		return err
	}
	return writer.SaveTranscript(ctx, k, store.Transcript{Text: t.Text, RawText: t.Raw, Language: t.Language, Model: t.Model, Source: t.Source})
}

// SaveMediaTranscript applies a local correction without any Telegram RPC. Raw
// recognition, language and model survive subsequent corrections. Source manual
// is the schema-v1 spelling of a client correction.
func (s *Server) SaveMediaTranscript(ctx context.Context, key engine.MessageKey, text, language, model string) (state.Transcript, error) {
	if strings.TrimSpace(text) == "" {
		return state.Transcript{}, engine.ErrInvalid
	}
	m, _, err := s.mediaMessage(ctx, key, false)
	if err != nil {
		return state.Transcript{}, err
	}
	if err = mediaAudio(m); err != nil {
		return state.Transcript{}, err
	}
	mu := mediaTranscriptLock(key)
	mu.Lock()
	defer mu.Unlock()
	t := state.Transcript{ChatJID: peerID(key.Peer), MessageID: strconv.Itoa(key.ID), Text: strings.TrimSpace(text), Language: language, Model: model, Source: "manual"}
	prev, found, err := s.readMediaTranscript(ctx, key)
	if err != nil {
		return t, err
	}
	if found {
		t.Raw = prev.Raw
		if t.Raw == "" {
			t.Raw = prev.Text
		}
		if t.Language == "" {
			t.Language = prev.Language
		}
		if t.Model == "" {
			t.Model = prev.Model
		}
	} else {
		t.Raw = t.Text
	}
	if err = s.persistMediaTranscript(ctx, key, t); err != nil {
		return state.Transcript{}, err
	}
	saved, found, err := s.readMediaTranscript(ctx, key)
	if err != nil {
		return state.Transcript{}, err
	}
	if !found {
		return state.Transcript{}, engine.ErrMessageNotFound
	}
	return saved, nil
}

// mediaToolError never echoes subprocess diagnostics, SQLite values, or arbitrary
// engine error strings. Known sentinels keep their useful stable meaning.
func mediaToolError(err error) map[string]any {
	for _, sentinel := range []error{engine.ErrInvalid, engine.ErrAccountMismatch, engine.ErrMessageNotFound, engine.ErrProtectedContent, engine.ErrNoAccess, engine.ErrPaused, engine.ErrNotAuthorized, engine.ErrNotSupported, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return toolError("%s", sentinel)
		}
	}
	var flood *engine.FloodWaitError
	if errors.As(err, &flood) {
		return textResult(map[string]any{"error": "Telegram rate limit", "flood_wait_until": flood.Until}, true)
	}
	return toolError("media operation failed")
}
