package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// MessageContentHash identifies retained content, excluding reaction/vote state
// and server revision metadata. Attachment identity is supplied by the adapter;
// an empty identity permits comparison of the public content of legacy rows.
func MessageContentHash(m Message, identity string) string {
	type pollOption struct{ Key, Text string }
	type pollDefinition struct {
		ID                                int64
		Question                          string
		Options                           []pollOption
		Closed, Anonymous, Multiple, Quiz bool
	}
	var poll *pollDefinition
	if p := m.Poll; p != nil {
		poll = &pollDefinition{ID: p.ID, Question: p.Question, Closed: p.Closed, Anonymous: p.Anonymous, Multiple: p.Multiple, Quiz: p.Quiz, Options: make([]pollOption, 0, len(p.Options))}
		for _, option := range p.Options {
			poll.Options = append(poll.Options, pollOption{Key: option.Key, Text: option.Text})
		}
	}
	content := struct {
		Text     string
		Entities []Entity
		Media    *Media
		Location *Location
		Poll     *pollDefinition
		ReplyTo  *ReplyRef
		Forward  *ForwardInfo
		Service  *ServiceAction
		Identity string
	}{m.Text, m.Entities, m.Media, m.Location, poll, m.ReplyTo, m.Forward, m.Service, identity}
	encoded, _ := json.Marshal(content)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
