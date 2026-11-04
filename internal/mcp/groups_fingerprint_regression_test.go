package mcp

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	fakeengine "github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

// These mutations only exercise the synthetic engine. An unrelated approved
// write must not invalidate an existing preview, whereas a changed field or
// participant role being overwritten must require another human approval.
func mutateGroupMetadata(t *testing.T, f *fakeengine.Controller, group engine.PeerRef, title, about *string) {
	t.Helper()
	r := engine.UpdateGroupRequest{Group: group, Title: title, About: about, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := f.UpdateGroup(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
	if out, err := f.UpdateGroup(context.Background(), r); err != nil || !out.Done {
		t.Fatalf("synthetic metadata mutation: %+v, %v", out, err)
	}
}

func mutateGroupRole(t *testing.T, f *fakeengine.Controller, group engine.PeerRef, user engine.PeerRef) {
	t.Helper()
	r := engine.ParticipantsRequest{Group: group, Users: []engine.PeerRef{user}, Action: engine.ParticipantsPromote, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := f.ManageParticipants(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
	if out, err := f.ManageParticipants(context.Background(), r); err != nil || !out.Done {
		t.Fatalf("synthetic role mutation: %+v, %v", out, err)
	}
}

func TestGroupFingerprintIgnoresUnrelatedTitleChanges(t *testing.T) {
	for _, groupID := range []string{"chat:1001", "channel:2001", "channel:2002"} {
		for _, action := range []string{"add", "remove", "ban", "promote", "demote", "invite_link", "leave", "about"} {
			t.Run(groupID+"/"+action, func(t *testing.T) {
				s, f := newFakeServer(t, engine.ModeReadWrite)
				group, err := parsePeer(groupID)
				if err != nil {
					t.Fatal(err)
				}
				name := "manage_group_participants"
				a := map[string]any{"group_id": groupID, "action": action, "participants": []string{"user:1001"}}
				switch action {
				case "invite_link":
					name, a = "get_group_invite_link", map[string]any{"group_id": groupID, "reset": true}
				case "leave":
					name, a = "leave_group", map[string]any{"group_id": groupID}
				case "about":
					name, a = "update_group", map[string]any{"group_id": groupID, "description": "approved new about"}
				}
				fp := previewFingerprint(t, toolCall(t, s, name, a))
				title := "Unrelated group title update"
				mutateGroupMetadata(t, f, group, &title, nil)
				a["confirm"], a["preview_fingerprint"] = true, fp
				if out := toolCall(t, s, name, a); out["isError"] != false || len(f.Writes()) != 2 {
					t.Fatalf("unrelated title change invalidated %s preview: %v; writes=%d", name, out, len(f.Writes()))
				}
			})
		}
	}
}

func TestGroupFingerprintBindsOnlyUpdatedMetadata(t *testing.T) {
	for _, field := range []string{"name", "description"} {
		for _, relevant := range []bool{false, true} {
			label := "unrelated"
			if relevant {
				label = "relevant"
			}
			t.Run(field+"/"+label, func(t *testing.T) {
				s, f := newFakeServer(t, engine.ModeReadWrite)
				a := map[string]any{"group_id": "chat:1001", field: "approved replacement"}
				fp := previewFingerprint(t, toolCall(t, s, "update_group", a))
				value := "concurrent metadata update"
				var title, about *string
				if (field == "name") == relevant {
					title = &value
				} else {
					about = &value
				}
				mutateGroupMetadata(t, f, engine.PeerRef{Type: engine.PeerChat, ID: 1001}, title, about)
				a["confirm"], a["preview_fingerprint"] = true, fp
				out := toolCall(t, s, "update_group", a)
				if relevant {
					if out["isError"] != true || len(f.Writes()) != 1 {
						t.Fatalf("changed field accepted stale preview: %v; writes=%d", out, len(f.Writes()))
					}
				} else if out["isError"] != false || len(f.Writes()) != 2 {
					t.Fatalf("unrelated field invalidated preview: %v; writes=%d", out, len(f.Writes()))
				}
			})
		}
	}
}

func TestGroupFingerprintBindsTargetParticipantRole(t *testing.T) {
	for _, relevant := range []bool{false, true} {
		label := "unrelated"
		if relevant {
			label = "relevant"
		}
		t.Run(label, func(t *testing.T) {
			s, f := newFakeServer(t, engine.ModeReadWrite)
			group := engine.PeerRef{Type: engine.PeerChat, ID: 1001}
			other := engine.PeerRef{Type: engine.PeerUser, ID: 1002}
			g, err := f.RefreshGroup(context.Background(), engine.GroupRequest{Group: group})
			if err != nil {
				t.Fatal(err)
			}
			members := append(g.Participants, engine.Participant{Peer: other, Role: "member"})
			g.Peer.Participants = len(members)
			if err := f.Publish(context.Background(), engine.Batch{Source: engine.SourceRefresh,
				Peers:        []engine.PeerUpsert{{Peer: g.Peer}, {Peer: engine.Peer{Ref: other, Kind: engine.KindUser, FirstName: "Synthetic other member", Access: engine.AccessOK}}},
				Participants: []engine.ParticipantsSnapshot{{Group: group, Participants: members, Size: len(members), About: g.About, At: fakeengine.Epoch(), AmAdmin: true}},
			}); err != nil {
				t.Fatal(err)
			}
			a := map[string]any{"group_id": "chat:1001", "action": "promote", "participants": []string{"user:1001"}}
			fp := previewFingerprint(t, toolCall(t, s, "manage_group_participants", a))
			id := int64(1002)
			if relevant {
				id = 1001
			}
			mutateGroupRole(t, f, engine.PeerRef{Type: engine.PeerChat, ID: 1001}, engine.PeerRef{Type: engine.PeerUser, ID: id})
			a["confirm"], a["preview_fingerprint"] = true, fp
			out := toolCall(t, s, "manage_group_participants", a)
			if relevant {
				if out["isError"] != true || len(f.Writes()) != 1 {
					t.Fatalf("changed target participant role accepted stale preview: %v; writes=%d", out, len(f.Writes()))
				}
			} else if out["isError"] != false || len(f.Writes()) != 2 {
				t.Fatalf("unrelated participant invalidated preview: %v; writes=%d", out, len(f.Writes()))
			}
		})
	}
}
