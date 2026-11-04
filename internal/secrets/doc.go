// Package secrets stores Telegram credentials in an account-scoped OS keyring,
// or, on Linux, an authenticated encrypted file with a separate key directory.
//
// # Selection and integration
//
// Open prefers macOS Keychain, Windows Credential Manager or Linux Secret Service
// through github.com/zalando/go-keyring. TELEGRAM_MCP_SECRETS=keyring requires the
// keyring; =file explicitly chooses the Linux encrypted backend. An empty setting
// permits Linux fallback if the initial keyring read fails (including no session
// D-Bus). Backend and Fallback expose the decision without logging identifiers or
// errors from providers. Surface fallback in the UI; do not silently hide it.
// A later keyring operation failure returns ErrUnavailable, never switches stores.
// Availability checks are read-only and cannot guarantee subsequent writes work.
// Pin the selected backend for an account: automatic migration/merging is not
// performed, and logout must delete credentials in any previously selected store.
//
// Each Open handle is bound to one opaque account identifier; use a stable ID,
// not a phone number or display name. api_id, api_hash and session are opaque
// byte strings. Delete removes a key; DeleteAll removes the complete account.
// Get returns ErrNotFound for a missing key. Keyring provider size limits still
// apply; failures are redacted and never trigger a fallback containing partial
// credentials. Keyring values are limited to 1800 bytes before encoding (the
// shared safe limit across the library's macOS and Windows providers); larger
// values return ErrTooLarge before calling the provider. The engine integrator
// must verify its serialized session fits. The Linux file backend allows each
// value up to 16 MiB and the encoded account up to 32 MiB. This package does not
// validate Telegram-specific value formats.
//
// # File backend threat model
//
// Linux defaults are $XDG_STATE_HOME/telegram-mcp/secrets (or
// ~/.local/state/telegram-mcp/secrets) and $XDG_CONFIG_HOME/telegram-mcp/keys
// (or ~/.config/telegram-mcp/keys). Data and key directories must be disjoint,
// mode 0700; regular files must be mode 0600 or stricter. Symlinks are rejected.
// AES-256-GCM uses a fresh random nonce per write and account-bound associated
// data. Writes use same-directory temporary files, fsync, rename and directory
// fsync. Per-account flock serializes threads and processes sharing the paths.
// Keep the small non-secret lock files; unlinking live locks breaks exclusion.
//
// This protects against copying the database/data directory alone. It does NOT
// protect against copying the key directory too, processes running as the same
// user, or root. Backups MUST exclude the key directory; intentionally losing
// keys requires reauthentication. Deletion is logical/cryptographic deletion,
// not a promise of secure erase on SSDs, snapshots, swap or backups. Go does not
// guarantee memory erasure. Do not log returned secret values. Parent directories
// must be trusted and kept separate by the operator (including mount aliases).
// The file backend is Linux-only: POSIX modes do not enforce Windows ACLs.
//
// Dependency licenses and copyrights are recorded in THIRD_PARTY_NOTICES.md in
// this package; the release integrator must include them in aggregate notices.
package secrets
