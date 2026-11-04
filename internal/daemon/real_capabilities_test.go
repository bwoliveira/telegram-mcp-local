package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// This is a real controller/gotd/store stack, not fakeengine or a per-tool
// adapter. The sole Telegram boundary is a closed, typed offline RPC allowlist.
// No socket, account, config, session or production database is opened.
type capabilitySession struct{}

func (*capabilitySession) LoadSession(context.Context) ([]byte, error) {
	return nil, engine.ErrNoSession
}
func (*capabilitySession) StoreSession(context.Context, []byte) error { return nil }
func (*capabilitySession) DeleteSession(context.Context) error        { return nil }

type capabilityRPC struct {
	mu       sync.Mutex
	calls    map[string]int
	now      time.Time
	approved bool
}

func (r *capabilityRPC) approve(approved bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approved = approved
}

func capabilityWriteRPC(in bin.Encoder) bool {
	switch in.(type) {
	case *tg.MessagesSendMessageRequest, *tg.MessagesSendMediaRequest,
		*tg.UploadSaveFilePartRequest, *tg.MessagesEditMessageRequest,
		*tg.MessagesSendReactionRequest, *tg.MessagesForwardMessagesRequest,
		*tg.MessagesEditChatTitleRequest, *tg.MessagesDeleteChatUserRequest,
		*tg.MessagesAddChatUserRequest, *tg.MessagesDeleteMessagesRequest,
		*tg.MessagesReadHistoryRequest, *tg.MessagesSetTypingRequest,
		*tg.FoldersEditPeerFoldersRequest:
		return true
	default:
		return false
	}
}

func (r *capabilityRPC) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[fmt.Sprintf("%T", in)]++
	if capabilityWriteRPC(in) && !r.approved {
		return fmt.Errorf("write/read-receipt RPC outside explicit approved execution: %T", in)
	}
	self := &tg.User{ID: 99, Self: true, FirstName: "Offline", Premium: true, Photo: &tg.UserProfilePhoto{PhotoID: 1}}
	member := &tg.User{ID: 10, FirstName: "Member", Username: "offline_member", Contact: true, AccessHash: 10}
	basic := &tg.Chat{ID: 20, Title: "Offline Group", ParticipantsCount: 2, Date: 100, Creator: true}
	switch q := in.(type) {
	case *tg.UsersGetUsersRequest:
		out.(*tg.UserClassVector).Elems = []tg.UserClass{self, member}
	case *tg.MessagesGetDialogsRequest:
		out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{Users: []tg.UserClass{self, member}, Chats: []tg.ChatClass{basic}, Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 99}, TopMessage: 11}, &tg.Dialog{Peer: &tg.PeerChat{ChatID: 20}}}}
	case *tg.UpdatesGetStateRequest:
		*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 1, Date: int(r.now.Unix())}
	case *tg.UpdatesGetDifferenceRequest:
		out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: int(r.now.Unix())}
	case *tg.MessagesGetFullChatRequest:
		if q.ChatID != 20 {
			return fmt.Errorf("unexpected synthetic chat")
		}
		*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChatFull{ID: 20, About: "Offline", Participants: &tg.ChatParticipants{ChatID: 20, Participants: []tg.ChatParticipantClass{&tg.ChatParticipantCreator{UserID: 99}, &tg.ChatParticipant{UserID: 10, Date: 100}}}, ExportedInvite: &tg.ChatInviteExported{Link: "https://t.me/+synthetic-offline"}}, Chats: []tg.ChatClass{basic}, Users: []tg.UserClass{self, member}}
	case *tg.MessagesSearchRequest, *tg.MessagesSearchGlobalRequest:
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Users: []tg.UserClass{self}, Messages: []tg.MessageClass{&tg.Message{ID: 15, PeerID: &tg.PeerUser{UserID: 99}, Date: int(r.now.Unix()), Message: "synthetic remote"}}}
	case *tg.MessagesGetHistoryRequest:
		var msgs []tg.MessageClass
		if q.OffsetID == 0 {
			msgs = []tg.MessageClass{&tg.Message{ID: 11, PeerID: &tg.PeerUser{UserID: 99}, Date: int(r.now.Unix()), Message: "synthetic"}}
		} else {
			msgs = []tg.MessageClass{&tg.Message{ID: q.OffsetID, PeerID: &tg.PeerUser{UserID: 99}, Date: int(r.now.Unix()), Message: "synthetic context"}}
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: msgs, Users: []tg.UserClass{self}}
	case *tg.MessagesGetMessagesRequest:
		var msgs []tg.MessageClass
		for _, id := range q.ID {
			i := id.(*tg.InputMessageID).ID
			media := &tg.Document{ID: 1, Size: 4, MimeType: "application/octet-stream", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "synthetic.bin"}}}
			if i == 5 {
				media.MimeType = "audio/ogg"
				media.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true, Duration: 1}}
			}
			msgs = append(msgs, &tg.Message{ID: i, PeerID: &tg.PeerUser{UserID: 99}, Date: int(r.now.Unix()), Media: &tg.MessageMediaDocument{Document: media}})
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: msgs}
	case *tg.UploadGetFileRequest:
		out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: []byte("data")}
	case *tg.MessagesTranscribeAudioRequest:
		*out.(*tg.MessagesTranscribedAudio) = tg.MessagesTranscribedAudio{TranscriptionID: 1, Text: "synthetic speech"}
	case *tg.MessagesGetPollResultsRequest:
		out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessagePoll{PollID: 7, Results: tg.PollResults{TotalVoters: 1, Results: []tg.PollAnswerVoters{{Option: []byte("a"), Voters: 1}}}}}}
	case *tg.MessagesSendMessageRequest, *tg.MessagesSendMediaRequest:
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 100, Date: int(r.now.Unix())}
	case *tg.UploadSaveFilePartRequest:
		out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
	case *tg.MessagesEditMessageRequest, *tg.MessagesSendReactionRequest, *tg.MessagesForwardMessagesRequest, *tg.MessagesEditChatTitleRequest, *tg.MessagesDeleteChatUserRequest:
		out.(*tg.UpdatesBox).Updates = &tg.Updates{}
	case *tg.MessagesAddChatUserRequest:
		*out.(*tg.MessagesInvitedUsers) = tg.MessagesInvitedUsers{Updates: &tg.Updates{}}
	case *tg.MessagesDeleteMessagesRequest:
		*out.(*tg.MessagesAffectedMessages) = tg.MessagesAffectedMessages{}
	case *tg.MessagesReadHistoryRequest:
		*out.(*tg.MessagesAffectedMessages) = tg.MessagesAffectedMessages{}
	case *tg.MessagesSetTypingRequest:
		out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
	case *tg.FoldersEditPeerFoldersRequest:
		out.(*tg.UpdatesBox).Updates = &tg.Updates{}
	default:
		return fmt.Errorf("RPC absent from offline allowlist: %T", in)
	}
	return nil
}

