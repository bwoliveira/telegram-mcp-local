package engine

import (
	"encoding/json"
	"strconv"
)

// decimalID decodes both the public decimal-string representation of opaque TL
// long ids and legacy numeric JSON stored before that representation was fixed.
// Keep the conversion entirely integral: decoding through any/float64 would
// silently change ids above JavaScript's Number.MAX_SAFE_INTEGER (2^53-1).
// Access hashes/file locations remain private (json:"-"), not stringified DTOs.
type decimalID int64

func (id *decimalID) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		return nil
	}
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*id = decimalID(value)
	return nil
}

// The explicit fields shadow the ,string tags only while decoding. Encoding
// always uses the tags on the DTOs, including nested tool/export/webhook values.
func (p *Poll) UnmarshalJSON(raw []byte) error {
	type wire Poll
	v := struct {
		*wire
		ID *decimalID `json:"id"`
	}{wire: (*wire)(p)}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.ID != nil {
		p.ID = int64(*v.ID)
	}
	return nil
}

func (p *PollUpdate) UnmarshalJSON(raw []byte) error {
	type wire PollUpdate
	v := struct {
		*wire
		ID *decimalID `json:"id"`
	}{wire: (*wire)(p)}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.ID != nil {
		p.ID = int64(*v.ID)
	}
	return nil
}

func (p *WebPageInfo) UnmarshalJSON(raw []byte) error {
	type wire WebPageInfo
	v := struct {
		*wire
		ID *decimalID `json:"id"`
	}{wire: (*wire)(p)}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.ID != nil {
		p.ID = int64(*v.ID)
	}
	return nil
}

func (m *Message) UnmarshalJSON(raw []byte) error {
	type wire Message
	v := struct {
		*wire
		GroupedID *decimalID `json:"grouped_id"`
	}{wire: (*wire)(m)}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.GroupedID != nil {
		m.GroupedID = int64(*v.GroupedID)
	}
	return nil
}

func (t *NativeTranscript) UnmarshalJSON(raw []byte) error {
	type wire NativeTranscript
	v := struct {
		*wire
		ID *decimalID `json:"transcription_id"`
	}{wire: (*wire)(t)}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.ID != nil {
		t.ID = int64(*v.ID)
	}
	return nil
}
