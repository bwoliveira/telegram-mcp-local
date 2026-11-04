package gotdengine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

var writeReturnSendKinds = []string{"text", "forward", "poll", "location", "photo", "video", "audio", "voice", "document", "sticker", "animation"}
var writeReturnMediaKinds = []string{"photo", "video", "audio", "voice", "document", "sticker", "animation"}

// Use the existing all-send fixture/call/raw/upload helpers without relaxing any
// assertions in their immediate-persistence and acceptance-failure tests.
func writeReturnRPC(t *testing.T, e *Engine, kind, envelope string, raw *tg.Message) (*int, *tg.UpdatesClass) {
	t.Helper()
	calls := new(int)
	var returned tg.UpdatesClass
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		var randomID int64
		switch r := in.(type) {
		case *tg.UploadSaveFilePartRequest:
			out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
			return nil
		case *tg.MessagesSendMessageRequest:
			randomID = r.RandomID
		case *tg.MessagesSendMediaRequest:
			randomID = r.RandomID
		case *tg.MessagesForwardMessagesRequest:
			if len(r.RandomID) != 1 {
				t.Fatalf("wrong forward random IDs: %v", r.RandomID)
			}
			randomID = r.RandomID[0]
		case *tg.MessagesEditMessageRequest:
			if kind != "edit" || r.ID != raw.ID {
				t.Fatalf("wrong edit request: %+v", r)
			}
		default:
			t.Fatalf("forbidden RPC %T", in)
		}
		*calls++
		var update tg.UpdateClass = &tg.UpdateNewMessage{Message: raw, Pts: 2, PtsCount: 1}
		if peerRef(raw.PeerID).Type == engine.PeerChannel {
			update = &tg.UpdateNewChannelMessage{Message: raw, Pts: 2, PtsCount: 1}
		}
		if kind == "edit" {
			update = &tg.UpdateEditMessage{Message: raw, Pts: 2, PtsCount: 1}
			if peerRef(raw.PeerID).Type == engine.PeerChannel {
				update = &tg.UpdateEditChannelMessage{Message: raw, Pts: 2, PtsCount: 1}
			}
		}
		us := []tg.UpdateClass{update}
		if kind != "edit" {
			us = append([]tg.UpdateClass{&tg.UpdateMessageID{ID: raw.ID, RandomID: randomID}}, us...)
		}
		switch envelope {
		case "updates":
			returned = &tg.Updates{Updates: us, Date: raw.Date, Seq: 1}
		case "combined":
			returned = &tg.UpdatesCombined{Updates: us, Date: raw.Date, SeqStart: 1, Seq: 1}
		case "short":
			returned = &tg.UpdateShort{Update: update, Date: raw.Date}
		case "short_sent":
			returned = &tg.UpdateShortSentMessage{ID: raw.ID, Out: true, Date: raw.Date, Pts: 2, PtsCount: 1, Media: raw.Media, TTLPeriod: raw.TTLPeriod}
		default:
			t.Fatal(envelope)
		}
		out.(*tg.UpdatesBox).Updates = returned
		return nil
	}))
	return calls, &returned
}

func writeReturnSend(t *testing.T, e *Engine, ctx context.Context, kind string, ref engine.PeerRef, calls *int) engine.WriteResult {
	t.Helper()
	path := sendAllUpload(t)
	preview, err := sendAllCall(e, ctx, kind, ref, path, engine.WriteOptions{DryRun: true})
	if err != nil || preview.Preview == nil || *calls != 0 {
		t.Fatalf("preview: %+v err=%v calls=%d", preview, err, *calls)
	}
	result, err := sendAllCall(e, ctx, kind, ref, path, engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint})
	if err != nil || !result.Done || result.Affected != 1 || *calls != 1 {
		t.Fatalf("send: %+v err=%v calls=%d", result, err, *calls)
	}
	return result
}

func writeReturnSetMediaTTL(raw *tg.Message, ttl int) bool {
	switch m := raw.Media.(type) {
	case *tg.MessageMediaPhoto:
		m.TTLSeconds = ttl
		return true
	case *tg.MessageMediaDocument:
		m.TTLSeconds = ttl
		return true
	default:
		return false
	}
}

