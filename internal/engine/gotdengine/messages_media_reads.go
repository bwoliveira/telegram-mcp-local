package gotdengine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// PollResults only invokes read RPCs. In particular, do not pass the returned
// Updates to handle: unrelated updates must not change local read counters.
func (e *Engine) PollResults(ctx context.Context, r engine.PollRequest) (engine.PollResults, error) {
	var zero engine.PollResults
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if r.Key.Account != e.cfg.Account {
		return zero, engine.ErrAccountMismatch
	}
	if r.Key.ID <= 0 || !validWriteMessageID(r.Key.ID) || r.Limit < 0 {
		return zero, engine.ErrInvalid
	}
	p, ip, err := e.writePeer(ctx, r.Key.Peer)
	if err != nil {
		return zero, err
	}
	m, ok, err := e.cfg.Store.Message(ctx, r.Key)
	if err != nil {
		return zero, err
	}
	if !ok || m.Key != r.Key || (m.ExpiresAt != nil && !m.ExpiresAt.After(e.now())) {
		return zero, engine.ErrMessageNotFound
	}
	if p.Protected || m.Protected || m.TTLPeriod > 0 || m.ExpiresAt != nil || (m.Media != nil && m.Media.TTLSeconds > 0) {
		return zero, engine.ErrProtectedContent
	}
	if err := e.require(); err != nil {
		return zero, err
	}
	var poll engine.Poll
	if m.Poll != nil && m.Poll.ID != 0 {
		poll = *m.Poll
		poll.Options = append([]engine.PollOption(nil), m.Poll.Options...)
	} else {
		// Older cached messages do not contain the poll definition. Fetch exactly
		// this scoped message, never history (and never mark it read).
		poll, err = e.mediaReadPollDefinition(ctx, r.Key, ip)
		if err != nil {
			return zero, err
		}
	}
	updates, err := e.api.MessagesGetPollResults(ctx, &tg.MessagesGetPollResultsRequest{Peer: ip, MsgID: r.Key.ID})
	if err != nil {
		return zero, rpcError(err)
	}
	var list []tg.UpdateClass
	switch u := updates.(type) {
	case *tg.Updates:
		list = u.Updates
	case *tg.UpdatesCombined:
		list = u.Updates
	case *tg.UpdateShort:
		list = []tg.UpdateClass{u.Update}
	}
	found := false
	for _, raw := range list {
		u, ok := raw.(*tg.UpdateMessagePoll)
		if !ok || u.PollID != poll.ID || (u.Peer != nil && peerRef(u.Peer) != r.Key.Peer) || (u.MsgID != 0 && u.MsgID != r.Key.ID) {
			continue
		}
		if u.Poll.ID != 0 {
			if u.Poll.ID != poll.ID {
				return zero, engine.ErrInvalid
			}
			poll = mediaReadConvertPoll(u.Poll, u.Results)
		} else {
			mediaReadMergePoll(&poll, u.Results)
		}
		found = true
	}
	if !found {
		return zero, engine.ErrInvalid
	}
	result := engine.PollResults{Poll: poll}
	if !r.WithVoters || poll.Anonymous {
		return result, nil
	}
	limit := r.Limit
	if limit == 0 || limit > 100 {
		limit = 100
	}
	votes, err := e.api.MessagesGetPollVotes(ctx, &tg.MessagesGetPollVotesRequest{Peer: ip, ID: r.Key.ID, Limit: limit})
	if err != nil {
		return zero, rpcError(err)
	}
	// One bounded page; mark a truncated voters view explicitly, not as complete.
	result.Poll.Partial = result.Poll.Partial || votes.NextOffset != "" || votes.Count > len(votes.Votes)
	result.Voters = make(map[string][]engine.Peer)
	peers := make(map[engine.PeerRef]engine.Peer)
	for _, up := range peerUpserts(votes.Users, votes.Chats) {
		peers[up.Peer.Ref] = up.Peer
	}
	allowed := make(map[string]bool)
	for _, o := range poll.Options {
		allowed[o.Key] = true
	}
	seen := make(map[string]map[engine.PeerRef]bool)
	for i, v := range votes.Votes {
		if i >= limit {
			result.Poll.Partial = true
			break
		}
		var ref engine.PeerRef
		var options [][]byte
		switch v := v.(type) {
		case *tg.MessagePeerVote:
			ref = peerRef(v.Peer)
			options = [][]byte{v.Option}
		case *tg.MessagePeerVoteMultiple:
			ref = peerRef(v.Peer)
			options = v.Options
		default:
			result.Poll.Partial = true
			continue
		}
		if ref.ID <= 0 {
			result.Poll.Partial = true
			continue
		}
		voter, ok := peers[ref]
		if !ok {
			voter, ok, err = e.cfg.Store.LookupPeer(ctx, e.cfg.Account, ref)
			if err != nil {
				return zero, err
			}
			if !ok {
				voter = engine.Peer{Ref: ref}
			}
		}
		for _, o := range options {
			key := base64.StdEncoding.EncodeToString(o)
			if !allowed[key] {
				result.Poll.Partial = true
				continue
			}
			if seen[key] == nil {
				seen[key] = make(map[engine.PeerRef]bool)
			}
			if !seen[key][ref] {
				result.Voters[key] = append(result.Voters[key], voter)
				seen[key][ref] = true
			}
		}
	}
	return result, nil
}

