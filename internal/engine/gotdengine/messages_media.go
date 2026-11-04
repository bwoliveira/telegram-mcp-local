package gotdengine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// mediaWritePlan is resolved from local state only. Authorization happens once,
// after validating and hashing the exact payload, and before any upload or RPC.
type mediaWritePlan struct {
	preview  *engine.Preview
	input    tg.InputPeerClass
	reply    *tg.InputReplyToMessage
	entities []tg.MessageEntityClass
	selfID   int64
	topic    int
}

func (e *Engine) mediaWritePlan(ctx context.Context, action string, ref engine.PeerRef, text string, mentions []engine.Mention, replyID, topicID int) (mediaWritePlan, error) {
	var p mediaWritePlan
	if !validWriteMessageID(replyID) || !validWriteMessageID(topicID) || !utf8.ValidString(text) {
		return p, engine.ErrInvalid
	}
	target, ip, err := e.writePeer(ctx, ref)
	if err != nil {
		return p, err
	}
	if topicID > 0 && (!target.Forum || ref.Type != engine.PeerChannel) {
		return p, engine.ErrInvalid
	}
	text, resolved, err := e.writeMentions(ctx, text, mentions)
	if err != nil {
		return p, err
	}
	a, err := e.Account(ctx)
	if err != nil {
		return p, err
	}
	p = mediaWritePlan{preview: &engine.Preview{Action: action, Target: target, Text: text, Reversible: true}, input: ip, selfID: a.SelfID, topic: topicID}
	for _, m := range resolved {
		p.preview.Entities = append(p.preview.Entities, m.Entity)
		p.preview.Users = append(p.preview.Users, m.Peer)
		p.entities = append(p.entities, &tg.InputMessageEntityMentionName{Offset: m.Entity.Offset, Length: m.Entity.Length, UserID: m.Input})
	}
	if replyID > 0 {
		key := engine.MessageKey{Account: e.cfg.Account, Peer: ref, ID: replyID}
		m, ok, err := e.cfg.Store.Message(ctx, key)
		if err != nil {
			return p, err
		}
		if !ok || m.Key != key || (m.ExpiresAt != nil && !m.ExpiresAt.After(e.now())) {
			return p, engine.ErrMessageNotFound
		}
		if topicID > 0 && m.TopicID > 0 && m.TopicID != topicID {
			return p, engine.ErrInvalid
		}
		p.preview.Quoted = &m
	}
	if replyID > 0 || topicID > 0 {
		p.reply = &tg.InputReplyToMessage{ReplyToMsgID: replyID}
		if replyID == 0 {
			p.reply.ReplyToMsgID = topicID
		}
		if topicID > 0 && topicID != 1 && p.reply.ReplyToMsgID != topicID {
			p.reply.SetTopMsgID(topicID)
		}
	}
	return p, nil
}

func (e *Engine) mediaWriteApprove(opts engine.WriteOptions, p *mediaWritePlan, intent any) (bool, error) {
	var err error
	p.preview.Fingerprint, err = canonicalWriteFingerprint(struct {
		Action   string
		Account  engine.AccountID
		SelfID   int64
		Preview  *engine.Preview
		Input    tg.InputPeerClass
		Reply    *tg.InputReplyToMessage
		Entities []tg.MessageEntityClass
		Key      string
		Intent   any
	}{p.preview.Action, e.cfg.Account, p.selfID, p.preview, p.input, p.reply, p.entities, opts.IdempotencyKey, intent})
	if err != nil {
		return false, err
	}
	if !opts.DryRun {
		if err = e.require(); err != nil {
			return false, err
		}
	}
	return e.checkWritePreview(opts, p.preview)
}