func writeReturnAssertSecurity(t *testing.T, db *store.Store, ctx context.Context, key engine.MessageKey, protected bool, ttl int, expires time.Time, mediaTTL int) engine.Message {
	t.Helper()
	m, found, err := db.Message(ctx, key)
	if err != nil || !found {
		t.Fatalf("missing durable write result: found=%v err=%v", found, err)
	}
	if m.Protected != protected || m.TTLPeriod != ttl || m.ExpiresAt == nil || !m.ExpiresAt.Equal(expires) {
		t.Fatalf("security weakened: protected=%v ttl=%d expires=%v; want %v/%d/%v", m.Protected, m.TTLPeriod, m.ExpiresAt, protected, ttl, expires)
	}
	if mediaTTL > 0 {
		if m.Media == nil || m.Media.TTLSeconds != mediaTTL || m.Media.Downloadable || len(m.Media.Remote) != 0 || m.Media.Filename != "" || m.Media.Size != 0 || m.Media.DocumentAttributes != nil || m.Text != "[temporary media]" || len(m.Entities) != 0 || m.Poll != nil || m.Location != nil {
			t.Fatalf("temporary media payload or policy leaked: %+v media=%+v", m, m.Media)
		}
	}
	return m
}

func TestWriteReturnSecurityAllSendKinds(t *testing.T) {
	for _, kind := range writeReturnSendKinds {
		for _, envelope := range []string{"updates", "combined", "short", "short_sent"} {
			if kind == "forward" && envelope == "short_sent" {
				continue // forwardMessages has no single-message short-sent receipt.
			}
			for _, channel := range []bool{false, true} {
				peer := "user"
				ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
				if channel {
					peer, ref = "broadcast", engine.PeerRef{Type: engine.PeerChannel, ID: 40}
				}
				for _, policy := range []string{"reported", "omitted", "longer"} {
					t.Run(kind+"/"+envelope+"/"+peer+"/"+policy, func(t *testing.T) {
						e, db, ctx := sendAllFixture(t)
						raw := sendAllRaw(kind, ref, 504)
						raw.Date = int(time.Now().Unix())
						date := time.Unix(int64(raw.Date), 0)
						key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
						mediaTTL := 0
						if policy == "reported" {
							raw.Noforwards, raw.TTLPeriod = true, 3600
							if writeReturnSetMediaTTL(raw, 15) {
								mediaTTL = 15
							}
							// Short-sent has no noforwards field; its protection comes
							// from the persisted destination, not invented message data.
							if envelope == "short_sent" {
								if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Protected: true}}}}); err != nil {
									t.Fatal(err)
								}
							}
						} else {
							seed := sendAllRaw(kind, ref, raw.ID)
							seed.Date, seed.Noforwards, seed.TTLPeriod = raw.Date, true, 3600
							if writeReturnSetMediaTTL(seed, 15) {
								mediaTTL = 15
							}
							m, ok := convertMessage("primary", seed)
							if !ok {
								t.Fatal("invalid seed")
							}
							if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
								t.Fatal(err)
							}
							if policy == "longer" {
								raw.TTLPeriod = 7200
								writeReturnSetMediaTTL(raw, 60)
								// Even a newer server revision and shifted creation
								// date must not extend an existing lifecycle deadline.
								raw.Date += 100
								raw.SetEditDate(raw.Date + 1)
							}
						}
						calls, returned := writeReturnRPC(t, e, kind, envelope, raw)
						writeReturnSend(t, e, ctx, kind, ref, calls)
						writeReturnAssertSecurity(t, db, ctx, key, true, 3600, date.Add(time.Hour), mediaTTL)
						_, err := e.Forward(ctx, engine.ForwardRequest{From: ref, To: engine.PeerRef{Type: engine.PeerChannel, ID: 40}, IDs: []int{key.ID}, WriteOptions: engine.WriteOptions{DryRun: true}})
						if !errors.Is(err, engine.ErrProtectedContent) || *calls != 1 {
							t.Fatalf("protected result became forwardable: err=%v calls=%d", err, *calls)
						}
						if envelope != "short_sent" {
							if err := e.handle(ctx, *returned); err != nil {
								t.Fatal(err)
							}
							writeReturnAssertSecurity(t, db, ctx, key, true, 3600, date.Add(time.Hour), mediaTTL)
						}
					})
				}
			}
		}
	}
}

