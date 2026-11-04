package mcp

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// These optional local extensions keep the fake/snapshot ReadStore seam small,
// while the durable store performs aggregation and export streaming in SQLite.
type statsAggregator interface {
	StatsMessages(context.Context, engine.AccountID, store.StatsFilter, string, int) (store.StatsResult, error)
}
type statsCoverageReader interface {
	StatsCoverage(context.Context, engine.AccountID, *engine.PeerRef) (store.Coverage, error)
}

func (s *Server) statsFilter(ctx context.Context, a arguments) (store.StatsFilter, error) {
	a.ChatJID = strings.TrimSpace(a.ChatJID)
	peer, err := s.readPeer(ctx, a, false)
	if err != nil {
		return store.StatsFilter{}, err
	}
	f, err := s.readFilter(a)
	if err != nil {
		return store.StatsFilter{}, err
	}
	media := f.media
	if media == "image" {
		media = string(engine.MediaPhoto)
	}
	// T28/T29 do not take topic/migration/pagination filters.
	return store.StatsFilter{Peer: peer, Since: f.since, Until: f.until, Direction: f.direction, MediaType: media, ExcludeGroups: f.excludeGroups}, nil
}

func statsMatches(f store.StatsFilter, m engine.Message) bool {
	if f.Peer != nil && m.Key.Peer != *f.Peer {
		return false
	}
	return (readFilter{since: f.Since, until: f.Until, direction: f.Direction, media: f.MediaType, excludeGroups: f.ExcludeGroups}).matches(m)
}

// statsCoverage never fabricates an empty gap list when a snapshot adapter has
// no rich coverage API. gaps_known=false/null explicitly preserves that limit.
func (s *Server) statsCoverage(ctx context.Context, account engine.AccountID, p *engine.PeerRef) (map[string]any, error) {
	if r, ok := s.readStore.(statsCoverageReader); ok {
		c, err := r.StatsCoverage(ctx, account, p)
		if err != nil {
			return nil, err
		}
		if c.Gaps == nil {
			c.Gaps = []engine.GapMark{}
		}
		return map[string]any{"history_since": c.HistorySince, "coverage_complete": c.Complete, "partial": !c.Complete, "gaps": c.Gaps, "gaps_known": true}, nil
	}
	complete, oldest, err := s.readStore.ReadCoverage(ctx, account, p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"history_since": oldest, "coverage_complete": complete, "partial": !complete, "gaps": nil, "gaps_known": false}, nil
}

// statsMessages is the Telegram local-store implementation of message_stats.
// Integration routes message_stats here instead of the inherited index handler.
func (s *Server) statsMessages(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	f, err := s.statsFilter(ctx, a)
	if err != nil {
		return toolError("%v", err)
	}
	group := a.GroupBy
	if group == "" {
		group = "chat"
	}
	switch group {
	case "chat", "sender", "day", "month":
	default:
		return toolError("group_by must be chat, sender, day or month")
	}
	account, fail := s.peopleAccount(ctx)
	if fail != nil {
		return fail
	}
	var result store.StatsResult
	if r, ok := s.readStore.(statsAggregator); ok {
		result, err = r.StatsMessages(ctx, account, f, group, limit(a.Limit, 50))
	} else {
		var snapshot readSnapshot
		snapshot, fail = s.readSnapshot(ctx)
		if fail != nil {
			return fail
		}
		result, err = statsSnapshot(ctx, snapshot, f, group, limit(a.Limit, 50))
	}
	if err != nil {
		return engineError(err)
	}
	coverage, err := s.statsCoverage(ctx, account, f.Peer)
	if err != nil {
		return engineError(err)
	}
	if result.Buckets == nil {
		result.Buckets = []store.StatsBucket{}
	}
	coverage["group_by"] = group
	coverage["buckets"] = result.Buckets
	coverage["total"] = result.Total
	coverage["groups"] = result.Groups
	coverage["truncated"] = result.Groups > len(result.Buckets)
	coverage["source"] = "telegram_local_store"
	coverage["untrusted_content"] = UntrustedContent
	coverage["note"] = "counts synchronized local messages, not reactions or deleted messages; coverage is not a promise of complete Telegram history"
	if group == "day" || group == "month" {
		zone, _ := s.clock().In(time.Local).Zone()
		coverage["timezone"] = zone
		coverage["timezone_location"] = time.Local.String()
	}
	return textResult(coverage, false)
}

func statsSnapshot(ctx context.Context, snapshot readSnapshot, f store.StatsFilter, group string, n int) (store.StatsResult, error) {
	result := store.StatsResult{Buckets: []store.StatsBucket{}}
	buckets := map[string]*store.StatsBucket{}
	for _, m := range snapshot.messages {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !statsMatches(f, m) {
			continue
		}
		key, name := peerID(m.Key.Peer), peopleName(snapshot.peers[m.Key.Peer])
		switch group {
		case "sender":
			key, name = "", m.SenderName
			if m.Sender != nil {
				key = peerID(*m.Sender)
				if local := peopleName(snapshot.peers[*m.Sender]); local != "" {
					name = local
				}
			}
			if m.Out {
				key, name = "me", "the account itself"
			}
		case "day":
			key, name = m.Date.In(time.Local).Format("2006-01-02"), ""
		case "month":
			key, name = m.Date.In(time.Local).Format("2006-01"), ""
		}
		b := buckets[key]
		if b == nil {
			b = &store.StatsBucket{Key: key, Name: name, First: m.Date, Last: m.Date}
			buckets[key] = b
		}
		b.Count++
		if m.Date.Before(b.First) {
			b.First = m.Date
		}
		if m.Date.After(b.Last) {
			b.Last = m.Date
		}
		result.Total++
	}
	result.Groups = len(buckets)
	for _, b := range buckets {
		result.Buckets = append(result.Buckets, *b)
	}
	sort.Slice(result.Buckets, func(i, j int) bool {
		a, b := result.Buckets[i], result.Buckets[j]
		if group == "day" || group == "month" {
			return a.Key > b.Key
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Key < b.Key
	})
	if len(result.Buckets) > n {
		result.Buckets = result.Buckets[:n]
	}
	return result, nil
}
