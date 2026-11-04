package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestForwardApprovalBindsSecondSourceSnapshot(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
	ctx := context.Background()
	r := engine.ForwardRequest{From: fake.PrivatePeer, IDs: []int{1, 2}, To: fake.BasicGroupPeer, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := f.Forward(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	var changed engine.Message
	for _, m := range f.Snapshot().Messages {
		if m.Key == key(fake.PrivatePeer, 2) {
			changed = m
		}
	}
	if changed.Key.ID == 0 {
		t.Fatal("second source fixture missing")
	}
	edited := fake.Epoch().Add(time.Minute)
	changed.Text = "second source changed after approval"
	changed.EditDate = &edited
	if err := f.Publish(ctx, engine.Batch{Edits: []engine.Message{changed}}); err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
	if _, err := f.Forward(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("changed second source accepted with old approval: %v", err)
	}
	if len(f.Writes()) != 0 {
		t.Fatalf("stale forward approval appended writes: %d", len(f.Writes()))
	}
	for _, m := range f.Snapshot().Messages {
		if m.Key.Peer == r.To && m.Out {
			t.Fatal("stale forward approval inserted an outgoing message")
		}
	}
	r.WriteOptions = engine.WriteOptions{DryRun: true}
	fresh, err := f.Forward(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: fresh.Preview.Fingerprint}
	result, err := f.Forward(ctx, r)
	if err != nil || !result.Done || len(result.Messages) != 2 || len(f.Writes()) != 1 {
		t.Fatalf("fresh forward approval: %+v, %v", result, err)
	}
	for _, m := range f.Snapshot().Messages {
		if m.Key == result.Messages[1] && m.Text == changed.Text {
			return
		}
	}
	t.Fatal("fresh forward did not send the newly approved second source")
}

func TestForwardPreviewShowsAllSourceSnapshots(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
	r := engine.ForwardRequest{From: fake.PrivatePeer, IDs: []int{1, 2, 4}, To: fake.BasicGroupPeer, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := f.Forward(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	// The frozen Preview contract has only one Quoted slot. The forward action
	// therefore exposes its complete, ordered source snapshots as JSON in Text.
	var shown []engine.Message
	if err := json.Unmarshal([]byte(p.Preview.Text), &shown); err != nil {
		t.Fatalf("forward preview does not expose a source snapshot payload: %v", err)
	}
	if len(shown) != len(r.IDs) {
		t.Fatalf("preview shows %d of %d sources", len(shown), len(r.IDs))
	}
	for i, id := range r.IDs {
		if shown[i].Key != key(r.From, id) {
			t.Fatalf("preview source %d is out of order: %+v", id, shown[i].Key)
		}
		for _, source := range f.Snapshot().Messages {
			if source.Key == key(r.From, id) && !reflect.DeepEqual(shown[i], source) {
				t.Fatalf("source %d snapshot is incomplete: %+v", id, shown[i])
			}
		}
	}
}