func TestWriteReturnEditPartialSecurityAcrossRevisions(t *testing.T) {
	for _, envelope := range []string{"updates", "combined", "short"} {
		for _, peer := range []string{"user", "broadcast"} {
			for _, revision := range []string{"stale", "equal", "newer"} {
				for _, policy := range []string{"protected", "ttl", "expiry_only", "media_ttl"} {
					t.Run(envelope+"/"+peer+"/"+revision+"/"+policy, func(t *testing.T) {
						e, db, ctx := sendAllFixture(t)
						ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
						if peer == "broadcast" {
							ref = engine.PeerRef{Type: engine.PeerChannel, ID: 40}
						}
						seed := sendAllRaw("document", ref, 504)
						seed.Date = int(time.Now().Unix())
						seed.SetEditDate(seed.Date + 10)
						switch policy {
						case "protected":
							seed.Noforwards = true
						case "ttl":
							seed.TTLPeriod = 3600
						case "media_ttl":
							writeReturnSetMediaTTL(seed, 15)
						}
						m, ok := convertMessage("primary", seed)
						if !ok {
							t.Fatal("invalid seed")
						}
						if policy == "expiry_only" {
							expires := m.Date.Add(time.Hour)
							m.ExpiresAt = &expires
						}
						if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
							t.Fatal(err)
						}
						// A sparse edit acknowledges text, not removal of previously
						// known protection, timer, expiry or temporary attachment.
						raw := sendAllRaw("text", ref, seed.ID)
						raw.Date = seed.Date + 100
						raw.Message = "replacement"
						edit := seed.EditDate
						if revision == "stale" {
							edit--
						} else if revision == "newer" {
							edit++
						}
						raw.SetEditDate(edit)
						calls, returned := writeReturnRPC(t, e, "edit", envelope, raw)
						r := engine.EditRequest{Key: m.Key, Text: "replacement", WriteOptions: engine.WriteOptions{DryRun: true}}
						preview, err := e.Edit(ctx, r)
						if err != nil || preview.Preview == nil || *calls != 0 {
							t.Fatalf("edit preview: %+v err=%v calls=%d", preview, err, *calls)
						}
						r.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
						result, err := e.Edit(ctx, r)
						if err != nil || !result.Done || result.Affected != 1 || *calls != 1 {
							t.Fatalf("edit: %+v err=%v calls=%d", result, err, *calls)
						}
						for replay := 0; replay < 2; replay++ {
							got, found, err := db.Message(ctx, m.Key)
							if err != nil || !found || got.Protected != m.Protected || got.TTLPeriod != m.TTLPeriod || !got.Date.Equal(m.Date) || (got.ExpiresAt == nil) != (m.ExpiresAt == nil) || (m.ExpiresAt != nil && !got.ExpiresAt.Equal(*m.ExpiresAt)) {
								t.Fatalf("partial edit weakened durable security: got=%+v want=%+v found=%v err=%v", got, m, found, err)
							}
							if policy == "media_ttl" {
								if got.Media == nil || got.Media.TTLSeconds != 15 || got.Media.Downloadable || len(got.Media.Remote) > 0 || got.Media.Filename != "" || got.Text != "[temporary media]" {
									t.Fatalf("partial edit restored temporary payload: %+v media=%+v", got, got.Media)
								}
							} else if revision == "newer" && got.Text != "replacement" {
								t.Fatalf("security retention discarded a genuine newer edit: %+v", got)
							}
							if err := e.handle(ctx, *returned); err != nil {
								t.Fatal(err)
							}
						}
						_, err = e.Forward(ctx, engine.ForwardRequest{From: ref, To: engine.PeerRef{Type: engine.PeerChannel, ID: 40}, IDs: []int{m.Key.ID}, WriteOptions: engine.WriteOptions{DryRun: true}})
						if !errors.Is(err, engine.ErrProtectedContent) || *calls != 1 {
							t.Fatalf("edit made restricted source forwardable: err=%v calls=%d", err, *calls)
						}
					})
				}
			}
		}
	}
}

