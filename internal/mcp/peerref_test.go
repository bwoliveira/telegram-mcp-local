package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestResolvePeerForms(t *testing.T) {
	e, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: e})
	a, _ := e.Account(context.Background())
	self := engine.PeerRef{Type: engine.PeerUser, ID: a.SelfID}
	for _, input := range []string{"me", "self", "saved", " Me "} {
		p, err := s.resolvePeer(context.Background(), input)
		if err != nil || p != self {
			t.Fatalf("%q: %v %v", input, p, err)
		}
	}
	for _, input := range []string{"@fake_channel", "https://t.me/fake_channel", "t.me/fake_channel/7", "Synthetic channel", "channel:2002", "2002"} {
		p, err := s.resolvePeer(context.Background(), input)
		if err != nil || p != fake.ChannelPeer {
			t.Fatalf("%q: %v %v", input, p, err)
		}
	}
	for _, input := range []string{"", "user:01", "channel:-1", "channel:0", "user:1:2", "https://evil.example/t.me/fake_channel", "t.me/+invite", "@bad/name"} {
		if _, err := s.resolvePeer(context.Background(), input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	if _, err := s.resolvePeer(context.Background(), "Synthetic"); err == nil || !strings.Contains(err.Error(), "channel:") || !strings.Contains(err.Error(), "chat:") {
		t.Fatalf("ambiguity lacks candidates: %v", err)
	}
}

type peerResolutionEngine struct {
	engine.Engine
	calls  []engine.ResolveRequest
	remote engine.Peer
}

func (e *peerResolutionEngine) Resolve(_ context.Context, r engine.ResolveRequest) ([]engine.Candidate, error) {
	e.calls = append(e.calls, r)
	if !r.Network {
		return nil, nil
	}
	return []engine.Candidate{{Peer: e.remote, Exact: true, Source: "username"}}, nil
}

func TestResolvePeerLocalPriorityAndAmbiguity(t *testing.T) {
	f, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := f.Account(context.Background())
	remote := engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel, ID: 9000}, Username: "remote_only"}
	e := &peerResolutionEngine{Engine: f, remote: remote}
	local := &peopleTestStore{ReadStore: f.(*fake.Controller), override: true, peers: []engine.Peer{
		{Ref: fake.BasicGroupPeer, Title: "Me"},
		{Ref: fake.ChannelPeer, Title: "Saved"},
		{Ref: fake.PrivatePeer, FirstName: "Local", Username: "local_user", Usernames: []string{"local_alias"}},
		{Ref: fake.SupergroupPeer, Title: "Local committee", Username: "remote_only_extra"},
		{Ref: engine.PeerRef{Type: engine.PeerChat, ID: 7001}, Title: "Equipe: Financeiro"},
		{Ref: engine.PeerRef{Type: engine.PeerChat, ID: 7002}, Title: "Dev / Ops"},
	}}
	s := New(Config{Engine: e, ReadStore: local})
	for _, input := range []string{"me", "saved", "self"} {
		p, err := s.resolvePeer(context.Background(), input)
		if err != nil || p.ID != a.SelfID || p.Type != engine.PeerUser {
			t.Fatalf("alias collision %q: %v %v", input, p, err)
		}
	}
	for _, input := range []string{"@LOCAL_USER", "t.me/local_alias", "Local"} {
		p, err := s.resolvePeer(context.Background(), input)
		if err != nil || p != fake.PrivatePeer {
			t.Fatalf("local %q: %v %v", input, p, err)
		}
	}
	for input, id := range map[string]int64{"Equipe: Financeiro": 7001, "Dev / Ops": 7002} {
		p, err := s.resolvePeer(context.Background(), input)
		if err != nil || p.ID != id {
			t.Fatalf("punctuated title %q: %v %v", input, p, err)
		}
	}
	if len(e.calls) != 0 {
		t.Fatalf("local lookup called network: %v", e.calls)
	}
	p, err := s.resolvePeer(context.Background(), "https://t.me/remote_only/7")
	if err != nil || p != remote.Ref || len(e.calls) != 1 || !e.calls[0].Network || e.calls[0].Query != "@remote_only" {
		t.Fatalf("remote fallback: %v %v calls=%v", p, err, e.calls)
	}
	local.peers = append(local.peers, engine.Peer{Ref: fake.BasicGroupPeer, Title: "Local"})
	if _, err := s.resolvePeer(context.Background(), "Local"); err == nil || !strings.Contains(err.Error(), "chat:1001") || !strings.Contains(err.Error(), "user:1001") {
		t.Fatalf("ambiguous exact title: %v", err)
	}
	if _, err := s.resolvePeer(context.Background(), "1001"); err == nil {
		t.Fatal("numeric namespace ambiguity accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.resolvePeer(ctx, "user:1"); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}
