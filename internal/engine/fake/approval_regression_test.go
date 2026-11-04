package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestIdenticalPreviewDoesNotReviveConsumedApproval(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
	ctx := context.Background()
	r := engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "same payload", WriteOptions: engine.WriteOptions{DryRun: true}}
	first, err := f.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: first.Preview.Fingerprint}
	if _, err := f.SendText(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{DryRun: true}
	second, err := f.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: first.Preview.Fingerprint}
	if _, err := f.SendText(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("identical preview revived consumed approval: %v", err)
	}
	if first.Preview.Fingerprint == second.Preview.Fingerprint {
		t.Fatal("distinct preview generations share a fingerprint")
	}
	if len(f.Writes()) != 1 {
		t.Fatalf("old approval appended another write: %d", len(f.Writes()))
	}
	r.ExpectFingerprint = second.Preview.Fingerprint
	if _, err := f.SendText(ctx, r); err != nil {
		t.Fatalf("fresh generation rejected: %v", err)
	}
	if len(f.Writes()) != 2 {
		t.Fatalf("fresh generation did not append exactly one write: %d", len(f.Writes()))
	}
}

func TestIdenticalPreviewDoesNotRenewOriginalExpiry(t *testing.T) {
	now := fake.Epoch()
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite, Clock: func() time.Time { return now }})
	ctx := context.Background()
	r := engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "same payload", WriteOptions: engine.WriteOptions{DryRun: true}}
	first, err := f.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(9 * time.Minute)
	second, err := f.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: first.Preview.Fingerprint}
	if _, err := f.SendText(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("identical preview renewed original expiry: %v", err)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("expired approval appended a write")
	}
	r.ExpectFingerprint = second.Preview.Fingerprint
	if _, err := f.SendText(ctx, r); err != nil {
		t.Fatalf("newer, unexpired generation rejected: %v", err)
	}
}

func TestApprovalGenerationsAreDeterministicAndConfirmDoesNotIssue(t *testing.T) {
	ctx := context.Background()
	cfg := engine.Config{Account: "test", Mode: engine.ModeReadWrite}
	f, twin := newFake(t, cfg), newFake(t, cfg)
	r := engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "deterministic payload", WriteOptions: engine.WriteOptions{DryRun: true}}
	first, err := f.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	twinFirst, err := twin.SendText(ctx, r)
	if err != nil || first.Preview.Fingerprint != twinFirst.Preview.Fingerprint {
		t.Fatalf("first generation is not deterministic: %v", err)
	}
	// Mismatched, successful and idempotent confirmations must not allocate a
	// generation. The twin deliberately performs none of these calls.
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: first.Preview.Fingerprint}
	r.Text = "changed"
	if _, err := f.SendText(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("changed payload accepted: %v", err)
	}
	r.Text = "deterministic payload"
	r.IdempotencyKey = "generation-check"
	if _, err := f.SendText(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SendText(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{DryRun: true}
	second, err := f.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	twinSecond, err := twin.SendText(ctx, r)
	if err != nil || second.Preview.Fingerprint != twinSecond.Preview.Fingerprint {
		t.Fatalf("confirmation allocated approval or generations are not deterministic: %v", err)
	}
	if first.Preview.Fingerprint == second.Preview.Fingerprint {
		t.Fatal("preview generation did not advance")
	}
	// A new preview must not invalidate an earlier outstanding approval either.
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: twinFirst.Preview.Fingerprint}
	if _, err := twin.SendText(ctx, r); err != nil {
		t.Fatalf("new preview invalidated earlier outstanding approval: %v", err)
	}
}