func mediaReadConvertPoll(p tg.Poll, r tg.PollResults) engine.Poll {
	out := engine.Poll{ID: p.ID, Question: p.Question.Text, Closed: p.Closed, Anonymous: !p.PublicVoters, Multiple: p.MultipleChoice, Quiz: p.Quiz}
	unknownAnswer := false
	for _, answer := range p.Answers {
		a, ok := answer.(*tg.PollAnswer)
		if !ok {
			unknownAnswer = true
			continue
		}
		out.Options = append(out.Options, engine.PollOption{Key: base64.StdEncoding.EncodeToString(a.Option), Text: a.Text.Text})
	}
	mediaReadMergePoll(&out, r)
	out.Partial = out.Partial || unknownAnswer
	return out
}

func mediaReadMergePoll(p *engine.Poll, r tg.PollResults) {
	p.TotalVoters = r.TotalVoters
	p.Partial = r.Min || (r.TotalVoters > 0 && len(r.Results) == 0)
	for i := range p.Options {
		p.Options[i].Voters = 0
		p.Options[i].Correct = false
		if !r.Min {
			p.Options[i].Chosen = false
		}
	}
	for _, v := range r.Results {
		key := base64.StdEncoding.EncodeToString(v.Option)
		for i := range p.Options {
			if p.Options[i].Key != key {
				continue
			}
			p.Options[i].Voters = v.Voters
			if !r.Min {
				p.Options[i].Chosen = v.Chosen
			}
			p.Options[i].Correct = v.Correct
		}
	}
}

func (e *Engine) mediaReadPollDefinition(ctx context.Context, key engine.MessageKey, ip tg.InputPeerClass) (engine.Poll, error) {
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: key.ID}}
	var got tg.MessagesMessagesClass
	var err error
	if p, ok := ip.(*tg.InputPeerChannel); ok {
		got, err = e.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}, ID: ids})
	} else {
		got, err = e.api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return engine.Poll{}, rpcError(err)
	}
	var messages []tg.MessageClass
	switch g := got.(type) {
	case *tg.MessagesMessages:
		messages = g.Messages
	case *tg.MessagesMessagesSlice:
		messages = g.Messages
	case *tg.MessagesChannelMessages:
		messages = g.Messages
	}
	for _, raw := range messages {
		m, ok := raw.(*tg.Message)
		if !ok || m.ID != key.ID || peerRef(m.PeerID) != key.Peer {
			continue
		}
		if m.TTLPeriod > 0 && !time.Unix(int64(m.Date), 0).Add(time.Duration(m.TTLPeriod)*time.Second).After(e.now()) {
			return engine.Poll{}, engine.ErrMessageNotFound
		}
		converted, readable := convertMessage(key.Account, m)
		if m.Noforwards || m.TTLPeriod > 0 || !readable || (converted.Media != nil && converted.Media.TTLSeconds > 0) {
			return engine.Poll{}, engine.ErrProtectedContent
		}
		media, ok := m.Media.(*tg.MessageMediaPoll)
		if !ok {
			return engine.Poll{}, engine.ErrInvalid
		}
		return mediaReadConvertPoll(media.Poll, media.Results), nil
	}
	return engine.Poll{}, engine.ErrMessageNotFound
}

