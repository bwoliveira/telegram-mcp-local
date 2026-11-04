package gotdengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestMediaReadsPollMissingDefinitionUsesScopedRPC(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(map[bool]string{false: "user", true: "channel"}[channel], func(t *testing.T) {
			e, _ := writeFixture(t)
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
			if channel {
				ref = engine.PeerRef{Type: engine.PeerChannel, ID: 30}
			}
			key := engine.MessageKey{Account: "primary", Peer: ref, ID: 70}
			m := engine.Message{Key: key, Date: e.now()}
			e.cfg.Store = mediaReadMessageStore{Persister: e.cfg.Store, message: m}
			rawPeer := tg.PeerClass(&tg.PeerUser{UserID: 10})
			if channel {
				rawPeer = &tg.PeerChannel{ChannelID: 30}
			}
			poll := tg.Poll{ID: 123, Question: tg.TextWithEntities{Text: "Remote question"}, Answers: []tg.PollAnswerClass{&tg.PollAnswer{Text: tg.TextWithEntities{Text: "A"}, Option: []byte{0, 255}}}}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				switch r := in.(type) {
				case *tg.MessagesGetMessagesRequest:
					if channel || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != 70 {
						t.Fatal(r)
					}
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 70, PeerID: rawPeer, Media: &tg.MessageMediaPoll{Poll: poll}}}}
				case *tg.ChannelsGetMessagesRequest:
					if !channel || r.Channel.(*tg.InputChannel).AccessHash != 3030 || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != 70 {
						t.Fatal(r)
					}
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{&tg.Message{ID: 70, PeerID: rawPeer, Media: &tg.MessageMediaPoll{Poll: poll}}}}
				case *tg.MessagesGetPollResultsRequest:
					out.(*tg.UpdatesBox).Updates = &tg.UpdatesCombined{Updates: []tg.UpdateClass{&tg.UpdateMessagePoll{PollID: 123, Results: tg.PollResults{TotalVoters: 4, Results: []tg.PollAnswerVoters{{Option: []byte{0, 255}, Voters: 4}}}}}}
				default:
					t.Fatalf("forbidden RPC %T", in)
				}
				return nil
			}))
			got, err := e.PollResults(context.Background(), engine.PollRequest{Key: key, WithVoters: true})
			if err != nil || calls != 2 || got.Poll.Question != "Remote question" || !got.Poll.Anonymous || got.Poll.Options[0].Key != "AP8=" || got.Poll.Options[0].Voters != 4 {
				t.Fatalf("%+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestMediaReadsPollRejectsRemoteMismatches(t *testing.T) {
	for _, tc := range []string{"poll", "peer", "message", "definition_peer", "definition_protected", "definition_expired"} {
		t.Run(tc, func(t *testing.T) {
			e, m := mediaReadPollFixture(t, false)
			if len(tc) > 10 && tc[:11] == "definition_" {
				m.Poll = nil
				e.cfg.Store = mediaReadMessageStore{Persister: e.cfg.Store, message: m}
			}
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.ChannelsGetMessagesRequest:
					raw := &tg.Message{ID: m.Key.ID, PeerID: &tg.PeerChannel{ChannelID: 30}, Date: int(e.now().Unix()), Media: &tg.MessageMediaPoll{Poll: tg.Poll{ID: 123}}}
					if tc == "definition_peer" {
						raw.PeerID = &tg.PeerChannel{ChannelID: 40}
					}
					if tc == "definition_protected" {
						raw.Noforwards = true
					}
					if tc == "definition_expired" {
						raw.Date = int(e.now().Add(-time.Hour).Unix())
						raw.TTLPeriod = 60
					}
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{raw}}
				case *tg.MessagesGetPollResultsRequest:
					u := &tg.UpdateMessagePoll{PollID: 123, Results: tg.PollResults{TotalVoters: 9}}
					switch tc {
					case "poll":
						u.PollID = 124
					case "peer":
						u.Peer = &tg.PeerChannel{ChannelID: 40}
					case "message":
						u.MsgID = 71
					}
					out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{u}}
				default:
					t.Fatalf("forbidden RPC %T", in)
				}
				return nil
			}))
			_, err := e.PollResults(context.Background(), engine.PollRequest{Key: m.Key})
			want := engine.ErrInvalid
			if tc == "definition_peer" || tc == "definition_expired" {
				want = engine.ErrMessageNotFound
			}
			if tc == "definition_protected" {
				want = engine.ErrProtectedContent
			}
			if !errors.Is(err, want) {
				t.Fatalf("want %v got %v", want, err)
			}
		})
	}
}