func TestWriteReturnSendExpiryOnlySurvivesPartialReceipt(t *testing.T) {
	for _, kind := range writeReturnSendKinds {
		for _, envelope := range []string{"updates", "short_sent"} {
			if kind == "forward" && envelope == "short_sent" {
				continue
			}
			t.Run(kind+"/"+envelope, func(t *testing.T) {
				e, db, ctx := sendAllFixture(t)
				ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
				raw := sendAllRaw(kind, ref, 504)
				raw.Date = int(time.Now().Unix())
				m, ok := convertMessage("primary", raw)
				if !ok {
					t.Fatal("invalid seed")
				}
				expires := m.Date.Add(time.Hour)
				m.ExpiresAt = &expires
				if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
					t.Fatal(err)
				}
				raw.Date += 100
				raw.SetEditDate(raw.Date + 1)
				calls, _ := writeReturnRPC(t, e, kind, envelope, raw)
				writeReturnSend(t, e, ctx, kind, ref, calls)
				writeReturnAssertSecurity(t, db, ctx, m.Key, false, 0, expires, 0)
			})
		}
	}
}

func TestWriteReturnExpiredShortMediaFailureRetainsAcceptance(t *testing.T) {
	e, db, ctx := sendAllFixture(t)
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	raw := sendAllRaw("photo", ref, 504)
	raw.Media = &tg.MessageMediaPhoto{TTLSeconds: 1}
	calls, _ := writeReturnRPC(t, e, "photo", "short_sent", raw)
	path := sendAllUpload(t)
	preview, err := sendAllCall(e, ctx, "photo", ref, path, engine.WriteOptions{DryRun: true})
	if err != nil || preview.Preview == nil {
		t.Fatalf("preview: %+v err=%v", preview, err)
	}
	localErr := errors.New("synthetic expired-media deletion failure")
	e.cfg.Store = mediaSendFailStore{Persister: db, err: localErr}
	result, err := sendAllCall(e, ctx, "photo", ref, path, engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint})
	key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
	if !errors.Is(err, localErr) || !result.Done || result.Affected != 1 || len(result.Messages) != 1 || result.Messages[0] != key || *calls != 1 || !e.held.Load() || !strings.Contains(result.Note, "do not blindly retry") {
		t.Fatalf("expired-media failure lost acceptance/checkpoint hold: result=%+v err=%v calls=%d held=%v", result, err, *calls, e.held.Load())
	}
	if _, found, err := db.Message(ctx, key); err != nil || found {
		t.Fatalf("failed expired-media deletion synthesized a message: found=%v err=%v", found, err)
	}
}

func TestWriteReturnExpiredShortMediaNeverResurrects(t *testing.T) {
	for _, kind := range writeReturnMediaKinds {
		for _, envelope := range []string{"updates", "combined", "short", "short_sent"} {
			t.Run(kind+"/"+envelope, func(t *testing.T) {
				e, db, ctx := sendAllFixture(t)
				ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
				raw := sendAllRaw(kind, ref, 504)
				valid := sendAllRaw(kind, ref, raw.ID)
				if kind == "photo" {
					raw.Media = &tg.MessageMediaPhoto{TTLSeconds: 1}
				} else {
					raw.Media = &tg.MessageMediaDocument{TTLSeconds: 1}
				}
				calls, _ := writeReturnRPC(t, e, kind, envelope, raw)
				result := writeReturnSend(t, e, ctx, kind, ref, calls)
				key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
				if envelope != "short" && (len(result.Messages) != 1 || result.Messages[0] != key) {
					t.Fatalf("lost accepted receipt: %+v", result)
				}
				if m, found, err := db.Message(ctx, key); err != nil || found {
					t.Errorf("expired server media retained as upload: found=%v message=%+v err=%v", found, m, err)
				}
				// A deletion tombstone, not merely skipping this response, must
				// prevent a delayed normal snapshot from restoring the payload.
				if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: valid}}); err != nil {
					t.Fatal(err)
				}
				if m, found, err := db.Message(ctx, key); err != nil || found {
					t.Fatalf("expired media resurrected by replay: found=%v message=%+v err=%v", found, m, err)
				}
			})
		}
	}
}
