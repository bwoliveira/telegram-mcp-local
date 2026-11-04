package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestDeleteRevokeRequiresConfirmationInRequest(t *testing.T) {
	ctrl, err := New(engine.Config{Mode: engine.ModeReadWrite, Clock: Epoch})
	if err != nil {
		t.Fatal(err)
	}
	f := ctrl.(*Controller)
	request := engine.DeleteRequest{
		Peer:         SelfPeer,
		IDs:          []int{8},
		Revoke:       true,
		WriteOptions: engine.WriteOptions{DryRun: true},
	}
	preview, err := f.Delete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.DryRun = false
	request.ExpectFingerprint = preview.Preview.Fingerprint
	if _, err = f.Delete(context.Background(), request); !errors.Is(err, engine.ErrInvalid) || len(f.Writes()) != 0 {
		t.Fatalf("unconfirmed revoke executed: err=%v writes=%d", err, len(f.Writes()))
	}
	request.ConfirmRevoke = true
	result, err := f.Delete(context.Background(), request)
	if err != nil || !result.Done || len(f.Writes()) != 1 {
		t.Fatalf("confirmed revoke failed: result=%+v err=%v writes=%d", result, err, len(f.Writes()))
	}
}