func (e *Engine) SendMedia(ctx context.Context, r engine.SendMediaRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !r.DryRun && e.Mode() == engine.ModeReadOnly {
		return zero, engine.ErrReadOnly
	}
	switch r.Kind {
	case engine.MediaPhoto, engine.MediaVideo, engine.MediaAudio, engine.MediaVoice, engine.MediaDocument, engine.MediaSticker, engine.MediaAnimation:
	default:
		return zero, engine.ErrInvalid
	}
	if r.Path == "" || !utf8.ValidString(r.Filename) || strings.ContainsAny(r.Filename, "/\\\x00") {
		return zero, engine.ErrInvalid
	}
	p, err := e.mediaWritePlan(ctx, "send_media", r.Peer, r.Caption, r.Mentions, r.ReplyTo, r.TopicID)
	if err != nil {
		return zero, err
	}
	if writeUTF16Len(p.preview.Text) > 1024 {
		return zero, engine.ErrInvalid
	}
	info, err := os.Lstat(r.Path)
	if err != nil {
		return zero, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 2<<30 {
		return zero, engine.ErrInvalid
	}
	f, err := os.Open(r.Path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return zero, err
	}
	if !os.SameFile(info, opened) {
		return zero, engine.ErrInvalid
	}
	name := r.Filename
	if name == "" {
		name = filepath.Base(r.Path)
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return zero, engine.ErrInvalid
	}
	contentType := r.MimeType
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(name))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	contentType, _, err = mime.ParseMediaType(contentType)
	if err != nil {
		return zero, engine.ErrInvalid
	}
	hash := sha256.New()
	var snapshot *os.File
	var dest io.Writer = hash
	if !r.DryRun {
		// Upload only this private snapshot, never reopen the caller's mutable path.
		snapshot, err = os.CreateTemp("", "telegram-approved-media-*")
		if err != nil {
			return zero, err
		}
		defer func() { snapshot.Close(); os.Remove(snapshot.Name()) }()
		dest = io.MultiWriter(hash, snapshot)
	}
	n, err := io.Copy(dest, &mediaContextReader{ctx: ctx, r: io.LimitReader(f, 2<<30+1)})
	if err != nil {
		return zero, err
	}
	if n <= 0 || n > 2<<30 {
		return zero, engine.ErrInvalid
	}
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	p.preview.Warnings = append(p.preview.Warnings, fmt.Sprintf("upload %s (%s), %d bytes; SHA256 %s", name, r.Kind, n, digest))
	original := r
	original.WriteOptions = engine.WriteOptions{IdempotencyKey: r.IdempotencyKey}
	dry, err := e.mediaWriteApprove(r.WriteOptions, &p, struct {
		Request            engine.SendMediaRequest
		Name, MIME, SHA256 string
		Bytes              int64
	}{original, name, contentType, digest, n})
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p.preview}, nil
	}
	if _, err = snapshot.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	file, err := uploader.NewUploader(mediaWriteUploader{e: e}).WithThreads(1).Upload(ctx, uploader.NewUpload(name, snapshot, n))
	if err != nil {
		if errors.Is(err, engine.ErrReadOnly) {
			return zero, engine.ErrReadOnly
		}
		return zero, rpcError(err)
	}
	var media tg.InputMediaClass
	if r.Kind == engine.MediaPhoto {
		media = &tg.InputMediaUploadedPhoto{File: file, Spoiler: r.Spoiler}
	} else {
		attrs := []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}}
		switch r.Kind {
		case engine.MediaAudio:
			attrs = append(attrs, &tg.DocumentAttributeAudio{})
		case engine.MediaVoice:
			attrs = append(attrs, &tg.DocumentAttributeAudio{Voice: true})
		case engine.MediaVideo:
			attrs = append(attrs, &tg.DocumentAttributeVideo{SupportsStreaming: true})
		case engine.MediaAnimation:
			attrs = append(attrs, &tg.DocumentAttributeAnimated{})
		case engine.MediaSticker:
			attrs = append(attrs, &tg.DocumentAttributeSticker{Stickerset: &tg.InputStickerSetEmpty{}})
		}
		media = &tg.InputMediaUploadedDocument{File: file, MimeType: contentType, Attributes: attrs, ForceFile: r.Kind == engine.MediaDocument, Spoiler: r.Spoiler}
	}
	return e.mediaWriteSend(ctx, p, r.WriteOptions, media, engine.Message{Media: &engine.Media{Type: r.Kind, Filename: name, MimeType: contentType, Size: n, Spoiler: r.Spoiler}})
}

type mediaWriteUploader struct{ e *Engine }

func (u mediaWriteUploader) UploadSaveFilePart(ctx context.Context, r *tg.UploadSaveFilePartRequest) (bool, error) {
	if u.e.Mode() == engine.ModeReadOnly {
		return false, engine.ErrReadOnly
	}
	if err := u.e.require(); err != nil {
		return false, err
	}
	return u.e.api.UploadSaveFilePart(ctx, r)
}

func (u mediaWriteUploader) UploadSaveBigFilePart(ctx context.Context, r *tg.UploadSaveBigFilePartRequest) (bool, error) {
	if u.e.Mode() == engine.ModeReadOnly {
		return false, engine.ErrReadOnly
	}
	if err := u.e.require(); err != nil {
		return false, err
	}
	return u.e.api.UploadSaveBigFilePart(ctx, r)
}

type mediaContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *mediaContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

