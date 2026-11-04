package mcp

import (
	"context"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// ReadStore is the local, account-scoped snapshot seam for read-only tools.
// It is deliberately separate from engine.Engine: none of these methods may
// refresh, backfill, call Telegram, publish events or perform Telegram writes.
// Implementations return owned snapshots and honor cancellation. An empty or
// different account must fail with engine.ErrAccountMismatch, never select a
// default account. Filtering, ordering for presentation and pagination belong
// to the handler, not this interface.
//
// A fake engine can satisfy this interface by type assertion. A persistent
// store is supplied separately by the MCP configuration; implementing it does
// not require importing mcp (Go interfaces are structural).
type ReadStore interface {
	// ReadPeers includes every seen peer, not only contacts or open dialogs.
	ReadPeers(ctx context.Context, account engine.AccountID) ([]engine.Peer, error)
	ReadDialogs(ctx context.Context, account engine.AccountID) ([]engine.Dialog, error)
	// ReadMessages excludes deleted and expired content at read time, even when
	// the TTL janitor has not run. Message keys retain their account and peer.
	ReadMessages(ctx context.Context, account engine.AccountID) ([]engine.Message, error)
	// ReadGroup returns the existing snapshot without refreshing SnapshotAt.
	// Retained history remains readable when peer access or authentication is lost.
	ReadGroup(ctx context.Context, account engine.AccountID, peer engine.PeerRef) (engine.Group, error)
	// ReadCoverage uses a nil peer for the whole account. Complete requires
	// proof: missing/incomplete cursors, open gaps, held checkpoints or unknown
	// coverage must never be represented as complete. Oldest is a known local
	// history boundary, not proof by itself; nil means no safe boundary is known.
	ReadCoverage(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) (complete bool, oldest *time.Time, err error)
}