// ProfilePhoto resolves access hashes locally, obtains only the requested peer's
// current public photo and streams it via gotd into an exclusive private file.
func (e *Engine) ProfilePhoto(ctx context.Context, r engine.PhotoRequest) (engine.Download, error) {
	var zero engine.Download
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	_, ip, err := e.writePeer(ctx, r.Peer)
	if errors.Is(err, engine.ErrNoAccess) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	if err := e.require(); err != nil {
		return zero, err
	}
	root, path, err := mediaReadPhotoRoot(r.Dir)
	if err != nil {
		return zero, err
	}
	defer root.Close()
	photoID, err := e.mediaReadPhotoID(ctx, r.Peer, ip)
	if mediaReadPhotoUnavailable(err) {
		return zero, nil
	}
	if err != nil {
		return zero, rpcError(err)
	}
	if photoID == 0 {
		return zero, nil
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return zero, err
	}
	name := "telegram-profile-" + hex.EncodeToString(random[:]) + ".jpg"
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return zero, err
	}
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = root.Remove(name)
		}
	}()
	if err := f.Chmod(0600); err != nil {
		return zero, err
	}
	h := sha256.New()
	writer := &mediaReadPhotoWriter{out: io.MultiWriter(f, h)}
	typ, err := downloader.NewDownloader().Download(e.api, &tg.InputPeerPhotoFileLocation{Peer: ip, PhotoID: photoID, Big: r.Full}).Stream(ctx, writer)
	if mediaReadPhotoUnavailable(err) {
		return zero, nil
	}
	if err != nil {
		if errors.Is(err, engine.ErrInvalid) {
			return zero, engine.ErrInvalid
		}
		return zero, rpcError(err)
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if writer.bytes == 0 {
		return zero, nil
	}
	if err := f.Sync(); err != nil {
		return zero, err
	}
	if err := f.Close(); err != nil {
		return zero, err
	}
	mime := "application/octet-stream"
	switch typ.(type) {
	case *tg.StorageFileJpeg:
		mime = "image/jpeg"
	case *tg.StorageFilePng:
		mime = "image/png"
	case *tg.StorageFileGif:
		mime = "image/gif"
	case *tg.StorageFileWebp:
		mime = "image/webp"
	}
	keep = true
	return engine.Download{Path: filepath.Join(path, name), Bytes: writer.bytes, MediaType: engine.MediaPhoto, MimeType: mime, Filename: name, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func (e *Engine) mediaReadPhotoID(ctx context.Context, ref engine.PeerRef, ip tg.InputPeerClass) (int64, error) {
	switch p := ip.(type) {
	case *tg.InputPeerSelf, *tg.InputPeerUser:
		var user tg.InputUserClass = &tg.InputUserSelf{}
		if p, ok := ip.(*tg.InputPeerUser); ok {
			user = &tg.InputUser{UserID: p.UserID, AccessHash: p.AccessHash}
		}
		users, err := e.api.UsersGetUsers(ctx, []tg.InputUserClass{user})
		if err != nil {
			return 0, err
		}
		for _, raw := range users {
			u, ok := raw.(*tg.User)
			if !ok || u.ID != ref.ID || u.Deleted {
				continue
			}
			if photo, ok := u.Photo.(*tg.UserProfilePhoto); ok {
				return photo.PhotoID, nil
			}
		}
		return 0, nil
	case *tg.InputPeerChat:
		chats, err := e.api.MessagesGetChats(ctx, []int64{p.ChatID})
		if err != nil {
			return 0, err
		}
		return mediaReadChatPhotoID(chats, ref), nil
	case *tg.InputPeerChannel:
		chats, err := e.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}})
		if err != nil {
			return 0, err
		}
		return mediaReadChatPhotoID(chats, ref), nil
	}
	return 0, engine.ErrInvalid
}

func mediaReadChatPhotoID(chats tg.MessagesChatsClass, ref engine.PeerRef) int64 {
	var list []tg.ChatClass
	switch c := chats.(type) {
	case *tg.MessagesChats:
		list = c.Chats
	case *tg.MessagesChatsSlice:
		list = c.Chats
	}
	for _, raw := range list {
		var photo tg.ChatPhotoClass
		switch c := raw.(type) {
		case *tg.Chat:
			if ref.Type == engine.PeerChat && c.ID == ref.ID {
				photo = c.Photo
			}
		case *tg.Channel:
			if ref.Type == engine.PeerChannel && c.ID == ref.ID {
				photo = c.Photo
			}
		}
		if p, ok := photo.(*tg.ChatPhoto); ok {
			return p.PhotoID
		}
	}
	return 0
}

func mediaReadPhotoUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var rpc *tgerr.Error
	return errors.As(err, &rpc) && rpc.IsOneOf("USER_PRIVACY_RESTRICTED", "USER_ID_INVALID", "USER_DEACTIVATED", "USER_DEACTIVATED_BAN", "CHANNEL_PRIVATE", "CHANNEL_INVALID", "CHAT_ID_INVALID", "CHAT_FORBIDDEN", "PHOTO_ID_INVALID", "LOCATION_INVALID", "FILE_LOCATION_INVALID")
}

// Walk through pinned directory handles and reject symlinks at every component.
// The caller supplies the media root; Telegram never supplies any path component.
func mediaReadPhotoRoot(dir string) (*os.Root, string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, "", engine.ErrInvalid
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	// On Windows start at this path's volume root, not the current drive's
	// root. A drive designator (C:) is not a child accepted by os.Root.
	volumeRoot := filepath.VolumeName(path) + string(filepath.Separator)
	root, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return nil, "", err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, volumeRoot), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			err = root.Mkdir(part, 0700)
			if err == nil || errors.Is(err, os.ErrExist) {
				info, err = root.Lstat(part)
			}
		}
		if err != nil {
			_ = root.Close()
			return nil, "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, "", engine.ErrInvalid
		}
		next, err := root.OpenRoot(part)
		_ = root.Close()
		if err != nil {
			return nil, "", err
		}
		root = next
	}
	return root, path, nil
}

// Profile photos have no caller MaxBytes option. Bound storage even if a server
// keeps returning full chunks instead of EOF.
type mediaReadPhotoWriter struct {
	out   io.Writer
	bytes int64
}

func (w *mediaReadPhotoWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > 32<<20-w.bytes {
		return 0, engine.ErrInvalid
	}
	n, err := w.out.Write(b)
	w.bytes += int64(n)
	return n, err
}
