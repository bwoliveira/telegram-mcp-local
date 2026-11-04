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
)

func TestMediaLocationApproval(t *testing.T) {
	e, s := writeFixture(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		r, ok := in.(*tg.MessagesSendMediaRequest)
		if !ok {
			t.Fatalf("RPC %T", in)
		}
		geo, ok := r.Media.(*tg.InputMediaGeoPoint)
		if !ok {
			t.Fatalf("media %T", r.Media)
		}
		point := geo.GeoPoint.(*tg.InputGeoPoint)
		if point.Lat != 12 || point.Long != 34 || r.Message != "Place\nAddress" {
			t.Fatalf("request %+v", r)
		}
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 501, Date: 1000}
		return nil
	}))
	r := engine.SendLocationRequest{Peer: ref, Location: engine.Location{Latitude: 12, Longitude: 34, Title: "Place", Address: "Address"}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendLocation(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || p.Preview.Action != "send_location" {
		t.Fatalf("preview %+v calls %d", p, calls)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	result, err := e.SendLocation(ctx, r)
	if err != nil || !result.Done || calls != 1 {
		t.Fatalf("send %+v %v calls %d", result, err, calls)
	}
	m, ok, err := s.Message(ctx, result.Messages[0])
	if err != nil || !ok || m.Location == nil {
		t.Fatalf("persist %+v %v", m, err)
	}
	if _, err = e.SendLocation(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("reuse %v", err)
	}
}

func TestMediaPollApproval(t *testing.T) {
	e, _ := writeFixture(t)
	ctx := context.Background()
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		r := in.(*tg.MessagesSendMediaRequest)
		p := r.Media.(*tg.InputMediaPoll)
		if p.Poll.Question.Text != "Question?" || len(p.Poll.Answers) != 2 || p.Poll.PublicVoters || !p.Poll.MultipleChoice {
			t.Fatalf("poll %+v", p.Poll)
		}
		if string(p.Poll.Answers[0].(*tg.PollAnswer).Option) == string(p.Poll.Answers[1].(*tg.PollAnswer).Option) {
			t.Fatal("options collide")
		}
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 502, Date: 1000}
		return nil
	}))
	r := engine.SendPollRequest{Peer: engine.PeerRef{Type: engine.PeerChat, ID: 20}, Question: "Question?", Options: []string{"A", "B"}, Multiple: true, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendPoll(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || p.Preview.Action != "send_poll" {
		t.Fatal("invalid preview")
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	got, err := e.SendPoll(ctx, r)
	if err != nil || !got.Done || calls != 1 {
		t.Fatalf("%+v %v calls %d", got, err, calls)
	}
}

func TestMediaUploadApprovalBindsBytes(t *testing.T) {
	e, _ := writeFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	uploads, sends := 0, 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.UploadSaveFilePartRequest:
			uploads++
			if string(r.Bytes) != "first" {
				t.Fatalf("uploaded unapproved bytes %q", r.Bytes)
			}
			out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
		case *tg.MessagesSendMediaRequest:
			sends++
			doc := r.Media.(*tg.InputMediaUploadedDocument)
			if doc.MimeType != "text/plain" || len(doc.Attributes) == 0 {
				t.Fatalf("doc %+v", doc)
			}
			out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 503, Date: 1000}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))
	r := engine.SendMediaRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Kind: engine.MediaDocument, Path: path, MimeType: "text/plain", WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendMedia(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if uploads != 0 || sends != 0 {
		t.Fatal("preview used network")
	}
	if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	if _, err := e.SendMedia(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("changed bytes %v", err)
	}
	if uploads != 0 || sends != 0 {
		t.Fatal("changed bytes used network")
	}
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := e.SendMedia(ctx, r)
	if err != nil || !got.Done || uploads != 1 || sends != 1 {
		t.Fatalf("%+v %v uploads=%d sends=%d", got, err, uploads, sends)
	}
}

func TestMediaApprovalExpiryAndReadOnly(t *testing.T) {
	e, _ := writeFixture(t)
	ctx := context.Background()
	now := time.Now()
	e.cfg.Clock = func() time.Time { return now }
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return errors.New("must not call") }))
	r := engine.SendLocationRequest{Peer: engine.PeerRef{Type: engine.PeerChat, ID: 20}, Location: engine.Location{Latitude: 1}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendLocation(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	if _, err = e.SendLocation(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("expired %v", err)
	}
	if err = e.SetMode(ctx, engine.ModeReadOnly); err != nil {
		t.Fatal(err)
	}
	if _, err = e.SendMedia(ctx, engine.SendMediaRequest{}); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("readonly media %v", err)
	}
	if _, err = e.SendLocation(ctx, r); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("readonly location %v", err)
	}
	if _, err = e.SendPoll(ctx, engine.SendPollRequest{}); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("readonly poll %v", err)
	}
	if calls != 0 {
		t.Fatalf("network calls %d", calls)
	}
}
