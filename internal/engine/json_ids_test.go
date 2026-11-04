package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"testing"
)

// Peer and message ids are deliberately excluded: Telegram bounds those ids,
// unlike opaque TL long identifiers. Never pass the latter through float64.
func TestWideIDJSONDecimalStrings(t *testing.T) {
	for _, id := range []int64{42, 9007199254740993, -9007199254740993, math.MaxInt64, math.MinInt64} {
		for _, tc := range []struct {
			name, field string
			value       any
		}{
			{"poll", "id", Poll{ID: id}},
			{"poll_update", "id", PollUpdate{ID: id, Definition: &Poll{ID: id}}},
			{"webpage", "id", WebPageInfo{ID: id}},
			{"grouped_message", "grouped_id", Message{GroupedID: id}},
			{"native_transcript", "transcription_id", NativeTranscript{ID: id}},
		} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, id), func(t *testing.T) {
				encoded, err := json.Marshal(tc.value)
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(encoded, &body); err != nil {
					t.Fatal(err)
				}
				if got := body[tc.field]; got != strconv.FormatInt(id, 10) {
					t.Errorf("%s = %#v (%T); want exact decimal string %q", tc.field, got, got, strconv.FormatInt(id, 10))
				}
				decoded := reflect.New(reflect.TypeOf(tc.value))
				if err := json.Unmarshal(encoded, decoded.Interface()); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(decoded.Elem().Interface(), tc.value) {
					t.Errorf("round trip changed DTO: %s", encoded)
				}
			})
		}
	}
}

func TestWideIDJSONLegacyNumericCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		newValue    func() any
		id          func(any) int64
	}{
		{"poll", "id", func() any { return new(Poll) }, func(v any) int64 { return v.(*Poll).ID }},
		{"poll_update", "id", func() any { return new(PollUpdate) }, func(v any) int64 { return v.(*PollUpdate).ID }},
		{"webpage", "id", func() any { return new(WebPageInfo) }, func(v any) int64 { return v.(*WebPageInfo).ID }},
		{"grouped_message", "grouped_id", func() any { return new(Message) }, func(v any) int64 { return v.(*Message).GroupedID }},
		{"native_transcript", "transcription_id", func() any { return new(NativeTranscript) }, func(v any) int64 { return v.(*NativeTranscript).ID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, literal := range []string{"9007199254740993", "-9223372036854775808", "9223372036854775807"} {
				want, _ := strconv.ParseInt(literal, 10, 64)
				for _, token := range []string{literal, strconv.Quote(literal)} {
					v := tc.newValue()
					if err := json.Unmarshal([]byte(fmt.Sprintf(`{%q:%s}`, tc.field, token)), v); err != nil {
						t.Errorf("decode %s: %v", token, err)
						continue
					}
					if tc.id(v) != want {
						t.Errorf("decode %s = %d; want %d", token, tc.id(v), want)
					}
				}
			}
			for _, token := range []string{`"9223372036854775808"`, `9223372036854775808`, `"-9223372036854775809"`, `1.5`, `1e3`, `true`, `"not-an-id"`} {
				if err := json.Unmarshal([]byte(fmt.Sprintf(`{%q:%s}`, tc.field, token)), tc.newValue()); err == nil {
					t.Errorf("accepted invalid id %s", token)
				}
			}
		})
	}
}

func TestWideIDJSONHiddenMediaCredentialsAndNumericPeerIDs(t *testing.T) {
	m := Message{Key: MessageKey{Account: "test", Peer: PeerRef{Type: PeerUser, ID: 123}, ID: 7}, Media: &Media{Type: MediaDocument, Remote: []byte(`{"document_id":9223372036854775807,"photo_id":9223372036854775807,"access_hash":-9223372036854775808,"random_id":9223372036854775807}`)}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	key := body["key"].(map[string]any)
	if key["id"] != float64(7) || key["peer"].(map[string]any)["id"] != float64(123) {
		t.Fatalf("peer/message id types changed: %s", raw)
	}
	if got := body["media"].(map[string]any); len(got) != 2 || got["type"] != "document" {
		t.Fatalf("opaque media identifiers escaped: %s", raw)
	}
}
