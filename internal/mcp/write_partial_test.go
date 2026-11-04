package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestWritePreservesPartialAcceptanceAndErrorMetadata(t *testing.T) {
	until := fake.Epoch().Add(time.Minute)
	cases := []struct {
		name     string
		result   any
		err      error
		affected float64
	}{
		{"group title accepted description failed", engine.WriteResult{Affected: 1, Note: "title accepted; description refused"}, engine.ErrPermission, 1},
		{"participants mixed outcomes", engine.ParticipantsResult{WriteResult: engine.WriteResult{Affected: 1}, PerUser: map[string]string{"user:1001": "ok", "user:1002": "CHAT_ADMIN_REQUIRED"}}, engine.ErrPermission, 1},
		{"participant remove left banned", engine.ParticipantsResult{PerUser: map[string]string{"user:1001": "removed but unban failed"}}, engine.ErrPermission, 0},
		{"forward accepted before flood", engine.WriteResult{Affected: 1, Messages: []engine.MessageKey{{Account: "primary", Peer: fake.SelfPeer, ID: 22}}}, &engine.FloodWaitError{Until: until}, 1},
		{"invite revoked before failure", engine.InviteLinkResult{Revoked: true, Link: "https://t.me/+synthetic", WriteResult: engine.WriteResult{Affected: 1}}, engine.ErrPermission, 1},
		{"incomplete without aggregate error", engine.WriteResult{Affected: 1, Note: "one of two changes accepted"}, nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := parityServer(t)
			const token = "synthetic-partial-preview"
			a := arguments{To: "me", Text: "request"}
			invoke := func(_ context.Context, o engine.WriteOptions) (any, error) {
				if o.DryRun {
					return engine.WriteResult{DryRun: true, Preview: &engine.Preview{Fingerprint: token}}, nil
				}
				return tc.result, tc.err
			}
			p := s.write(context.Background(), a, "partial-test", invoke)
			if previewFingerprint(t, p) != token {
				t.Fatal(p)
			}
			a.Confirm = true
			a.PreviewFingerprint = token
			r := s.write(context.Background(), a, "partial-test", invoke)
			body := toolBody(t, r)
			if r["isError"] != true || body["partial"] != true || body["error"] == nil {
				t.Fatalf("error hid partial acceptance: %+v", r)
			}
			result, ok := body["result"].(map[string]any)
			if !ok {
				t.Fatalf("partial result missing: %+v", body)
			}
			if tc.affected > 0 && result["affected"] != tc.affected {
				t.Fatal("lost accepted count", result)
			}
			switch tc.result.(type) {
			case engine.ParticipantsResult:
				if result["per_user"] == nil {
					t.Fatal("lost per-user outcomes", result)
				}
			case engine.InviteLinkResult:
				if result["revoked"] != true || result["link"] == nil {
					t.Fatal("lost revoked invite", result)
				}
			}
			if errors.As(tc.err, new(*engine.FloodWaitError)) && body["flood_wait_until"] != until.Format(time.RFC3339) {
				t.Fatal("lost retry time", body)
			}
			// Confirmation is consumed even when the remote operation only partly
			// succeeded. Retrying requires a fresh preview of the changed state.
			retry := s.write(context.Background(), a, "partial-test", func(context.Context, engine.WriteOptions) (any, error) {
				t.Fatal("partial approval replayed")
				return nil, nil
			})
			if retry["isError"] != true {
				t.Fatal(retry)
			}
			if len(f.Writes()) != 0 {
				t.Fatal("synthetic callback sent Telegram writes", f.Writes())
			}
		})
	}
}

func TestWriteFailureWithoutEvidenceIsNotClaimedPartial(t *testing.T) {
	s, _, _ := parityServer(t)
	r := s.write(context.Background(), arguments{}, "test", func(context.Context, engine.WriteOptions) (any, error) {
		return engine.WriteResult{}, engine.ErrPermission
	})
	body := toolBody(t, r)
	if r["isError"] != true || body["partial"] != nil || body["result"] != nil {
		t.Fatal(r)
	}
}