func capabilityCall(t *testing.T, ctx context.Context, s *mcp.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	raw := s.Handle(ctx, b)
	var r struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Error) > 0 || len(r.Result.Content) != 1 {
		t.Fatalf("%s: invalid RPC result", name)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(r.Result.Content[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	if failure, ok := body["error"].(string); ok {
		lower := strings.ToLower(failure)
		if strings.Contains(lower, "not supported") || strings.Contains(lower, "unsupported") || strings.Contains(lower, "not implemented") {
			t.Fatalf("%s reached a stub: %s", name, failure)
		}
	}
	if r.Result.IsError {
		t.Fatalf("%s: %v", name, body["error"])
	}
	return body
}

func TestRealCapabilitiesAll40ToolsThroughManagedGotdController(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	now := time.Unix(1800000000, 0).UTC()
	db, err := store.Open(ctx, filepath.Join(root, "store.db"), "primary", store.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rpc := &capabilityRPC{calls: map[string]int{}, now: now}
	cfg := engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Store: db, Session: &capabilitySession{}, Mode: engine.ModeReadWrite, Clock: func() time.Time { return now }}
	ctrl, err := gotdengine.NewWithOptions(cfg, gotdengine.Options{Invoker: rpc, RunClient: func(c context.Context, f func(context.Context) error) error { return f(c) }})
	if err != nil {
		t.Fatal(err)
	}
	managed := newManagedController(ctx, nil, cfg, ctrl)
	t.Cleanup(func() { cancel(); <-managed.done })
	if err := ctrl.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	// Ready may precede initial dialogs; wait on durable observations, not a sleep.
	for {
		peers, err := db.ReadPeers(ctx, "primary")
		if err == nil && len(peers) >= 3 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	self := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	group := engine.PeerRef{Type: engine.PeerChat, ID: 20}
	batch := engine.Batch{Account: "primary", Source: engine.SourceRefresh, Received: now}
	for _, id := range []int{4, 5, 8, 9, 10, 11, 12} {
		m := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: self, ID: id}, Date: now, Text: "synthetic", Out: true}
		if id == 4 {
			m.Media = &engine.Media{Type: engine.MediaDocument, Size: 4, Downloadable: true}
		}
		if id == 5 {
			m.Media = &engine.Media{Type: engine.MediaVoice}
		}
		batch.Messages = append(batch.Messages, m)
	}
	batch.Messages = append(batch.Messages, engine.Message{Key: engine.MessageKey{Account: "primary", Peer: group, ID: 7}, Date: now, Poll: &engine.Poll{ID: 7, Question: "Choice?", Anonymous: true, Options: []engine.PollOption{{Text: "A", Key: "YQ=="}}}})
	if _, err := db.Apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(root, "upload.txt")
	if err := os.WriteFile(upload, []byte("synthetic upload"), 0600); err != nil {
		t.Fatal(err)
	}
	s := mcp.New(mcp.Config{Engine: managed, ReadStore: db, Clock: func() time.Time { return now }, MediaDir: filepath.Join(root, "media"), ExportDir: filepath.Join(root, "exports")})
	cases := []struct {
		name  string
		args  map[string]any
		write bool
	}{
		{"health", nil, false}, {"telegram_status", nil, false}, {"list_chats", nil, false},
		{"get_chat_messages", map[string]any{"chat_id": "me"}, false},
		{"search_messages", map[string]any{"query": "synthetic", "remote": true}, false},
		{"list_contacts", nil, false}, {"list_groups", nil, false}, {"get_group", map[string]any{"group_id": "chat:20", "live": true}, false},
		{"send_text_message", map[string]any{"to": "me", "text": "synthetic"}, true},
		{"send_media_message", map[string]any{"to": "me", "type": "document", "url": upload}, true},
		{"download_media", map[string]any{"message_id": "user:99/4", "link": true}, false},
		{"transcribe_audio", map[string]any{"message_id": "user:99/5"}, false},
		{"save_transcript", map[string]any{"message_id": "user:99/5", "text": "synthetic corrected"}, false},
		{"sync_history", map[string]any{"chat_id": "me", "count": 1, "rounds": 1}, false},
		{"delete_message", map[string]any{"chat_id": "me", "message_id": "8"}, true},
		{"edit_message", map[string]any{"chat_id": "me", "message_id": "9", "text": "synthetic updated"}, true},
		{"react_to_message", map[string]any{"chat_id": "me", "message_id": "10", "emoji": "👍"}, true},
		{"resolve_contact", map[string]any{"queries": []string{"@offline_member"}}, false},
		{"get_profile_picture", map[string]any{"to": "me"}, false},
		{"send_location", map[string]any{"to": "me", "latitude": -23.0, "longitude": -46.0}, true},
		{"send_poll", map[string]any{"to": "me", "question": "Choice?", "options": []string{"A", "B"}}, true},
		{"get_poll_results", map[string]any{"message_id": "chat:20/7"}, false},
		{"organise_chat", map[string]any{"chat_id": "me", "action": "archive"}, true},
		{"forward_message", map[string]any{"message_id": "user:99/12", "to": "me"}, true},
		{"mark_chat_read", map[string]any{"chat_id": "me"}, true},
		{"send_typing", map[string]any{"to": "me"}, true},
		{"get_message_context", map[string]any{"chat_id": "me", "message_id": "11"}, false},
		{"message_stats", map[string]any{"chat_id": "me"}, false}, {"export_messages", map[string]any{"chat_id": "me"}, false},
		{"list_unread", nil, false}, {"list_unanswered", nil, false}, {"list_mentions", map[string]any{"chat_id": "me"}, false},
		{"mark_handled", map[string]any{"chat_id": "me"}, false}, {"snooze_chat", map[string]any{"chat_id": "me", "until": now.Add(time.Hour).Format(time.RFC3339)}, false},
		{"manage_group_participants", map[string]any{"group_id": "chat:20", "action": "add", "participants": []string{"@offline_member"}}, true},
		{"update_group", map[string]any{"group_id": "chat:20", "name": "Updated"}, true},
		{"get_group_invite_link", map[string]any{"group_id": "chat:20"}, false},
		{"leave_group", map[string]any{"group_id": "chat:20"}, true},
		{"media_stats", nil, false}, {"purge_media", nil, false},
	}
	catalog := s.Handle(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(catalog, &listed); err != nil {
		t.Fatal(err)
	}
	missing := map[string]bool{}
	properties := map[string]map[string]json.RawMessage{}
	for _, tool := range listed.Result.Tools {
		missing[tool.Name] = true
		properties[tool.Name] = tool.InputSchema.Properties
	}
	parameterPattern := regexp.MustCompile("\\b([a-z][a-z0-9_]+)\\s*[:=]\\s*(?:true|false)\\b|\\b(confirm_[a-z0-9_]+)\\b|`([a-z][a-z0-9_]*)`")
	if len(missing) != 40 || len(cases) != 40 {
		t.Fatalf("catalogue=%d cases=%d", len(missing), len(cases))
	}
	for _, tc := range cases {
		if !missing[tc.name] {
			t.Fatalf("duplicate/unknown tool %s", tc.name)
		}
		delete(missing, tc.name)
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "transcribe_audio" {
				request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tc.name, "arguments": tc.args}})
				if err != nil {
					t.Fatal(err)
				}
				result := s.Handle(ctx, request)
				if !bytes.Contains(result, []byte("local transcription is not installed")) || rpc.calls["*tg.MessagesTranscribeAudioRequest"] != 0 {
					t.Fatalf("MCP transcription was not local-only: %s", result)
				}
				return
			}
			if tc.name == "get_group" {
				cached := capabilityCall(t, ctx, s, tc.name, map[string]any{"group_id": "chat:20"})
				if cached["partial"] != true || cached["snapshot_available"] != false {
					t.Fatal("known basic chat without snapshot was not explicitly partial")
				}
			}
			body := capabilityCall(t, ctx, s, tc.name, tc.args)
			if tc.name == "get_group" {
				cached := capabilityCall(t, ctx, s, tc.name, map[string]any{"group_id": "chat:20"})
				participants, ok := cached["participants"].([]any)
				if !ok || len(participants) != 2 || cached["snapshot_available"] != true || cached["size"] != float64(2) || cached["about"] != body["about"] {
					t.Fatal("live group membership was not retained for offline reads")
				}
			}
			if tc.write {
				preview, ok := body["preview"].(map[string]any)
				if !ok || preview["fingerprint"] == "" {
					t.Fatal("missing concrete engine preview")
				}
				encodedPreview, err := json.Marshal(preview)
				if err != nil {
					t.Fatal(err)
				}
				for _, match := range parameterPattern.FindAllStringSubmatch(string(encodedPreview), -1) {
					for _, field := range match[1:] {
						if field != "" {
							if _, exists := properties[tc.name][field]; !exists {
								t.Errorf("real preview references %s missing from %s inputSchema", field, tc.name)
							}
						}
					}
				}
				tc.args["confirm"] = true
				if tc.name == "delete_message" {
					tc.args["confirm_revoke"] = true
				}
				tc.args["preview_fingerprint"] = preview["fingerprint"]
				rpc.approve(true)
				defer rpc.approve(false)
				approved := capabilityCall(t, ctx, s, tc.name, tc.args)
				if approved["done"] != true {
					t.Fatalf("approved RPC did not complete: %v", approved)
				}
			}
			if tc.name == "purge_media" {
				fingerprint, _ := body["preview_fingerprint"].(string)
				purged := capabilityCall(t, ctx, s, tc.name, map[string]any{"confirm": true, "preview_fingerprint": fingerprint})
				if count, ok := purged["deleted_files"].(float64); !ok || count < 1 {
					t.Fatal("real local media purge was not exercised")
				}
			}
			if tc.name == "sync_history" {
				for {
					job := s.HistoryStatus()
					if job != nil && job.FinishedAt != nil {
						if len(job.Failures) > 0 {
							t.Fatal("history failed", job.Failures)
						}
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(time.Millisecond):
					}
				}
			}
		})
	}
	if len(missing) != 0 {
		t.Fatal("tools not traversed", missing)
	}
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	for _, name := range []string{"*tg.MessagesGetFullChatRequest", "*tg.MessagesSearchGlobalRequest", "*tg.MessagesGetHistoryRequest", "*tg.UploadGetFileRequest", "*tg.MessagesSendMessageRequest", "*tg.MessagesDeleteMessagesRequest", "*tg.MessagesEditChatTitleRequest"} {
		if rpc.calls[name] == 0 {
			t.Errorf("required real capability never reached RPC: %s", name)
		}
	}
	if _, err := managed.HistoryProgress(ctx, self); errors.Is(err, engine.ErrNotSupported) {
		t.Fatal("controller hid history capability")
	}
}
