package fake

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func (f *Controller) SendText(ctx context.Context, r engine.SendTextRequest) (engine.WriteResult, error) {
	return f.write(ctx, "send_text", r.Peer, r, r.WriteOptions)
}
func (f *Controller) SendMedia(ctx context.Context, r engine.SendMediaRequest) (engine.WriteResult, error) {
	return f.write(ctx, "send_media", r.Peer, r, r.WriteOptions)
}
func (f *Controller) SendLocation(ctx context.Context, r engine.SendLocationRequest) (engine.WriteResult, error) {
	return f.write(ctx, "send_location", r.Peer, r, r.WriteOptions)
}
func (f *Controller) SendPoll(ctx context.Context, r engine.SendPollRequest) (engine.WriteResult, error) {
	return f.write(ctx, "send_poll", r.Peer, r, r.WriteOptions)
}
func (f *Controller) Edit(ctx context.Context, r engine.EditRequest) (engine.WriteResult, error) {
	return f.write(ctx, "edit", r.Key.Peer, r, r.WriteOptions)
}
func (f *Controller) Delete(ctx context.Context, r engine.DeleteRequest) (engine.WriteResult, error) {
	if !r.DryRun && f.Mode() == engine.ModeReadOnly {
		return engine.WriteResult{}, engine.ErrReadOnly
	}
	if r.Peer.Type == engine.PeerChannel && !r.Revoke {
		return engine.WriteResult{}, engine.ErrNotSupported
	}
	if r.Revoke && !r.DryRun && !r.ConfirmRevoke {
		return engine.WriteResult{}, fmt.Errorf("%w: confirm_revoke:true is required to delete for everyone", engine.ErrInvalid)
	}
	return f.write(ctx, "delete", r.Peer, r, r.WriteOptions)
}
func (f *Controller) React(ctx context.Context, r engine.ReactRequest) (engine.WriteResult, error) {
	return f.write(ctx, "react", r.Key.Peer, r, r.WriteOptions)
}
func (f *Controller) Forward(ctx context.Context, r engine.ForwardRequest) (engine.WriteResult, error) {
	return f.write(ctx, "forward", r.To, r, r.WriteOptions)
}
func (f *Controller) MarkRead(ctx context.Context, r engine.MarkReadRequest) (engine.WriteResult, error) {
	return f.write(ctx, "mark_read", r.Peer, r, r.WriteOptions)
}
func (f *Controller) SetTyping(ctx context.Context, r engine.TypingRequest) (engine.WriteResult, error) {
	return f.write(ctx, "typing", r.Peer, r, r.WriteOptions)
}
func (f *Controller) Organise(ctx context.Context, r engine.OrganiseRequest) (engine.WriteResult, error) {
	return f.write(ctx, "organise", r.Peer, r, r.WriteOptions)
}
func (f *Controller) UpdateGroup(ctx context.Context, r engine.UpdateGroupRequest) (engine.WriteResult, error) {
	return f.write(ctx, "update_group", r.Group, r, r.WriteOptions)
}
func (f *Controller) Leave(ctx context.Context, r engine.LeaveRequest) (engine.WriteResult, error) {
	return f.write(ctx, "leave", r.Group, r, r.WriteOptions)
}
func (f *Controller) ManageParticipants(ctx context.Context, r engine.ParticipantsRequest) (engine.ParticipantsResult, error) {
	result, err := f.write(ctx, "participants", r.Group, r, r.WriteOptions)
	out := engine.ParticipantsResult{WriteResult: result}
	if err == nil && result.Done {
		out.PerUser = make(map[string]string)
		for _, u := range r.Users {
			out.PerUser[peerText(u)] = "ok"
		}
	}
	return out, err
}
func (f *Controller) InviteLink(ctx context.Context, r engine.InviteLinkRequest) (engine.InviteLinkResult, error) {
	if r.Revoke {
		result, err := f.write(ctx, "invite_link", r.Group, r, r.WriteOptions)
		return engine.InviteLinkResult{WriteResult: result, Revoked: result.Done}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.networkLocked(ctx); err != nil {
		return engine.InviteLinkResult{}, err
	}
	if _, err := f.peerLocked(r.Group); err != nil {
		return engine.InviteLinkResult{}, err
	}
	if _, ok := f.groups[r.Group]; !ok {
		return engine.InviteLinkResult{}, engine.ErrInvalid
	}
	// The .invalid TLD makes this visibly synthetic and non-actionable.
	return engine.InviteLinkResult{Link: fmt.Sprintf("https://telegram.invalid/+synthetic-%d", r.Group.ID)}, nil
}

type writePlan struct {
	preview engine.Preview
	batch   engine.Batch

	mediaDigest string
	newKeys     []engine.MessageKey
	affected    int
	groupState  *groupWriteState
}

// Group approval binds permissions and selected fields, not a full mutable peer
// or membership snapshot. Names, counts and unrelated members are presentation.
type groupMemberState struct {
	Known, Present            bool
	Role                      string
	AdminRights, BannedRights uint32
	BannedUntil               int
	CanEdit                   bool
}

type groupWriteState struct {
	SelfID             int64
	AmAdmin, Owner     bool
	Self               groupMemberState
	Users              []groupMemberState
	OldTitle, OldAbout *string
}

func groupIdentity(p engine.Peer) engine.Peer {
	return engine.Peer{Ref: p.Ref, Kind: p.Kind, Access: p.Access, MigratedTo: p.MigratedTo}
}

func groupMemberApproval(g engine.Group, ref engine.PeerRef) groupMemberState {
	for _, member := range g.Participants {
		if member.Peer == ref {
			return groupMemberState{Known: true, Present: true, Role: member.Role, AdminRights: member.AdminRights, BannedRights: member.BannedRights, BannedUntil: member.BannedUntil, CanEdit: member.CanEdit}
		}
	}
	return groupMemberState{Known: !g.Partial}
}

func (f *Controller) write(ctx context.Context, action string, ref engine.PeerRef, request any, opts engine.WriteOptions) (engine.WriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return engine.WriteResult{}, err
	}
	// The gate is deliberately before peer resolution, file reads and persistence.
	if !opts.DryRun && f.status.Mode != engine.ModeReadWrite {
		return engine.WriteResult{}, engine.ErrReadOnly
	}
	if err := f.networkLocked(ctx); err != nil {
		return engine.WriteResult{}, err
	}
	// Compare immutable arguments before looking up mutable targets: a successful
	// delete, edit or leave must still be retryable after its commit.
	raw, err := json.Marshal(request)
	if err != nil {
		return engine.WriteResult{}, engine.ErrInvalid
	}
	var args map[string]json.RawMessage
	if err = json.Unmarshal(raw, &args); err != nil {
		return engine.WriteResult{}, engine.ErrInvalid
	}
	delete(args, "dry_run")
	delete(args, "preview_fingerprint")
	delete(args, "idempotency_key")
	// Explicit confirmation is execution metadata. Revoke remains in args and
	// already binds delete scope into the preview fingerprint.
	delete(args, "confirm_revoke")
	identity, _ := json.Marshal(struct {
		Account engine.AccountID
		Action  string
		Args    map[string]json.RawMessage
	}{f.cfg.Account, action, args})
	requestHash := sha256.Sum256(identity)
	requestDigest := hex.EncodeToString(requestHash[:])
	if !opts.DryRun && opts.IdempotencyKey != "" {
		if previous, ok := f.idempotent[opts.IdempotencyKey]; ok {
			if previous.requestDigest != requestDigest || opts.ExpectFingerprint != previous.fingerprint {
				return engine.WriteResult{}, engine.ErrPreviewMismatch
			}
			return copyOf(previous.result), nil
		}
	}
	var a approval
	if !opts.DryRun {
		var ok bool
		a, ok = f.approvals[opts.ExpectFingerprint]
		age := f.clock().Sub(a.at)
		if !ok || a.used || age < 0 || age >= 10*time.Minute {
			return engine.WriteResult{}, engine.ErrPreviewMismatch
		}
	}
	target, err := f.peerLocked(ref)
	if err != nil {
		return engine.WriteResult{}, err
	}
	request = copyOf(request)
	plan := writePlan{preview: engine.Preview{Action: action, Target: copyOf(target), Reversible: true}, batch: engine.Batch{Source: engine.SourceSend}, affected: 1}
	if err := f.prepareLocked(ctx, &plan, request); err != nil {
		return engine.WriteResult{}, err
	}
	// Only a successful preview allocates a generation. Confirmation recomputes
	// the canonical payload with its original generation, never issuing approval.
	if opts.DryRun {
		f.nextApproval++
		a = approval{at: f.clock(), generation: f.nextApproval}
	}
	// Canonical JSON map ordering and the generation sequence make fingerprints
	// deterministic for the same preview history, but unique per issuance.
	approvalTarget, approvalUsers := target, plan.preview.Users
	if plan.groupState != nil {
		approvalTarget = groupIdentity(target)
		approvalUsers = make([]engine.Peer, len(plan.preview.Users))
		for i, user := range plan.preview.Users {
			approvalUsers[i] = groupIdentity(user)
		}
	}
	canonical, err := json.Marshal(struct {
		Generation  uint64
		Action      string
		Args        map[string]json.RawMessage
		Target      engine.Peer
		Text        string
		Entities    []engine.Entity
		Quoted      *engine.Message
		Affects     []engine.MessageKey
		Users       []engine.Peer
		MediaDigest string
		GroupState  *groupWriteState
	}{a.generation, action, args, approvalTarget, plan.preview.Text, plan.preview.Entities, plan.preview.Quoted, plan.preview.Affects, approvalUsers, plan.mediaDigest, plan.groupState})
	if err != nil {
		return engine.WriteResult{}, engine.ErrInvalid
	}
	sum := sha256.Sum256(canonical)
	fp := hex.EncodeToString(sum[:])
	plan.preview.Fingerprint = fp
	if opts.DryRun {
		f.approvals[fp] = a
		// Bound stale confirmation metadata; the journal itself is intentionally retained.
		for key, a := range f.approvals {
			if f.clock().Sub(a.at) >= 10*time.Minute {
				delete(f.approvals, key)
			}
		}
		if f.status.Mode == engine.ModeReadOnly {
			plan.preview.Warnings = append(plan.preview.Warnings, "read-only mode: this action would be refused; enable writes in the panel")
		}
		return engine.WriteResult{DryRun: true, Preview: copyOf(&plan.preview)}, nil
	}
	if opts.ExpectFingerprint == "" || opts.ExpectFingerprint != fp {
		return engine.WriteResult{}, engine.ErrPreviewMismatch
	}

	if err := ctx.Err(); err != nil {
		return engine.WriteResult{}, err
	}
	applied, err := f.applyLocked(ctx, plan.batch)
	if err != nil {
		return engine.WriteResult{}, err
	}
	// A hidden persisted tombstone may reject a generated key after reopening.
	// Never report a synthetic send as accepted when the store skipped its row.
	if len(plan.newKeys) > 0 && len(applied.Inserted) != len(plan.newKeys) {
		for _, key := range plan.newKeys {
			if key.ID >= f.nextID {
				f.nextID = key.ID + 1
			}
		}
		return engine.WriteResult{}, fmt.Errorf("%w: synthetic send was not inserted", engine.ErrInvalid)
	}
	a.used = true
	f.approvals[fp] = a
	result := engine.WriteResult{Done: true, Preview: &plan.preview, Messages: plan.newKeys, Affected: plan.affected, Note: "synthetic fake-engine acceptance; no Telegram request was made"}
	f.writes = append(f.writes, Write{Sequence: uint64(len(f.writes) + 1), At: f.clock(), Action: action, Peer: ref, Request: request, Result: copyOf(result)})
	if opts.IdempotencyKey != "" {
		f.idempotent[opts.IdempotencyKey] = cachedWrite{fingerprint: fp, requestDigest: requestDigest, result: copyOf(result)}
	}
	return copyOf(result), nil
}
func (f *Controller) prepareLocked(ctx context.Context, p *writePlan, request any) error {
	target := p.preview.Target.Ref
	var outgoing []int
	switch r := request.(type) {
	case engine.SendTextRequest, engine.SendMediaRequest, engine.SendLocationRequest, engine.SendPollRequest:
		ids, err := f.allocateIDsLocked(ctx, target, 1)
		if err != nil {
			return err
		}
		outgoing = ids
	case engine.ForwardRequest:
		ids, err := f.allocateIDsLocked(ctx, target, len(r.IDs))
		if err != nil {
			return err
		}
		outgoing = ids
	}
	newMessage := func(text string) engine.Message {
		self := engine.PeerRef{Type: engine.PeerUser, ID: f.account.SelfID}
		return engine.Message{Key: engine.MessageKey{Account: f.cfg.Account, Peer: target, ID: outgoing[len(p.newKeys)]}, Date: f.clock(), Out: true, Sender: &self, SenderName: "Test Account", Text: text, ChatName: p.preview.Target.Title}
	}
	appendMessage := func(m engine.Message) {
		p.batch.Messages = append(p.batch.Messages, m)
		p.newKeys = append(p.newKeys, m.Key)
	}
	reply := func(id int) error {
		if id == 0 {
			return nil
		}
		if id < 0 {
			return engine.ErrInvalid
		}
		m, err := f.messageLocked(engine.MessageKey{Account: f.cfg.Account, Peer: target, ID: id})
		if err != nil {
			return err
		}
		p.preview.Quoted = copyOf(&m)
		return nil
	}
	checkGroup := func(admin bool) (engine.Group, error) {
		g, ok := f.groups[target]
		if !ok {
			return g, engine.ErrInvalid
		}
		if admin && !g.AmAdmin {
			return g, engine.ErrPermission
		}
		self := engine.PeerRef{Type: engine.PeerUser, ID: f.account.SelfID}
		p.groupState = &groupWriteState{SelfID: f.account.SelfID, AmAdmin: g.AmAdmin, Owner: g.Owner != nil && *g.Owner == self, Self: groupMemberApproval(g, self)}
		return copyOf(g), nil
	}
	switch r := request.(type) {
	case engine.SendTextRequest:
		if r.TopicID < 0 || utf16Len(r.Text) > 4096 || strings.TrimSpace(r.Text) == "" {
			return engine.ErrInvalid
		}
		if err := reply(r.ReplyTo); err != nil {
			return err
		}
		text, entities, err := f.mentionsLocked(r.Text, r.Mentions)
		if err != nil {
			return err
		}
		if utf16Len(text) > 4096 {
			return engine.ErrInvalid
		}
		p.preview.Text = text
		p.preview.Entities = entities
		m := newMessage(text)
		m.Entities = entities
		m.TopicID = r.TopicID
		m.Silent = r.Silent
		if r.ReplyTo > 0 {
			m.ReplyTo = &engine.ReplyRef{ID: r.ReplyTo}
		}
		appendMessage(m)
	case engine.SendMediaRequest:
		switch r.Kind {
		case engine.MediaPhoto, engine.MediaVideo, engine.MediaAudio, engine.MediaVoice, engine.MediaDocument, engine.MediaSticker, engine.MediaAnimation:
		default:
			return engine.ErrInvalid
		}
		if r.Path == "" || r.TopicID < 0 {
			return engine.ErrInvalid
		}
		if err := reply(r.ReplyTo); err != nil {
			return err
		}
		// O_NONBLOCK prevents special files from blocking before fstat rejects
		// them. It has no effect on regular-file reads.
		file, err := os.OpenFile(r.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("%w: cannot open media", engine.ErrInvalid)
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return engine.ErrInvalid
		}
		hash := sha256.New()
		if _, err = io.Copy(hash, contextReader{ctx: ctx, reader: file}); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		p.mediaDigest = hex.EncodeToString(hash.Sum(nil))
		text, entities, err := f.mentionsLocked(r.Caption, r.Mentions)
		if err != nil {
			return err
		}
		if utf16Len(text) > 1024 {
			return engine.ErrInvalid
		}
		p.preview.Text = text
		p.preview.Entities = entities
		m := newMessage(text)
		m.Entities = entities
		m.TopicID = r.TopicID
		m.Media = &engine.Media{Type: r.Kind, Filename: r.Filename, MimeType: r.MimeType, Size: stat.Size(), Downloadable: true, Spoiler: r.Spoiler}
		if r.ReplyTo > 0 {
			m.ReplyTo = &engine.ReplyRef{ID: r.ReplyTo}
		}
		appendMessage(m)
	case engine.SendLocationRequest:
		l := r.Location
		if math.IsNaN(l.Latitude) || math.IsNaN(l.Longitude) || math.IsInf(l.Latitude, 0) || math.IsInf(l.Longitude, 0) || l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 || l.AccuracyMeters < 0 || l.LivePeriod < 0 {
			return engine.ErrInvalid
		}
		if err := reply(r.ReplyTo); err != nil {
			return err
		}
		m := newMessage("")
		m.Location = &l
		m.Media = &engine.Media{Type: engine.MediaGeo}
		if l.Title != "" {
			m.Media.Type = engine.MediaVenue
		}
		if l.LivePeriod > 0 {
			m.Media.Type = engine.MediaLiveGeo
		}
		if r.ReplyTo > 0 {
			m.ReplyTo = &engine.ReplyRef{ID: r.ReplyTo}
		}
		appendMessage(m)
	case engine.SendPollRequest:
		if strings.TrimSpace(r.Question) == "" || utf16Len(r.Question) > 300 || len(r.Options) < 2 || len(r.Options) > 12 {
			return engine.ErrInvalid
		}
		poll := engine.Poll{ID: int64(f.nextID), Question: r.Question, Anonymous: true, Multiple: r.Multiple}
		if r.Anonymous != nil {
			poll.Anonymous = *r.Anonymous
		}
		for i, opt := range r.Options {
			if strings.TrimSpace(opt) == "" || utf16Len(opt) > 100 {
				return engine.ErrInvalid
			}
			poll.Options = append(poll.Options, engine.PollOption{Key: base64.StdEncoding.EncodeToString([]byte{byte(i)}), Text: opt})
		}
		m := newMessage(r.Question)
		m.Poll = &poll
		m.Media = &engine.Media{Type: engine.MediaPoll}
		p.preview.Text = r.Question
		appendMessage(m)
	case engine.EditRequest:
		m, err := f.messageLocked(r.Key)
		if err != nil {
			return err
		}
		if !m.Out {
			return engine.ErrPermission
		}
		if strings.TrimSpace(r.Text) == "" || utf16Len(r.Text) > 4096 {
			return engine.ErrInvalid
		}
		p.preview.Quoted = copyOf(&m)
		text, entities, err := f.mentionsLocked(r.Text, r.Mentions)
		if err != nil {
			return err
		}
		if utf16Len(text) > 4096 {
			return engine.ErrInvalid
		}
		now := f.clock()
		if m.EditDate != nil && !now.After(*m.EditDate) {
			now = m.EditDate.Add(time.Nanosecond)
		}
		m.Text = text
		m.Entities = entities
		m.EditDate = &now
		p.preview.Text = text
		p.preview.Entities = entities
		p.preview.Affects = []engine.MessageKey{r.Key}
		p.batch.Edits = []engine.Message{m}
	case engine.DeleteRequest:
		if len(r.IDs) == 0 {
			return engine.ErrInvalid
		}
		seen := make(map[int]bool)
		for _, id := range r.IDs {
			if seen[id] {
				return engine.ErrInvalid
			}
			seen[id] = true
			key := engine.MessageKey{Account: f.cfg.Account, Peer: target, ID: id}
			if _, err := f.messageLocked(key); err != nil {
				return err
			}
			p.preview.Affects = append(p.preview.Affects, key)
		}
		p.batch.Deletes = []engine.Deletion{{Peer: &target, IDs: r.IDs}}
		p.affected = len(r.IDs)
		p.preview.Reversible = false
		p.preview.Warnings = append(p.preview.Warnings, "deleting messages is irreversible")
		if r.Revoke {
			p.preview.Warnings = append(p.preview.Warnings, engine.DeleteRevokeWarning)
		}
	case engine.ReactRequest:
		m, err := f.messageLocked(r.Key)
		if err != nil {
			return err
		}
		counts := copyOf(m.Reactions)
		var next []engine.ReactionCount
		for _, c := range counts {
			if c.Chosen {
				c.Chosen = false
				c.Count--
			}
			if c.Count > 0 {
				next = append(next, c)
			}
		}
		if r.Emoji != "" {
			found := false
			for i := range next {
				if next[i].Emoji == r.Emoji {
					next[i].Count++
					next[i].Chosen = true
					found = true
				}
			}
			if !found {
				next = append(next, engine.ReactionCount{Emoji: r.Emoji, Count: 1, Chosen: true})
			}
		}
		p.preview.Affects = []engine.MessageKey{r.Key}
		p.batch.Reactions = []engine.ReactionUpdate{{Key: r.Key, Counts: next}}
	case engine.ForwardRequest:
		if len(r.IDs) == 0 || r.TopicID < 0 {
			return engine.ErrInvalid
		}
		source, err := f.peerLocked(r.From)
		if err != nil {
			return err
		}
		sources := make([]engine.Message, 0, len(r.IDs))
		seen := make(map[int]bool)
		for _, id := range r.IDs {
			if seen[id] {
				return engine.ErrInvalid
			}
			seen[id] = true
			m, err := f.messageLocked(engine.MessageKey{Account: f.cfg.Account, Peer: r.From, ID: id})
			if err != nil {
				return err
			}
			if protected(m, source) {
				return engine.ErrProtectedContent
			}
			p.preview.Affects = append(p.preview.Affects, m.Key)
			sources = append(sources, copyOf(m))
			if p.preview.Quoted == nil {
				p.preview.Quoted = copyOf(&m)
			}
			out := newMessage(m.Text)
			out.Entities = copyOf(m.Entities)
			out.Media = copyOf(m.Media)
			out.Poll = copyOf(m.Poll)
			out.Location = copyOf(m.Location)
			out.TopicID = r.TopicID
			out.Silent = r.Silent
			if !r.DropAuthor {
				out.Forward = &engine.ForwardInfo{From: &r.From, FromName: m.SenderName, Date: m.Date}
			}
			appendMessage(out)
		}
		// Preview has only one Quoted slot. Expose every full source snapshot in
		// Text as an ordered action payload; canonicalization above binds all of
		// these snapshots, not just the first quote or the source message keys.
		payload, err := json.Marshal(sources)
		if err != nil {
			return engine.ErrInvalid
		}
		p.preview.Text = string(payload)
		p.affected = len(r.IDs)
	case engine.MarkReadRequest:
		if r.MaxID < 0 {
			return engine.ErrInvalid
		}
		max := r.MaxID
		if max == 0 {
			for k := range f.messages {
				if k.Peer == target && k.ID > max {
					max = k.ID
				}
			}
		}
		unread := 0
		for _, m := range f.messages {
			if m.Key.Peer == target && !m.Out && m.Key.ID > max {
				unread++
			}
		}
		p.batch.Reads = []engine.ReadUpdate{{Peer: target, MaxID: max, StillUnread: unread}}
	case engine.TypingRequest:
		if r.TopicID < 0 {
			return engine.ErrInvalid
		}
		switch r.Action {
		case engine.TypingText, engine.TypingAudio, engine.TypingCancel:
		default:
			return engine.ErrInvalid
		}
	case engine.OrganiseRequest:
		d := f.dialogs[target]
		d.Peer = target
		switch r.Action {
		case engine.OrganiseArchive:
			d.FolderID = 1
		case engine.OrganiseUnarchive:
			d.FolderID = 0
		case engine.OrganisePin:
			d.Pinned = true
		case engine.OrganiseUnpin:
			d.Pinned = false
		case engine.OrganiseMute:
			t := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
			if r.MuteUntil != nil {
				t = *r.MuteUntil
			}
			if !t.After(f.clock()) {
				return engine.ErrInvalid
			}
			d.MuteUntil = &t
		case engine.OrganiseUnmute:
			d.MuteUntil = nil
		default:
			return engine.ErrInvalid
		}
		p.batch.Dialogs = []engine.Dialog{d}
	case engine.ParticipantsRequest:
		g, err := checkGroup(true)
		if err != nil {
			return err
		}
		if len(r.Users) == 0 {
			return engine.ErrInvalid
		}
		switch r.Action {
		case engine.ParticipantsAdd, engine.ParticipantsRemove, engine.ParticipantsBan, engine.ParticipantsPromote, engine.ParticipantsDemote:
		default:
			return engine.ErrInvalid
		}
		p.preview.Text = string(r.Action)
		if r.Action == engine.ParticipantsBan && target.Type == engine.PeerChat {
			p.preview.Warnings = append(p.preview.Warnings, "basic groups support removal only; this does not permanently prevent rejoining")
		}
		if r.Action == engine.ParticipantsPromote && target.Type == engine.PeerChannel {
			p.preview.Warnings = append(p.preview.Warnings, "promotes with minimal administrator status; grants no posting, deletion, ban, invite, or add-admin rights")
		}
		seen := make(map[engine.PeerRef]bool)
		for _, ref := range r.Users {
			if ref.Type != engine.PeerUser || seen[ref] {
				return engine.ErrInvalid
			}
			seen[ref] = true
			user, err := f.peerLocked(ref)
			if err != nil {
				return err
			}
			p.preview.Users = append(p.preview.Users, user)
			p.groupState.Users = append(p.groupState.Users, groupMemberApproval(g, ref))
			index := -1
			for i, member := range g.Participants {
				if member.Peer == ref {
					index = i
					break
				}
			}
			if ref.ID == f.account.SelfID {
				return engine.ErrPermission
			}
			switch r.Action {
			case engine.ParticipantsAdd:
				if index < 0 {
					g.Participants = append(g.Participants, engine.Participant{Peer: ref, Name: strings.TrimSpace(user.FirstName + " " + user.LastName), Username: user.Username, Role: "member"})
					g.Size++
				}
			case engine.ParticipantsRemove, engine.ParticipantsBan:
				if index >= 0 {
					g.Participants = append(g.Participants[:index], g.Participants[index+1:]...)
					g.Size--
				}
			case engine.ParticipantsPromote, engine.ParticipantsDemote:
				if index < 0 {
					return engine.ErrPeerNotFound
				}
				role := "admin"
				if r.Action == engine.ParticipantsDemote {
					role = "member"
				}
				g.Participants[index].Role = role
			}
		}
		if r.Action == engine.ParticipantsBan {
			p.preview.Reversible = false
			p.preview.Warnings = append(p.preview.Warnings, "banning participants is an irreversible moderation action")
		}
		g.SnapshotAt = f.clock()
		g.Peer.Participants = g.Size
		p.batch.Peers = []engine.PeerUpsert{{Peer: g.Peer}}
		p.batch.Participants = []engine.ParticipantsSnapshot{{Group: target, Participants: g.Participants, Size: g.Size, Partial: g.Partial, About: g.About, Owner: g.Owner, CreatedAt: g.CreatedAt, LinkedChat: g.LinkedChat, AmAdmin: g.AmAdmin, At: g.SnapshotAt}}
		p.affected = len(r.Users)
	case engine.UpdateGroupRequest:
		g, err := checkGroup(true)
		if err != nil {
			return err
		}
		if r.Title == nil && r.About == nil {
			return engine.ErrInvalid
		}
		if r.Title != nil {
			oldTitle := g.Peer.Title
			p.groupState.OldTitle = &oldTitle
			if strings.TrimSpace(*r.Title) == "" || utf16Len(*r.Title) > 128 {
				return engine.ErrInvalid
			}
			p.preview.Text = "title: " + *r.Title
			g.Peer.Title = *r.Title
		}
		if r.About != nil {
			oldAbout := g.About
			p.groupState.OldAbout = &oldAbout
			if utf16Len(*r.About) > 255 {
				return engine.ErrInvalid
			}
			if r.Title != nil {
				p.preview.Text += "\n"
			}
			p.preview.Text += "about: " + *r.About
			g.About = *r.About
		}
		p.batch.Peers = []engine.PeerUpsert{{Peer: g.Peer}}
		p.batch.Participants = []engine.ParticipantsSnapshot{{Group: target, Participants: g.Participants, Size: g.Size, Partial: g.Partial, About: g.About, Owner: g.Owner, CreatedAt: g.CreatedAt, LinkedChat: g.LinkedChat, AmAdmin: g.AmAdmin, At: f.clock()}}
	case engine.InviteLinkRequest:
		if _, err := checkGroup(true); err != nil {
			return err
		}
		if !r.Revoke {
			return engine.ErrInvalid
		}
		p.preview.Reversible = false
		p.preview.Warnings = append(p.preview.Warnings, "revoking an invite link is irreversible")
	case engine.LeaveRequest:
		if _, err := checkGroup(false); err != nil {
			return err
		}
		p.batch.Access = []engine.AccessChange{{Peer: target, State: engine.AccessLeft, Reason: "left by request"}}
		p.batch.Gaps = []engine.GapMark{{Peer: &target, Reason: engine.GapAccessLost, Detected: f.clock()}}
		p.batch.Cursor = &engine.Cursor{Peer: target, State: "no_access"}
		p.preview.Reversible = false
		p.preview.Warnings = append(p.preview.Warnings, "leaving a group is irreversible without a new invitation")
	default:
		return engine.ErrNotSupported
	}
	if len(p.batch.Messages) > 0 {
		// Dialog counters follow the synthetic sends, without marking inbox read.
		d := f.dialogs[target]
		d.Peer = target
		d.TopMessageID = p.batch.Messages[len(p.batch.Messages)-1].Key.ID
		p.batch.Dialogs = append(p.batch.Dialogs, d)
	}
	return nil
}
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }
func (f *Controller) mentionsLocked(text string, mentions []engine.Mention) (string, []engine.Entity, error) {
	var entities []engine.Entity
	for _, m := range mentions {
		user, err := f.peerLocked(m.User)
		if err != nil {
			return "", nil, err
		}
		if user.Ref.Type != engine.PeerUser {
			return "", nil, engine.ErrInvalid
		}
		label := m.Text
		if label == "" {
			if user.Username != "" {
				label = "@" + user.Username
			} else {
				label = strings.TrimSpace(user.FirstName + " " + user.LastName)
			}
		}
		if label == "" {
			return "", nil, engine.ErrInvalid
		}
		index := strings.Index(text, label)
		if index < 0 {
			if text != "" {
				text += " "
			}
			index = len(text)
			text += label
		}
		entities = append(entities, engine.Entity{Type: engine.EntityMentionName, Offset: utf16Len(text[:index]), Length: utf16Len(label), UserID: user.Ref.ID})
	}
	return text, entities, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (f *Controller) allocateIDsLocked(ctx context.Context, peer engine.PeerRef, count int) ([]int, error) {
	var out []int
	for id := f.nextID; len(out) < count; id++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := engine.MessageKey{Account: f.cfg.Account, Peer: peer, ID: id}
		if _, ok := f.messages[key]; ok || f.tombstones[key] || (peer.Type != engine.PeerChannel && f.pendingDeletes[id]) {
			continue
		}
		if f.cfg.Store != nil {
			_, exists, err := f.cfg.Store.Message(ctx, key)
			if err != nil {
				return nil, err
			}
			if exists {
				continue
			}
			if peer.Type != engine.PeerChannel {
				keys, err := f.cfg.Store.ResolveMessageIDs(ctx, f.cfg.Account, []int{id})
				if err != nil {
					return nil, err
				}
				if len(keys) > 0 {
					continue
				}
			}
		}
		out = append(out, id)
	}
	return out, nil
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
