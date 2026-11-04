package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// SEP-2549's ttlMs is a freshness hint at the top level of the list result,
// not a promise that the catalog cannot change before its ten minutes expire.
const catalogTTLMS = 600000

// Tool definitions are static for one daemon binary. Hash their complete wire
// representation (including descriptions and schemas), not release numbers,
// process identity or account state. encoding/json sorts object keys; the tool
// and schema array order remains part of the contract clients actually cache.
var toolCatalogVersion = catalogVersion(toolDefinitions())

func catalogVersion(tools []any) string {
	canonical, err := json.Marshal(tools)
	if err != nil {
		panic(fmt.Sprintf("invalid static tool catalog: %v", err))
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
}

// catalogMeta is a private extension, separate from SEP-2549. Returning it on
// initialize and ping lets the stdio bridge detect a replaced daemon binary on
// the next ordinary liveness check, without polling tools/list or requiring the
// client to rediscover tools before we can tell it discovery is stale. There is
// deliberately no background service, database or Telegram access here.
func catalogMeta() map[string]any {
	return map[string]any{"catalogVersion": toolCatalogVersion}
}

func catalogResult() map[string]any {
	return map[string]any{
		"tools":      toolDefinitions(),
		"ttlMs":      catalogTTLMS,
		"cacheScope": "public", // Identical definitions for every account/mode.
		"_meta":      catalogMeta(),
	}
}