func (e *Engine) SendLocation(ctx context.Context, r engine.SendLocationRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !r.DryRun && e.Mode() == engine.ModeReadOnly {
		return zero, engine.ErrReadOnly
	}
	loc := r.Location
	if math.IsNaN(loc.Latitude) || math.IsInf(loc.Latitude, 0) || math.IsNaN(loc.Longitude) || math.IsInf(loc.Longitude, 0) || loc.Latitude < -90 || loc.Latitude > 90 || loc.Longitude < -180 || loc.Longitude > 180 || loc.AccuracyMeters < 0 || loc.AccuracyMeters > 1500 || loc.LivePeriod < 0 {
		return zero, engine.ErrInvalid
	}
	if loc.LivePeriod != 0 && loc.LivePeriod != 60 && loc.LivePeriod != 900 && loc.LivePeriod != 3600 && loc.LivePeriod != 28800 && loc.LivePeriod != 2147483647 {
		return zero, engine.ErrInvalid
	}
	text := strings.TrimSpace(strings.Join([]string{loc.Title, loc.Address}, "\n"))
	if writeUTF16Len(text) > 1024 {
		return zero, engine.ErrInvalid
	}
	p, err := e.mediaWritePlan(ctx, "send_location", r.Peer, text, nil, r.ReplyTo, 0)
	if err != nil {
		return zero, err
	}
	p.preview.Warnings = append(p.preview.Warnings, fmt.Sprintf("coordinates %.8f, %.8f; accuracy %d m; live period %d s", loc.Latitude, loc.Longitude, loc.AccuracyMeters, loc.LivePeriod))
	original := r
	original.WriteOptions = engine.WriteOptions{IdempotencyKey: r.IdempotencyKey}
	dry, err := e.mediaWriteApprove(r.WriteOptions, &p, original)
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p.preview}, nil
	}
	geo := &tg.InputGeoPoint{Lat: loc.Latitude, Long: loc.Longitude}
	if loc.AccuracyMeters > 0 {
		geo.SetAccuracyRadius(loc.AccuracyMeters)
	}
	var media tg.InputMediaClass = &tg.InputMediaGeoPoint{GeoPoint: geo}
	if loc.LivePeriod > 0 {
		media = &tg.InputMediaGeoLive{GeoPoint: geo, Period: loc.LivePeriod}
	}
	return e.mediaWriteSend(ctx, p, r.WriteOptions, media, engine.Message{Location: &loc})
}

func (e *Engine) SendPoll(ctx context.Context, r engine.SendPollRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !r.DryRun && e.Mode() == engine.ModeReadOnly {
		return zero, engine.ErrReadOnly
	}
	if !utf8.ValidString(r.Question) || strings.TrimSpace(r.Question) == "" || writeUTF16Len(r.Question) > 300 || len(r.Options) < 2 || len(r.Options) > 12 {
		return zero, engine.ErrInvalid
	}
	poll := tg.Poll{Question: tg.TextWithEntities{Text: r.Question}, MultipleChoice: r.Multiple, PublicVoters: r.Anonymous != nil && !*r.Anonymous}
	dto := engine.Poll{Question: r.Question, Multiple: r.Multiple, Anonymous: !poll.PublicVoters}
	seen := make(map[string]bool)
	for i, o := range r.Options {
		if !utf8.ValidString(o) || strings.TrimSpace(o) == "" || writeUTF16Len(o) > 100 || seen[o] {
			return zero, engine.ErrInvalid
		}
		seen[o] = true
		key := []byte{byte(i)}
		poll.Answers = append(poll.Answers, &tg.PollAnswer{Text: tg.TextWithEntities{Text: o}, Option: key})
		dto.Options = append(dto.Options, engine.PollOption{Key: base64.StdEncoding.EncodeToString(key), Text: o})
	}
	p, err := e.mediaWritePlan(ctx, "send_poll", r.Peer, "", nil, 0, 0)
	if err != nil {
		return zero, err
	}
	p.preview.Text = r.Question + "\n" + strings.Join(r.Options, "\n")
	original := r
	original.WriteOptions = engine.WriteOptions{IdempotencyKey: r.IdempotencyKey}
	dry, err := e.mediaWriteApprove(r.WriteOptions, &p, original)
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p.preview}, nil
	}
	// Poll question/options belong to InputMediaPoll, not a duplicate caption.
	p.preview.Text = ""
	return e.mediaWriteSend(ctx, p, r.WriteOptions, &tg.InputMediaPoll{Poll: poll}, engine.Message{Poll: &dto})
}

