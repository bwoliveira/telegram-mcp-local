package mcp

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"strconv"
)

// TranscriptReader lets the account-scoped Telegram store provide cached text
// without transcribing, downloading, or refreshing media as a side effect.
type TranscriptReader interface {
	ReadTranscript(context.Context, engine.MessageKey) (state.Transcript, bool, error)
}

func (s *Server) attachReadTranscripts(ctx context.Context, acc engine.AccountID, msgs []message) error {
	reader, ok := s.readStore.(TranscriptReader)
	for i := range msgs {
		if msgs[i].MediaType != "audio" && msgs[i].MediaType != "voice" {
			continue
		}
		if ok {
			p, err := parsePeer(msgs[i].ChatJID)
			if err != nil {
				return err
			}
			id, err := strconv.Atoi(msgs[i].ID)
			if err != nil {
				return err
			}
			tr, found, err := reader.ReadTranscript(ctx, engine.MessageKey{Account: acc, Peer: p, ID: id})
			if err != nil {
				return err
			}
			if found {
				msgs[i].Transcript = &tr
			}
		} else if s.state != nil {
			// Compatibility only: state belongs to this server's account slot. It is
			// never searched independently of a currently retained Telegram message.
			if tr, err := s.state.Transcript(ctx, msgs[i].ChatJID, msgs[i].ID); err == nil {
				msgs[i].Transcript = &tr
			}
		}
	}
	return nil
}