func TestMediaReadsPollRefreshClearsStaleCountsAndPartial(t *testing.T) {
	e, m := mediaReadPollFixture(t, false)
	m.Poll.TotalVoters = 5
	m.Poll.Partial = true
	m.Poll.Options[0].Voters = 5
	m.Poll.Options[0].Chosen = true
	e.cfg.Store = mediaReadMessageStore{Persister: e.cfg.Store, message: m}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetPollResultsRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessagePoll{PollID: 123}}}
		return nil
	}))
	got, err := e.PollResults(context.Background(), engine.PollRequest{Key: m.Key})
	if err != nil || got.Poll.TotalVoters != 0 || got.Poll.Partial || got.Poll.Options[0].Voters != 0 || got.Poll.Options[0].Chosen {
		t.Fatalf("stale values: %+v %v", got, err)
	}
}

func TestMediaReadsPollFutureTTLIsProtected(t *testing.T) {
	for _, onlyPeriod := range []bool{false, true} {
		e, m := mediaReadPollFixture(t, false)
		if onlyPeriod {
			m.TTLPeriod = 60
		} else {
			future := e.now().Add(time.Hour)
			m.ExpiresAt = &future
		}
		e.cfg.Store = mediaReadMessageStore{Persister: e.cfg.Store, message: m}
		e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil }))
		if _, err := e.PollResults(context.Background(), engine.PollRequest{Key: m.Key}); !errors.Is(err, engine.ErrProtectedContent) {
			t.Fatalf("unprotected TTL snapshot: %v", err)
		}
	}
}

func TestMediaReadsCancellationPauseAndFloodWait(t *testing.T) {
	e, m := mediaReadPollFixture(t, false)
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return tgerr.New(420, "FLOOD_WAIT_9") }))
	if _, err := e.PollResults(context.Background(), engine.PollRequest{Key: m.Key}); err == nil {
		t.Fatal("missing error")
	} else {
		var fw *engine.FloodWaitError
		if !errors.As(err, &fw) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.PollResults(ctx, engine.PollRequest{Key: m.Key}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := e.ProfilePhoto(ctx, engine.PhotoRequest{Peer: m.Key.Peer, Dir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	e.paused = true
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil }))
	if _, err := e.PollResults(context.Background(), engine.PollRequest{Key: m.Key}); !errors.Is(err, engine.ErrPaused) {
		t.Fatal(err)
	}
	if _, err := e.ProfilePhoto(context.Background(), engine.PhotoRequest{Peer: m.Key.Peer, Dir: t.TempDir()}); !errors.Is(err, engine.ErrPaused) {
		t.Fatal(err)
	}
}

func TestMediaReadsProfilePhotoCleansPartialChunks(t *testing.T) {
	e, _ := writeFixture(t)
	dir := t.TempDir()
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 10, Photo: &tg.UserProfilePhoto{PhotoID: 1}}}
		case *tg.UploadGetFileRequest:
			calls++
			if r.Offset > 0 {
				return tgerr.New(400, "TEST_FAILURE")
			}
			out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: make([]byte, r.Limit)}
		default:
			t.Fatalf("forbidden RPC %T", in)
		}
		return nil
	}))
	if _, err := e.ProfilePhoto(context.Background(), engine.PhotoRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Dir: dir}); err == nil {
		t.Fatal("missing failure")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 || calls != 2 {
		t.Fatalf("partial file retained: %v %v calls=%d", files, err, calls)
	}
}

func TestMediaReadsProfilePhotoDoesNotOverwriteFiles(t *testing.T) {
	e, _ := writeFixture(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UsersGetUsersRequest:
			out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: 10, Photo: &tg.UserProfilePhoto{PhotoID: 1}}}
		case *tg.UploadGetFileRequest:
			out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: []byte("photo")}
		default:
			t.Fatalf("forbidden RPC %T", in)
		}
		return nil
	}))
	req := engine.PhotoRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Dir: dir}
	a, err := e.ProfilePhoto(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.ProfilePhoto(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "keep" || a.Path == b.Path {
		t.Fatalf("overwrite: %s %v paths %s %s", content, err, a.Path, b.Path)
	}
}
