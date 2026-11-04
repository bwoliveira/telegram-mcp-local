package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

func TestContradictoryRefreshAlwaysTightensSecurity(t *testing.T) {
	for _, peerType := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		for _, policy := range []string{"protected", "media_ttl", "expired", "future_ttl"} {
			t.Run(string(peerType)+"/"+policy, func(t *testing.T) {
				s := confOpen(t)
				m := securityMessage()
				m.Date = confTime.Add(-time.Hour)
				m.Key.Peer.Type = peerType
				m.ContentHash = "original"
				confApply(t, s, securityBatch(engine.SourceUpdate, false, m))
				incoming := m
				media := *m.Media
				media.Filename = "contradictory.txt"
				incoming.Media = &media
				incoming.ContentHash = "contradictory"
				incoming.Text = "contradictory text"
				switch policy {
				case "protected":
					incoming.Protected = true
				case "media_ttl":
					incoming.Media.TTLSeconds = 30
				case "expired":
					incoming.TTLPeriod = 1
				case "future_ttl":
					incoming.TTLPeriod = int(confTime.Sub(m.Date)/time.Second) + 3600
				}
				confApply(t, s, securityBatch(engine.SourceRefresh, false, incoming))
				got, found, err := s.Message(context.Background(), m.Key)
				if err != nil {
					t.Fatal(err)
				}
				if policy == "expired" {
					if found {
						t.Fatalf("expired contradictory refresh remains readable: %+v", got)
					}
					return
				}
				if !found {
					t.Fatal("message missing")
				}
				switch policy {
				case "protected":
					if !got.Protected || got.Media.Downloadable {
						t.Fatalf("restriction ignored: %+v", got)
					}
				case "media_ttl":
					if got.Media.TTLSeconds != 30 || got.Media.Downloadable || got.Text != "[temporary media]" {
						t.Fatalf("temporary restriction ignored: %+v", got)
					}
				case "future_ttl":
					if got.TTLPeriod != incoming.TTLPeriod || got.ExpiresAt == nil {
						t.Fatalf("TTL ignored: %+v", got)
					}
				}
			})
		}
	}
}
