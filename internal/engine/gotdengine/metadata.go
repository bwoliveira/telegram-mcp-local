package gotdengine

import (
	"encoding/base64"
	"strconv"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

func convertGeo(raw tg.GeoPointClass) *engine.Location {
	p, ok := raw.(*tg.GeoPoint)
	if !ok {
		return nil
	}
	return &engine.Location{Latitude: p.Lat, Longitude: p.Long, AccuracyMeters: p.AccuracyRadius}
}

func convertRichMedia(out *engine.Message, raw tg.MessageMediaClass) {
	switch v := raw.(type) {
	case *tg.MessageMediaPoll:
		out.Media.Type = engine.MediaPoll
		p := mediaReadConvertPoll(v.Poll, v.Results)
		out.Poll = &p
		if out.Text == "" {
			out.Text = p.Question
		}
	case *tg.MessageMediaGeo:
		out.Media.Type = engine.MediaGeo
		out.Location = convertGeo(v.Geo)
	case *tg.MessageMediaGeoLive:
		out.Media.Type = engine.MediaLiveGeo
		out.Location = convertGeo(v.Geo)
		if out.Location != nil {
			out.Location.LivePeriod = v.Period
		}
	case *tg.MessageMediaVenue:
		out.Media.Type = engine.MediaVenue
		out.Location = convertGeo(v.Geo)
		if out.Location != nil {
			out.Location.Title = v.Title
			out.Location.Address = v.Address
			out.Location.Provider = v.Provider
			out.Location.VenueID = v.VenueID
			out.Location.VenueType = v.VenueType
		}
	case *tg.MessageMediaContact:
		out.Media.Type = engine.MediaContact
		out.Media.Contact = &engine.ContactInfo{Phone: v.PhoneNumber, FirstName: v.FirstName, LastName: v.LastName, UserID: v.UserID, VCard: v.Vcard}
	case *tg.MessageMediaDice:
		out.Media.Type = engine.MediaDice
		out.Media.Dice = &engine.DiceInfo{Emoji: v.Emoticon, Value: v.Value}
	case *tg.MessageMediaStory:
		out.Media.Type = engine.MediaStory
		out.Media.Story = &engine.StoryInfo{Peer: peerRef(v.Peer), ID: v.ID, ViaMention: v.ViaMention}
	case *tg.MessageMediaWebPage:
		out.Media.Type = engine.MediaWebPage
		switch p := v.Webpage.(type) {
		case *tg.WebPage:
			out.Media.WebPage = &engine.WebPageInfo{ID: p.ID, URL: p.URL, DisplayURL: p.DisplayURL, Title: p.Title, Description: p.Description, SiteName: p.SiteName, Author: p.Author}
		case *tg.WebPagePending:
			out.Media.WebPage = &engine.WebPageInfo{ID: p.ID, URL: p.URL, Pending: true}
		}
	}
}

func convertReaction(raw tg.ReactionClass) string {
	switch v := raw.(type) {
	case *tg.ReactionEmoji:
		return v.Emoticon
	case *tg.ReactionCustomEmoji:
		return "custom:" + strconv.FormatInt(v.DocumentID, 10)
	case *tg.ReactionPaid:
		return "paid"
	default:
		return ""
	}
}

func convertReactions(key engine.MessageKey, r tg.MessageReactions) engine.ReactionUpdate {
	out := engine.ReactionUpdate{Key: key, Counts: []engine.ReactionCount{}, Min: r.Min}
	for _, v := range r.Results {
		emoji := convertReaction(v.Reaction)
		if emoji == "" {
			continue
		}
		_, chosen := v.GetChosenOrder()
		out.Counts = append(out.Counts, engine.ReactionCount{Emoji: emoji, Count: v.Count, Chosen: chosen})
	}
	// A missing recent list is partial, but an empty full snapshot clears it.
	if len(r.Results) == 0 {
		out.Recent = []engine.PeerReaction{}
	}
	for _, v := range r.RecentReactions {
		emoji := convertReaction(v.Reaction)
		if emoji == "" {
			continue
		}
		out.Recent = append(out.Recent, engine.PeerReaction{By: peerRef(v.PeerID), Emoji: emoji, Date: time.Unix(int64(v.Date), 0).UTC(), Big: v.Big, My: v.My})
	}
	return out
}

func convertPollUpdate(v *tg.UpdateMessagePoll) engine.PollUpdate {
	out := engine.PollUpdate{ID: v.PollID, Min: v.Results.Min}
	if p, ok := v.GetPoll(); ok {
		d := mediaReadConvertPoll(p, tg.PollResults{})
		out.Definition = &d
	}
	if _, ok := v.Results.GetTotalVoters(); ok || v.Results.TotalVoters != 0 {
		n := v.Results.TotalVoters
		out.TotalVoters = &n
	}
	for _, r := range v.Results.Results {
		out.Results = append(out.Results, engine.PollOption{Key: base64.StdEncoding.EncodeToString(r.Option), Voters: r.Voters, Chosen: r.Chosen, Correct: r.Correct})
	}
	return out
}
