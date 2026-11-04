package bridge

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
)

var catalogChangedNotification = []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n")

// catalogTracker is owned by Run's output lock, so observing a catalog and
// writing its notification cannot interleave with another response. Ordinary
// pings carry the daemon's content hash: a reconnect to a replaced binary is
// detected without client tools/list calls, extra network probes or polling.
// Idle clients which never ping use the tools/list TTL freshness hint instead.
// Request sequence suppresses late responses from the previous daemon.
type catalogTracker struct {
	version  string
	sequence uint64
}

func (c *catalogTracker) observe(request, response []byte, sequence uint64) bool {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(request, &req) != nil || len(req.ID) == 0 {
		return false
	}
	switch req.Method {
	case "initialize", "ping", "tools/list":
	default:
		return false
	}
	var resp struct {
		ID     json.RawMessage `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result struct {
			Meta struct {
				Version string `json:"catalogVersion"`
			} `json:"_meta"`
		} `json:"result"`
	}
	if json.Unmarshal(response, &resp) != nil || len(resp.Error) != 0 || !bytes.Equal(bytes.TrimSpace(req.ID), bytes.TrimSpace(resp.ID)) {
		return false
	}
	version := resp.Result.Meta.Version
	if len(version) != len("sha256:")+64 || !strings.HasPrefix(version, "sha256:") {
		return false
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(version, "sha256:")); err != nil {
		return false
	}
	if sequence <= c.sequence {
		return false
	}
	changed := c.version != "" && c.version != version
	c.version, c.sequence = version, sequence
	return changed
}
