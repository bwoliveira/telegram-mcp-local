package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// resolveMessageKey resolves only the separate human conversation input. A
// compound message identity remains strictly canonical, never a username/link.
// Validate the message token before any account, snapshot or remote lookup.
func (s *Server) resolveMessageKey(ctx context.Context, a arguments) (engine.MessageKey, error) {
	var k engine.MessageKey
	var err error
	if strings.Contains(a.MessageID, "/") {
		k, err = parseMessageKey(arguments{MessageID: a.MessageID}, "")
	} else {
		k, err = parseMessageKey(arguments{ChatJID: "user:1", MessageID: a.MessageID}, "")
	}
	if err != nil {
		return engine.MessageKey{}, err
	}
	if a.ChatJID == "" {
		if !strings.Contains(a.MessageID, "/") {
			return engine.MessageKey{}, fmt.Errorf("message_id and chat_id are required; alternatively use channel:123/456")
		}
		return k, nil
	}
	p, err := s.resolvePeer(ctx, a.ChatJID)
	if err != nil {
		return engine.MessageKey{}, err
	}
	if strings.Contains(a.MessageID, "/") && p != k.Peer {
		return engine.MessageKey{}, fmt.Errorf("message_id belongs to another chat_id")
	}
	k.Peer = p
	return k, nil
}
