package mcp

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

// Telegram resolution and mention entities belong to the engine contract.
// Non-user and malformed identifiers must not be accepted as mentions.
func TestMentionsUseTypedUsers(t *testing.T) {
	s := New(Config{})
	m, fail := s.mentions(context.Background(), []string{"user:12", "user:34"})
	if fail != nil || len(m) != 2 || m[0].User.ID != 12 || m[1].User.Type != engine.PeerUser {
		t.Fatalf("mentions=%v fail=%v", m, fail)
	}
	for _, invalid := range []string{"chat:12", "channel:12", "123@example.invalid", "user:0"} {
		if _, fail := s.mentions(context.Background(), []string{invalid}); fail == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestIsClosing(t *testing.T) {
	own := []string{"5511999999999"}
	for text, want := range map[string]bool{"Obrigado!": true, "ok.": true, "👍": true, "@5511999999999 valeu": true,
		"obrigado, mas e o boleto?": false, "Pode me ligar?": false} {
		if got := isClosing(index.Row{Text: text}, own); got != want {
			t.Errorf("isClosing(%q) = %v, want %v", text, got, want)
		}
	}
	if !isClosing(index.Row{MediaType: "sticker"}, own) || isClosing(index.Row{MediaType: "image"}, own) {
		t.Error("a sticker closes, a photo does not")
	}
}

func TestShaping(t *testing.T) {
	if _, err := newShaping(arguments{Fields: []string{"bogus"}}, messageFields); err == nil {
		t.Error("an unknown field must be refused")
	}
	sh, err := newShaping(arguments{Fields: []string{"id", "text"}, MaxContentChars: 5}, messageFields)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []message{{ID: "A", Text: "uma mensagem longa", ChatJID: "x"}, {ID: "B", Text: "curta"},
		{ID: "C", MediaType: "audio", Transcript: &state.Transcript{Text: "um áudio comprido", Raw: "um audio comprido"}}}
	out := sh.messages(msgs).([]map[string]any)
	if out[0]["text"] != "uma m…" || out[0]["text_truncated"] != true || out[0]["chat_id"] != nil {
		t.Errorf("first = %v", out[0])
	}
	if _, cut := out[1]["text_truncated"]; cut || out[1]["text"] != "curta" {
		t.Errorf("second = %v", out[1])
	}
	if out[2]["text_truncated"] != true {
		t.Errorf("a cut transcript is marked: %v", out[2])
	}
	if msgs[2].Transcript.Text != "um áu…" {
		t.Errorf("transcript = %q", msgs[2].Transcript.Text)
	}
}