func (e *Engine) mediaWriteSend(ctx context.Context, p mediaWritePlan, opts engine.WriteOptions, media tg.InputMediaClass, m engine.Message) (engine.WriteResult, error) {
	var zero engine.WriteResult
	// Recheck after a potentially long upload; a mode switch must stop sending.
	if e.Mode() == engine.ModeReadOnly {
		return zero, engine.ErrReadOnly
	}
	if err := e.require(); err != nil {
		return zero, err
	}
	id, err := e.sendRandomID(opts.IdempotencyKey)
	if err != nil {
		return zero, err
	}
	req := &tg.MessagesSendMediaRequest{Peer: p.input, Media: media, Message: p.preview.Text, RandomID: id, Entities: p.entities}
	if p.reply != nil {
		req.ReplyTo = p.reply
	}
	got, err := e.api.MessagesSendMedia(ctx, req)
	if err != nil {
		return zero, rpcError(err)
	}
	result := engine.WriteResult{Done: true, Affected: 1, Note: "accepted by Telegram; not delivery or read confirmation"}
	if short, ok := got.(*tg.UpdateShortSentMessage); ok {
		m.Key = engine.MessageKey{Account: e.cfg.Account, Peer: p.preview.Target.Ref, ID: short.ID}
		result.Messages = []engine.MessageKey{m.Key}
		if expiredMedia(short.Media) {
			// A payload-free photo/document is a server deletion, even in a
			// short receipt. Never restore our upload DTO as ordinary media.
			if _, err := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceSend, Deletes: []engine.Deletion{{Peer: &m.Key.Peer, IDs: []int{m.Key.ID}}}}); err != nil {
				return acceptedWriteError(result, err)
			}
			return acceptedWriteError(result, e.manager.HandleAffected(ctx, 0, short.Pts, short.PtsCount))
		}
		m.Date = time.Unix(int64(short.Date), 0)
		m.Text = p.preview.Text
		m.Entities = p.preview.Entities
		m.Out = true
		m.TopicID = p.topic
		m.Protected = p.preview.Target.Protected
		m.Post = p.preview.Target.Kind == engine.KindChannel
		self := engine.PeerRef{Type: engine.PeerUser, ID: p.selfID}
		m.Sender = &self
		m.TTLPeriod = short.TTLPeriod
		if p.reply != nil {
			m.ReplyTo = &engine.ReplyRef{ID: p.reply.ReplyToMsgID, TopID: p.topic}
		}
		if short.TTLPeriod > 0 {
			expires := m.Date.Add(time.Duration(short.TTLPeriod) * time.Second)
			m.ExpiresAt = &expires
		}
		if short.Media != nil {
			if pollMedia, ok := short.Media.(*tg.MessageMediaPoll); ok {
				poll := mediaReadConvertPoll(pollMedia.Poll, pollMedia.Results)
				m.Poll = &poll
			}
			if converted, ok := convertMessage(e.cfg.Account, &tg.Message{ID: short.ID, PeerID: mediaPeerClass(m.Key.Peer), Date: short.Date, Media: short.Media}); ok && converted.Media != nil && m.Media != nil {
				// Preserve known upload type/name; the shared converter currently
				// retains only a content-free kind and TTL placeholder.
				m.Media.TTLSeconds = converted.Media.TTLSeconds
			}
		}
		result.Messages = []engine.MessageKey{m.Key}
		if _, err = e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceSend, Messages: []engine.Message{m}}); err != nil {
			return acceptedWriteError(result, err)
		}
		return acceptedWriteError(result, e.manager.HandleAffected(ctx, 0, short.Pts, short.PtsCount))
	}
	result.Messages = e.acceptedMessageKeys(got, p.preview.Target.Ref, map[int64]bool{id: true})
	// A running manager only enqueues the RPC result. Persist its authoritative
	// snapshot now so an immediate download can find our own sent message. Keep
	// the response intact for the manager's sequencing; its later delivery is an
	// idempotent replay, and no checkpoint is inferred or advanced here.
	if err := e.handle(ctx, got); err != nil {
		return acceptedWriteError(result, err)
	}
	return acceptedWriteError(result, e.manager.Handle(ctx, got))
}

func mediaPeerClass(ref engine.PeerRef) tg.PeerClass {
	switch ref.Type {
	case engine.PeerUser:
		return &tg.PeerUser{UserID: ref.ID}
	case engine.PeerChat:
		return &tg.PeerChat{ChatID: ref.ID}
	default:
		return &tg.PeerChannel{ChannelID: ref.ID}
	}
}
