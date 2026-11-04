package gotdengine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func mediaReadPollFixture(t *testing.T, anonymous bool) (*Engine, engine.Message) {
	t.Helper()
	e, s := writeFixture(t)
	m := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerChannel, ID: 30}, ID: 70}, Date: time.Now(), Poll: &engine.Poll{ID: 123, Question: "Choose", Anonymous: anonymous, Options: []engine.PollOption{{Key: base64.StdEncoding.EncodeToString([]byte{0, 255}), Text: "A"}, {Key: base64.StdEncoding.EncodeToString([]byte{1}), Text: "B"}}}}
	if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
		t.Fatal(err)
	}
	return e, m
}

// The real persister purges protected/TTL messages. Inject snapshots to exercise
// the engine's own guards as well, including a mis-scoped persister response.
type mediaReadMessageStore struct {
	engine.Persister
	message engine.Message
}

func (s mediaReadMessageStore) Message(context.Context, engine.MessageKey) (engine.Message, bool, error) {
	return s.message, true, nil
}

func TestMediaReadsPollResultsAndVoters(t *testing.T) {
	e, m := mediaReadPollFixture(t, false)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		switch r := in.(type) {
		case *tg.MessagesGetPollResultsRequest:
			if r.MsgID != 70 || !reflect.DeepEqual(r.Peer, &tg.InputPeerChannel{ChannelID: 30, AccessHash: 3030}) {
				t.Fatalf("bad scope: %#v", r)
			}
			out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessagePoll{PollID: 123, Results: tg.PollResults{TotalVoters: 2, Results: []tg.PollAnswerVoters{{Option: []byte{0, 255}, Voters: 2, Chosen: true}, {Option: []byte{1}, Voters: 1, Correct: true}}}}}}
		case *tg.MessagesGetPollVotesRequest:
			if r.ID != 70 || r.Limit != 2 || r.Offset != "" || r.Option != nil {
				t.Fatalf("bad votes request: %#v", r)
			}
			*out.(*tg.MessagesVotesList) = tg.MessagesVotesList{Count: 2, NextOffset: "next", Users: []tg.UserClass{&tg.User{ID: 10, FirstName: "Ana"}, &tg.User{ID: 11, FirstName: "Jo"}}, Votes: []tg.MessagePeerVoteClass{&tg.MessagePeerVote{Peer: &tg.PeerUser{UserID: 10}, Option: []byte{0, 255}}, &tg.MessagePeerVoteMultiple{Peer: &tg.PeerUser{UserID: 11}, Options: [][]byte{{0, 255}, {1}}}}}
		default:
			t.Fatalf("forbidden read receipt/write RPC %T", in)
		}
		return nil
	}))
	got, err := e.PollResults(context.Background(), engine.PollRequest{Key: m.Key, WithVoters: true, Limit: 2})
	if err != nil || calls != 2 || got.Poll.TotalVoters != 2 || !got.Poll.Partial || !got.Poll.Options[0].Chosen || !got.Poll.Options[1].Correct {
		t.Fatalf("bad results: %+v calls=%d err=%v", got, calls, err)
	}
	if len(got.Voters[m.Poll.Options[0].Key]) != 2 || got.Voters[m.Poll.Options[1].Key][0].FirstName != "Jo" {
		t.Fatalf("bad voters: %+v", got.Voters)
	}
}

func TestMediaReadsPollAnonymousNeverRequestsVoters(t *testing.T) {
	e, m := mediaReadPollFixture(t, true)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		if _, ok := in.(*tg.MessagesGetPollResultsRequest); !ok {
			t.Fatalf("forbidden RPC %T", in)
		}
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessagePoll{PollID: 123, Results: tg.PollResults{TotalVoters: 3}}}
		return nil
	}))
	got, err := e.PollResults(context.Background(), engine.PollRequest{Key: m.Key, WithVoters: true})
	if err != nil || calls != 1 || !got.Poll.Anonymous || got.Voters != nil || got.Poll.TotalVoters != 3 {
		t.Fatalf("%+v calls=%d err=%v", got, calls, err)
	}
}

