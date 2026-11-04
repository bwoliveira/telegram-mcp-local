package index

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAudioContextPrompt(t *testing.T) {
	c := AudioContext{ChatName: "Felipe Cardoso - Lancer", Speaker: "Felipe Cardoso - Lancer", People: []string{"Felipe Cardoso - Lancer"},
		Before: []ContextLine{{At: time.Now(), Sender: "Felipe", Text: "Vai ter um da outliers no Capuava"}, {Sender: "Eu", Text: "Me manda o do velocitta"}}}
	p := c.Prompt()
	for _, want := range []string{"Felipe Cardoso - Lancer", "Capuava", "velocitta"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt %q lacks %q", p, want)
		}
	}
	if !strings.HasPrefix(p, "Felipe Cardoso - Lancer") {
		t.Errorf("names should lead the prompt: %q", p)
	}
	long := AudioContext{ChatName: "x"}
	for i := 0; i < 100; i++ {
		long.Before = append(long.Before, ContextLine{Text: strings.Repeat("palavra ", 10)})
	}
	if n := len(long.Prompt()); n > 800 {
		t.Errorf("prompt is %d characters; Whisper reads only a few hundred", n)
	}
}

func TestIsIdentifier(t *testing.T) {
	for in, want := range map[string]bool{"user:42": true, "chat:42": true, "channel:42": true, "@alice": true, "+55 (11) 98765-4321": true, "": true, "Mãe": false, "Time 2": false} {
		if got := IsIdentifier(in); got != want {
			t.Errorf("IsIdentifier(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNamesTelegramIdentityAndAmbiguousNames(t *testing.T) {
	x := store(t, time.Now())
	ctx := context.Background()
	for input, want := range map[string]string{" user:42 ": "user:42", "chat:42": "chat:42", "channel:42": "channel:42", "Alice": "Alice", "42": "42"} {
		if got := x.Names.Canonical(ctx, input); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", input, got, want)
		}
	}
	if x.Names.Same(ctx, "user:42", "chat:42") || x.Names.Same(ctx, "chat:42", "channel:42") {
		t.Error("different Telegram peer types must not merge numeric IDs")
	}
	if !x.Names.Same(ctx, " user:42 ", "user:42") {
		t.Error("surrounding whitespace must not change identity")
	}
	if got := x.Names.Name(ctx, "user:42"); got != "user:42" {
		t.Errorf("unknown user fallback = %q", got)
	}
	if got := x.Names.Known(ctx, "user:42"); got != "" {
		t.Errorf("unknown user has name %q", got)
	}
}

func TestNamesUsesOnlyLocalTypedPeers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT);
		CREATE TABLE contact_aliases (jid TEXT PRIMARY KEY, alias TEXT);
		INSERT INTO chats VALUES ('user:42', 'Alice'), ('chat:42', 'Alice'), ('channel:42', 'News'), ('user:43', 'Alice'), ('user:44', 'user:44'), ('untyped', 'Ignore');
		INSERT INTO contact_aliases VALUES ('user:42', 'My Alice'), ('user:45', 'Alice'), ('chat:42', 'Alice');`)
	if err != nil {
		t.Fatal(err)
	}
	// An adjacent file is not an account or identity source for this index.
	adjacent := filepath.Join(dir, "session.db")
	if err := os.WriteFile(adjacent, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	x, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	ctx := context.Background()
	for peer, want := range map[string]string{"user:42": "My Alice", "chat:42": "Alice", "channel:42": "News", "user:43": "Alice", "user:45": "Alice"} {
		if got := x.Names.Name(ctx, peer); got != want {
			t.Errorf("Name(%q) = %q, want %q", peer, got, want)
		}
	}
	if got := x.Names.Known(ctx, "user:44"); got != "" {
		t.Errorf("identifier placeholder treated as name: %q", got)
	}
	if got := x.Names.Canonical(ctx, "Alice"); got != "Alice" {
		t.Errorf("ambiguous display name resolved to %q", got)
	}
	if x.Names.Same(ctx, "user:43", "user:45") {
		t.Error("identical display names must not merge distinct users")
	}
	want := []Contact{{JID: "user:43", Name: "Alice"}, {JID: "user:45", Name: "Alice"}, {JID: "user:42", Name: "My Alice"}}
	if got := x.Names.Contacts(ctx, "alice", 10); !reflect.DeepEqual(got, want) {
		t.Errorf("Contacts = %+v, want %+v", got, want)
	}
	if got := x.Names.Contacts(ctx, "42", 10); len(got) != 1 || got[0].JID != "user:42" || got[0].Phone != "" {
		t.Errorf("typed numeric ID search must not invent phone numbers: %+v", got)
	}
	if got := x.Names.Contacts(ctx, "", 0); len(got) != 0 {
		t.Errorf("zero limit returned %+v", got)
	}
	if got := x.Names.Contacts(ctx, "", -1); len(got) != 0 {
		t.Errorf("negative limit returned %+v", got)
	}
	if got := x.Names.Self(ctx); got != "" {
		t.Errorf("index invented an account name: %q", got)
	}
	if first, second := x.Names.Own(ctx); first != "" || second != "" {
		t.Errorf("index invented an account identity: %q, %q", first, second)
	}
	// On Linux, verify the resolver did not retain any descriptor for the
	// adjacent session file, even when a read-only open would leave it unchanged.
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		for _, entry := range entries {
			if target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name())); err == nil && target == adjacent {
				t.Errorf("resolver opened an external session file: %s", target)
			}
		}
	}
	if got, err := os.ReadFile(adjacent); err != nil || string(got) != "not a database" {
		t.Errorf("adjacent file changed: %q, %v", got, err)
	}
}
