package mcp

import (
	"fmt"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func partialWriteError(out any, err error) map[string]any {
	body := engineErrorFields(err)
	// Typed metadata must survive, but its ordinary retry advice is unsafe
	// after acceptance: replaying the whole write could duplicate side effects.
	warning := "Telegram accepted at least part of the write; inspect result (affected, messages, per_user and note); do not blindly retry the entire operation. Reconcile accepted steps first; retry only remaining, unaccepted steps with a fresh preview and human approval"
	if _, limited := body["flood_wait_until"]; limited {
		warning += ", after flood_wait_until"
	}
	body["error"] = fmt.Sprintf("%s. Original error: %v", warning, err)
	body["partial"], body["result"] = true, out
	return textResult(body, true)
}

func writeHasEvidence(out any) bool {
	var r engine.WriteResult
	switch v := out.(type) {
	case engine.WriteResult:
		r = v
	case engine.ParticipantsResult:
		if len(v.PerUser) > 0 {
			return true
		}
		r = v.WriteResult
	case engine.InviteLinkResult:
		if v.Revoked || v.Link != "" {
			return true
		}
		r = v.WriteResult
	default:
		return false
	}
	return r.Done || r.Affected > 0 || len(r.Messages) > 0
}