func TestMediaReadsPollValidationIsLocal(t *testing.T) {
	for _, tc := range []string{"account", "zero", "overflow", "peer", "missing", "expired", "protected", "peer_protected", "ephemeral", "limit"} {
		t.Run(tc, func(t *testing.T) {
			e, m := mediaReadPollFixture(t, false)
			req := engine.PollRequest{Key: m.Key}
			want := engine.ErrInvalid
			switch tc {
			case "account":
				req.Key.Account = "other"
				want = engine.ErrAccountMismatch
			case "zero":
				req.Key.ID = 0
			case "overflow":
				req.Key.ID = 1 << 31
			case "peer":
				req.Key.Peer.Type = "invalid"
			case "missing":
				req.Key.ID++
				want = engine.ErrMessageNotFound
			case "expired":
				past := e.now().Add(-time.Hour)
				m.ExpiresAt = &past
				want = engine.ErrMessageNotFound
			case "protected":
				m.Protected = true
				want = engine.ErrProtectedContent
			case "peer_protected":
				_, err := e.cfg.Store.Apply(context.Background(), engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: m.Key.Peer, Protected: true}, AccessHash: 3030}}})
				if err != nil {
					t.Fatal(err)
				}
				want = engine.ErrProtectedContent
			case "ephemeral":
				m.Media = &engine.Media{TTLSeconds: 10}
				want = engine.ErrProtectedContent
			case "limit":
				req.Limit = -1
			}
			e.cfg.Store = mediaReadMessageStore{Persister: e.cfg.Store, message: m}
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil }))
			_, err := e.PollResults(context.Background(), req)
			if !errors.Is(err, want) {
				t.Fatalf("want %v got %v", want, err)
			}
		})
	}
}

func TestMediaReadsProfilePhotoAllPeers(t *testing.T) {
	for _, tc := range []struct {
		ref   engine.PeerRef
		input tg.InputPeerClass
	}{
		{engine.PeerRef{Type: engine.PeerUser, ID: 99}, &tg.InputPeerSelf{}},
		{engine.PeerRef{Type: engine.PeerUser, ID: 10}, &tg.InputPeerUser{UserID: 10, AccessHash: 1010}},
		{engine.PeerRef{Type: engine.PeerChat, ID: 20}, &tg.InputPeerChat{ChatID: 20}},
		{engine.PeerRef{Type: engine.PeerChannel, ID: 30}, &tg.InputPeerChannel{ChannelID: 30, AccessHash: 3030}},
	} {
		t.Run(string(tc.ref.Type)+strconv.FormatInt(tc.ref.ID, 10), func(t *testing.T) {
			e, _ := writeFixture(t)
			dir := filepath.Join(t.TempDir(), "media")
			data := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				switch r := in.(type) {
				case *tg.UsersGetUsersRequest:
					var want tg.InputUserClass = &tg.InputUser{UserID: 10, AccessHash: 1010}
					if tc.ref.ID == 99 {
						want = &tg.InputUserSelf{}
					}
					if len(r.ID) != 1 || !reflect.DeepEqual(r.ID[0], want) {
						t.Fatalf("bad user request: %#v", r)
					}
					out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.User{ID: tc.ref.ID, Photo: &tg.UserProfilePhoto{PhotoID: 456}}}
				case *tg.MessagesGetChatsRequest:
					if !reflect.DeepEqual(r.ID, []int64{20}) {
						t.Fatal(r)
					}
					out.(*tg.MessagesChatsBox).Chats = &tg.MessagesChats{Chats: []tg.ChatClass{&tg.Chat{ID: 20, Photo: &tg.ChatPhoto{PhotoID: 456}}}}
				case *tg.ChannelsGetChannelsRequest:
					if !reflect.DeepEqual(r.ID, []tg.InputChannelClass{&tg.InputChannel{ChannelID: 30, AccessHash: 3030}}) {
						t.Fatal(r)
					}
					out.(*tg.MessagesChatsBox).Chats = &tg.MessagesChats{Chats: []tg.ChatClass{&tg.Channel{ID: 30, Photo: &tg.ChatPhoto{PhotoID: 456}}}}
				case *tg.UploadGetFileRequest:
					loc, ok := r.Location.(*tg.InputPeerPhotoFileLocation)
					if !ok || !loc.Big || loc.PhotoID != 456 || !reflect.DeepEqual(loc.Peer, tc.input) || r.Offset != 0 {
						t.Fatalf("bad location: %#v", r)
					}
					out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: data}
				default:
					t.Fatalf("forbidden RPC %T", in)
				}
				return nil
			}))
			got, err := e.ProfilePhoto(context.Background(), engine.PhotoRequest{Peer: tc.ref, Dir: dir, Full: true})
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(got.Path)
			if err != nil {
				t.Fatal(err)
			}
			st, err := os.Stat(got.Path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if calls != 2 || !reflect.DeepEqual(b, data) || service.CheckPrivateDataPath(got.Path, false) != nil || filepath.Dir(got.Path) != dir || got.Bytes != int64(len(data)) || got.SHA256 != hex.EncodeToString(sum[:]) || got.MimeType != "image/jpeg" || got.MediaType != engine.MediaPhoto {
				t.Fatalf("bad download: %+v mode=%v calls=%d", got, st.Mode(), calls)
			}
		})
	}
}

