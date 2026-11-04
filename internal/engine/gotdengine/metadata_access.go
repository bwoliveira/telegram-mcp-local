package gotdengine

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tgerr"
)

// persistMetadataAccessLoss reconciles definitive full-chat/full-channel RPC
// evidence through the existing transactional lifecycle path. Access, channel
// checkpoint removal, no_access cursor and coverage gap commit together; apply
// publishes only committed changes and fences checkpoints if persistence fails.
// CHANNEL_INVALID can mean a stale hash or malformed input, not lost membership.
func (e *Engine) persistMetadataAccessLoss(ctx context.Context, peer engine.PeerRef, rpcErr error) error {
	for _, reason := range []string{"CHANNEL_PRIVATE", "CHAT_FORBIDDEN", "USER_BANNED_IN_CHANNEL"} {
		if !tgerr.Is(rpcErr, reason) {
			continue
		}
		state := engine.AccessLost
		if reason == "USER_BANNED_IN_CHANNEL" {
			state = engine.AccessBanned
		}
		_, err := e.apply(ctx, engine.Batch{
			Account: e.cfg.Account,
			Source:  engine.SourceRefresh,
			Access:  []engine.AccessChange{{Peer: peer, State: state, Reason: reason}},
		})
		return err
	}
	return nil
}
