package gotdengine

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
)

// Errors deliberately omit JSON decoder details and all session bytes. In
// particular, neither error is session.ErrNotFound: damaged or newer credentials
// must not silently fall through to a new login and overwrite an existing key.
var (
	ErrSessionCorrupt = errors.New("gotd session: invalid or corrupted data")
	ErrSessionVersion = errors.New("gotd session: unsupported format version")
)

const compactSessionLimit = 1800

// compactSessionData v2 stores only the permanent connection identity. Config
// (including DC options) is refreshable, not a credential. In gotd v0.163.0,
// restoreConnection uses DC/AuthKey/AuthKeyID/Salt; connection initialization
// fetches help.getConfig again. Addr is optional (gotd normally leaves it empty).
// This is a new representation, not compressed gotd JSON, and requires no fork.
// Both versions remain inside the existing encrypted secrets backend/namespace.
type compactSessionData struct {
	DC        int
	Addr      string
	AuthKey   []byte
	AuthKeyID []byte
	Salt      int64
}

type sessionEnvelope struct {
	Version int
	Data    json.RawMessage
}

func readSession(b []byte) (*session.Data, int, error) {
	var envelope sessionEnvelope
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, 0, ErrSessionCorrupt
	}
	var d session.Data
	switch envelope.Version {
	case 1: // gotd's original full JSON, retained until the next successful save.
		if err := json.Unmarshal(envelope.Data, &d); err != nil {
			return nil, 0, ErrSessionCorrupt
		}
	case 2:
		if len(b) > compactSessionLimit {
			return nil, 0, ErrSessionCorrupt
		}
		var c compactSessionData
		decoder := json.NewDecoder(bytes.NewReader(envelope.Data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			return nil, 0, ErrSessionCorrupt
		}
		d = session.Data{DC: c.DC, Addr: c.Addr, AuthKey: c.AuthKey, AuthKeyID: c.AuthKeyID, Salt: c.Salt}
	case 0:
		return nil, 0, ErrSessionCorrupt
	default:
		return nil, 0, ErrSessionVersion
	}
	// DC zero is a valid gotd legacy sentinel: restoreConnection falls back to
	// the client's configured DC. Preserve it rather than forcing a new login.
	if d.DC < 0 || len(d.Addr) > 255 || len(d.AuthKey) != 256 || len(d.AuthKeyID) != 8 {
		return nil, 0, ErrSessionCorrupt
	}
	var key crypto.AuthKey
	copy(key.Value[:], d.AuthKey)
	copy(key.ID[:], d.AuthKeyID)
	if key.Value.ID() != key.ID {
		return nil, 0, ErrSessionCorrupt
	}
	return &d, envelope.Version, nil
}

func compactSession(b []byte) ([]byte, error) {
	d, _, err := readSession(b)
	if err != nil {
		return nil, err
	}
	c := compactSessionData{DC: d.DC, Addr: d.Addr, AuthKey: d.AuthKey, AuthKeyID: d.AuthKeyID, Salt: d.Salt}
	result, err := json.Marshal(struct {
		Version int
		Data    compactSessionData
	}{Version: 2, Data: c})
	if err != nil || len(result) > compactSessionLimit {
		return nil, ErrSessionCorrupt
	}
	return result, nil
}

func expandSession(b []byte) ([]byte, error) {
	d, _, err := readSession(b)
	if err != nil {
		return nil, err
	}
	// Canonicalize legacy input too: passing original duplicate JSON fields to
	// gotd's typed decoder could produce errors containing unvalidated bytes.
	// This changes only the in-memory view, never the stored legacy credential.
	// Only the gotd-facing boundary sees the version 1 envelope. The stored bytes
	// remain compact, so offline status measures their actual portable size.
	result, err := json.Marshal(struct {
		Version int
		Data    *session.Data
	}{Version: 1, Data: d})
	if err != nil {
		return nil, ErrSessionCorrupt
	}
	return result, nil
}
