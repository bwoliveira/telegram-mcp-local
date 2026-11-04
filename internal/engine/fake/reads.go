package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func (f *Controller) peerLocked(ref engine.PeerRef) (engine.Peer, error) {
	if ref.ID <= 0 || (ref.Type != engine.PeerUser && ref.Type != engine.PeerChat && ref.Type != engine.PeerChannel) {
		return engine.Peer{}, engine.ErrInvalid
	}
	p, ok := f.peers[ref]
	if !ok {
		return p, engine.ErrPeerNotFound
	}
	if p.Access != "" && p.Access != engine.AccessOK {
		return p, engine.ErrNoAccess
	}
	return p, nil
}
func (f *Controller) messageLocked(key engine.MessageKey) (engine.Message, error) {
	if key.Account != f.cfg.Account {
		return engine.Message{}, engine.ErrAccountMismatch
	}
	if key.ID <= 0 {
		return engine.Message{}, engine.ErrInvalid
	}
	if _, err := f.peerLocked(key.Peer); err != nil {
		return engine.Message{}, err
	}
	m, ok := f.messages[key]
	if !ok || expired(m, f.clock()) {
		return engine.Message{}, engine.ErrMessageNotFound
	}
	return m, nil
}
func (f *Controller) Account(ctx context.Context) (engine.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return engine.Account{}, err
	}
	return copyOf(f.account), nil
}
func (f *Controller) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return engine.Diagnostics{}, err
	}
	return engine.Diagnostics{Library: "fakeengine", SessionPresent: f.account.Authenticated, Authorized: f.account.Authenticated, Connected: f.status.State == engine.StateConnected, DC: f.account.DC, LocalUpdates: copyOf(f.status.Updates), CheckedAt: f.clock()}, nil
}
func (f *Controller) Resolve(ctx context.Context, req engine.ResolveRequest) ([]engine.Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return nil, err
	}
	return f.resolveLocked(req)
}
func (f *Controller) resolveLocked(req engine.ResolveRequest) ([]engine.Candidate, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" || req.Limit < 0 {
		return nil, engine.ErrInvalid
	}
	q := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(query, "https://t.me/"), "t.me/"))
	q = strings.TrimPrefix(q, "@")
	var out []engine.Candidate
	for _, p := range f.peers {
		if len(req.Kinds) > 0 {
			match := false
			for _, k := range req.Kinds {
				if p.Kind == k {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		name := strings.TrimSpace(p.FirstName + " " + p.LastName)
		if p.Title != "" {
			name = p.Title
		}
		exact := strings.EqualFold(query, peerText(p.Ref)) || (p.Username != "" && strings.EqualFold(q, p.Username)) || (p.Phone != "" && query == p.Phone) || strings.EqualFold(query, name)
		for _, u := range p.Usernames {
			if strings.EqualFold(q, u) {
				exact = true
			}
		}
		if exact || strings.Contains(strings.ToLower(name), q) {
			out = append(out, engine.Candidate{Peer: copyOf(p), Exact: exact, Source: "store"})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Exact != out[j].Exact {
			return out[i].Exact
		}
		return peerText(out[i].Peer.Ref) < peerText(out[j].Peer.Ref)
	})
	limit := req.Limit
	if limit == 0 {
		limit = 20
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *Controller) ResolveContacts(ctx context.Context, queries []string) ([]engine.ContactResolution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return nil, err
	}
	out := make([]engine.ContactResolution, 0, len(queries))
	for _, q := range queries {
		r := engine.ContactResolution{Query: q, Reason: "not_found"}
		if strings.TrimSpace(q) == "" {
			r.Reason = "invalid"
		} else {
			cs, _ := f.resolveLocked(engine.ResolveRequest{Query: q, Kinds: []engine.PeerKind{engine.KindUser, engine.KindSelf, engine.KindBot}})
			if len(cs) == 1 && cs[0].Exact {
				p := cs[0].Peer
				r.Peer = &p
				r.Found = true
				r.Reason = ""
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func (f *Controller) RefreshDialogs(ctx context.Context, req engine.RefreshRequest) (engine.RefreshResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.RefreshResult{}, err
	}
	if req.Limit < 0 {
		return engine.RefreshResult{}, engine.ErrInvalid
	}
	refs := copyOf(req.Peers)
	if len(refs) == 0 {
		for p := range f.dialogs {
			refs = append(refs, p)
		}
		sort.Slice(refs, func(i, j int) bool { return peerText(refs[i]) < peerText(refs[j]) })
	}
	if req.Limit > 0 && len(refs) > req.Limit {
		refs = refs[:req.Limit]
	}
	b := engine.Batch{Source: engine.SourceRefresh}
	for _, ref := range refs {
		p, err := f.peerLocked(ref)
		if err != nil {
			return engine.RefreshResult{}, err
		}
		b.Peers = append(b.Peers, engine.PeerUpsert{Peer: p})
		if d, ok := f.dialogs[ref]; ok {
			b.Dialogs = append(b.Dialogs, d)
		}
	}
	if _, err := f.applyLocked(ctx, b); err != nil {
		return engine.RefreshResult{}, err
	}
	return engine.RefreshResult{Dialogs: len(b.Dialogs), Peers: len(b.Peers)}, nil
}
func (f *Controller) RefreshGroup(ctx context.Context, req engine.GroupRequest) (engine.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.Group{}, err
	}
	if req.Limit < 0 {
		return engine.Group{}, engine.ErrInvalid
	}
	if _, err := f.peerLocked(req.Group); err != nil {
		return engine.Group{}, err
	}
	g, ok := f.groups[req.Group]
	if !ok {
		return engine.Group{}, engine.ErrInvalid
	}
	g = copyOf(g)
	g.Peer = f.peers[req.Group]
	g.SnapshotAt = f.clock()
	b := engine.Batch{Source: engine.SourceRefresh, Peers: []engine.PeerUpsert{{Peer: g.Peer}}, Participants: []engine.ParticipantsSnapshot{{Group: req.Group, Participants: g.Participants, Size: g.Size, Partial: g.Partial, About: g.About, At: g.SnapshotAt}}}
	if _, err := f.applyLocked(ctx, b); err != nil {
		return engine.Group{}, err
	}
	if req.Search != "" {
		members := g.Participants[:0]
		q := strings.ToLower(req.Search)
		for _, p := range g.Participants {
			if strings.Contains(strings.ToLower(p.Name+" "+p.Username), q) {
				members = append(members, p)
			}
		}
		g.Participants = members
		g.Partial = true
	}
	if req.Limit > 0 && len(g.Participants) > req.Limit {
		g.Participants = g.Participants[:req.Limit]
		g.Partial = true
	}
	return g, nil
}
func (f *Controller) searchLocked(req engine.SearchRequest) []engine.Message {
	var out []engine.Message
	for _, m := range f.messages {
		if expired(m, f.clock()) || (req.Peer != nil && m.Key.Peer != *req.Peer) || (!req.Since.IsZero() && m.Date.Before(req.Since)) || (!req.Until.IsZero() && !m.Date.Before(req.Until)) || (req.Media != "" && (m.Media == nil || m.Media.Type != req.Media)) {
			continue
		}
		if req.Query != "" && !strings.Contains(strings.ToLower(m.Text), strings.ToLower(req.Query)) {
			continue
		}
		out = append(out, copyOf(m))
	}
	sortMessages(out)
	return out
}
func (f *Controller) SearchRemote(ctx context.Context, req engine.SearchRequest) (engine.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.SearchResult{}, err
	}
	if req.Limit < 0 {
		return engine.SearchResult{}, engine.ErrInvalid
	}
	if req.Peer != nil {
		if _, err := f.peerLocked(*req.Peer); err != nil {
			return engine.SearchResult{}, err
		}
	}
	ms := f.searchLocked(req)
	partial := false
	if req.Limit > 0 && len(ms) > req.Limit {
		ms = ms[len(ms)-req.Limit:]
		partial = true
	}
	if _, err := f.applyLocked(ctx, engine.Batch{Source: engine.SourceSearch, Messages: ms}); err != nil {
		return engine.SearchResult{}, err
	}
	return engine.SearchResult{Messages: ms, Partial: partial}, nil
}
func (f *Controller) FetchWindow(ctx context.Context, req engine.WindowRequest) ([]engine.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return nil, err
	}
	if req.Before < 0 || req.After < 0 {
		return nil, engine.ErrInvalid
	}
	if _, err := f.messageLocked(req.Key); err != nil {
		return nil, err
	}
	ms := f.searchLocked(engine.SearchRequest{Peer: &req.Key.Peer})
	var out []engine.Message
	for i, m := range ms {
		if m.Key == req.Key {
			start := i - req.Before
			if start < 0 {
				start = 0
			}
			end := i + req.After + 1
			if end > len(ms) {
				end = len(ms)
			}
			out = ms[start:end]
			break
		}
	}
	if _, err := f.applyLocked(ctx, engine.Batch{Source: engine.SourceSearch, Messages: out}); err != nil {
		return nil, err
	}
	return out, nil
}
func (f *Controller) PollResults(ctx context.Context, req engine.PollRequest) (engine.PollResults, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.PollResults{}, err
	}
	m, err := f.messageLocked(req.Key)
	if err != nil {
		return engine.PollResults{}, err
	}
	if m.Poll == nil {
		return engine.PollResults{}, engine.ErrInvalid
	}
	return engine.PollResults{Poll: copyOf(*m.Poll)}, nil
}
func (f *Controller) Backfill(ctx context.Context, req engine.BackfillRequest) (engine.BackfillResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.BackfillResult{}, err
	}
	if _, err := f.peerLocked(req.Peer); err != nil {
		return engine.BackfillResult{}, err
	}
	if req.Count < 0 || req.Count > 100 || req.Rounds < 0 {
		return engine.BackfillResult{}, engine.ErrInvalid
	}
	count := req.Count
	if count == 0 {
		count = 100
	}
	rounds := req.Rounds
	if rounds == 0 {
		rounds = 1
	}
	ms := f.searchLocked(engine.SearchRequest{Peer: &req.Peer})
	previous := f.cursors[req.Peer]
	if f.cfg.Store != nil {
		c, ok, err := f.cfg.Store.Cursor(ctx, f.cfg.Account, req.Peer)
		if err != nil {
			return engine.BackfillResult{}, err
		}
		if ok {
			previous = c
		}
	}
	if previous.Complete {
		return engine.BackfillResult{Peer: req.Peer, Complete: true, OldestID: previous.OldestID, OldestDate: copyOf(previous.OldestDate)}, nil
	}
	if previous.OldestID > 0 {
		older := ms[:0]
		for _, m := range ms {
			if m.Key.ID < previous.OldestID {
				older = append(older, m)
			}
		}
		ms = older
	}
	bounded := false
	if req.Until != nil {
		filtered := ms[:0]
		for _, m := range ms {
			if !m.Date.Before(*req.Until) {
				filtered = append(filtered, m)
			} else {
				bounded = true
			}
		}
		ms = filtered
	}
	complete := !bounded
	// Division avoids integer overflow from an untrusted rounds parameter.
	if len(ms) > 0 && rounds <= (len(ms)-1)/count {
		ms = ms[len(ms)-count*rounds:]
		complete = false
	}
	used := (len(ms) + count - 1) / count
	c := copyOf(previous)
	c.Peer = req.Peer
	c.Complete = complete
	c.State = "idle"
	c.Fetched += int64(len(ms))
	if len(ms) > 0 {
		c.OldestID = ms[0].Key.ID
		t := ms[0].Date
		c.OldestDate = &t
		if c.NewestID == 0 {
			c.NewestID = ms[len(ms)-1].Key.ID
		}
	}
	a, err := f.applyLocked(ctx, engine.Batch{Source: engine.SourceBackfill, Messages: ms, Cursor: &c})
	if err != nil {
		return engine.BackfillResult{}, err
	}
	return engine.BackfillResult{Peer: req.Peer, Fetched: len(ms), Added: len(a.Inserted), Rounds: used, Complete: complete, OldestID: c.OldestID, OldestDate: c.OldestDate}, nil
}
func protected(m engine.Message, p engine.Peer) bool {
	return m.Protected || p.Protected || (m.Media != nil && m.Media.TTLSeconds > 0)
}
func (f *Controller) Download(ctx context.Context, req engine.DownloadRequest) (engine.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.Download{}, err
	}
	m, err := f.messageLocked(req.Key)
	if err != nil {
		return engine.Download{}, err
	}
	if protected(m, f.peers[m.Key.Peer]) {
		return engine.Download{}, engine.ErrProtectedContent
	}
	if m.Media == nil || !m.Media.Downloadable {
		return engine.Download{}, engine.ErrNotSupported
	}
	data := []byte("Synthetic fake engine media fixture\n")
	if m.Media.Type == engine.MediaPhoto {
		data = photoBytes
	}
	if m.Media.Type == engine.MediaVoice {
		data = voiceBytes
	}
	if req.MaxBytes < 0 || (req.MaxBytes > 0 && int64(len(data)) > req.MaxBytes) {
		return engine.Download{}, engine.ErrInvalid
	}
	d, err := writeDownload(ctx, req.Dir, m.Media.Type, m.Media.MimeType, data)
	if err != nil {
		return d, err
	}
	_, err = f.applyLocked(ctx, engine.Batch{Source: engine.SourceRefresh, Downloads: []engine.DownloadRecord{{Key: m.Key, Path: d.Path, Bytes: d.Bytes, SHA256: d.SHA256, At: f.clock()}}})
	if err != nil {
		_ = os.Remove(d.Path)
		return engine.Download{}, err
	}
	return d, nil
}
func (f *Controller) ProfilePhoto(ctx context.Context, req engine.PhotoRequest) (engine.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.Download{}, err
	}
	if _, err := f.peerLocked(req.Peer); err != nil {
		return engine.Download{}, err
	}
	return writeDownload(ctx, req.Dir, engine.MediaPhoto, "image/png", photoBytes)
}
func writeDownload(ctx context.Context, dir string, kind engine.MediaType, mime string, data []byte) (engine.Download, error) {
	if err := ctx.Err(); err != nil {
		return engine.Download{}, err
	}
	if dir == "" {
		return engine.Download{}, engine.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return engine.Download{}, err
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return engine.Download{}, err
	}
	file, err := os.CreateTemp(path, "fake-media-*")
	if err != nil {
		return engine.Download{}, err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(file.Name())
		}
	}()
	if _, err = file.Write(data); err != nil {
		return engine.Download{}, err
	}
	if err = file.Close(); err != nil {
		return engine.Download{}, err
	}
	if err = ctx.Err(); err != nil {
		return engine.Download{}, err
	}
	sum := sha256.Sum256(data)
	// Rename replaces the directory entry atomically, never follows a planted
	// symlink, and gives repeated fixture downloads a deterministic basename.
	finalPath := filepath.Join(path, fmt.Sprintf("fake-%s-%s", kind, hex.EncodeToString(sum[:])[:16]))
	if err = os.Rename(file.Name(), finalPath); err != nil {
		return engine.Download{}, err
	}
	keep = true
	return engine.Download{Path: finalPath, Bytes: int64(len(data)), MediaType: kind, MimeType: mime, Filename: fmt.Sprintf("synthetic-%s", kind), SHA256: hex.EncodeToString(sum[:])}, nil
}
func (f *Controller) TranscribeNative(ctx context.Context, key engine.MessageKey) (engine.NativeTranscript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.NativeTranscript{}, err
	}
	m, err := f.messageLocked(key)
	if err != nil {
		return engine.NativeTranscript{}, err
	}
	if protected(m, f.peers[key.Peer]) {
		return engine.NativeTranscript{}, engine.ErrProtectedContent
	}
	if m.Media == nil || (m.Media.Type != engine.MediaVoice && m.Media.Type != engine.MediaVideoNote) {
		return engine.NativeTranscript{}, engine.ErrNotSupported
	}
	return engine.NativeTranscript{Text: "This is a synthetic voice transcript.", ID: int64(key.ID)}, nil
}