func TestMediaReadsProfilePhotoUnavailableAndFailureCleanup(t *testing.T) {
	for _, tc := range []string{"empty", "private", "unavailable", "download_private", "failure"} {
		t.Run(tc, func(t *testing.T) {
			e, _ := writeFixture(t)
			dir := t.TempDir()
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.UsersGetUsersRequest:
					if tc == "private" {
						return tgerr.New(400, "USER_PRIVACY_RESTRICTED")
					}
					if tc == "unavailable" {
						out.(*tg.UserClassVector).Elems = []tg.UserClass{&tg.UserEmpty{ID: 10}}
						return nil
					}
					user := &tg.User{ID: 10, Photo: &tg.UserProfilePhotoEmpty{}}
					if tc == "download_private" || tc == "failure" {
						user.Photo = &tg.UserProfilePhoto{PhotoID: 456}
					}
					out.(*tg.UserClassVector).Elems = []tg.UserClass{user}
				case *tg.UploadGetFileRequest:
					if tc == "download_private" {
						return tgerr.New(400, "PHOTO_ID_INVALID")
					}
					return tgerr.New(400, "TEST_FAILURE")
				default:
					t.Fatalf("forbidden RPC %T", in)
				}
				return nil
			}))
			got, err := e.ProfilePhoto(context.Background(), engine.PhotoRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Dir: dir})
			if tc == "failure" {
				var rpc *engine.RPCError
				if !errors.As(err, &rpc) || rpc.Type != "TEST_FAILURE" {
					t.Fatalf("wrong error: %v", err)
				}
			} else if err != nil || got != (engine.Download{}) {
				t.Fatalf("want empty got %+v err=%v", got, err)
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("partial files leaked: %v %v", files, err)
			}
		})
	}
}

func TestMediaReadsProfilePhotoRejectsUnsafeRoot(t *testing.T) {
	for _, tc := range []string{"empty", "symlink", "ancestor_symlink", "file"} {
		t.Run(tc, func(t *testing.T) {
			e, _ := writeFixture(t)
			root := t.TempDir()
			dir := ""
			if tc == "symlink" || tc == "ancestor_symlink" {
				dir = filepath.Join(root, "link")
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
				if tc == "ancestor_symlink" {
					dir = filepath.Join(dir, "nested")
				}
			}
			if tc == "file" {
				dir = filepath.Join(root, "file")
				if err := os.WriteFile(dir, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("unexpected RPC"); return nil }))
			if _, err := e.ProfilePhoto(context.Background(), engine.PhotoRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Dir: dir}); err == nil {
				t.Fatal("unsafe root accepted")
			}
		})
	}
}
