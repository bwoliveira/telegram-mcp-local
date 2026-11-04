package updater

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Environment variables that point a real build at a local stand-in for
// GitHub Releases, signed with a disposable minisign key. They exist only to
// rehearse a complete update and rollback before anything is published.
const (
	// DevModeEnv must be exactly "1" for either override to be accepted. It
	// is the same switch that guards the sidecar manifest override.
	DevModeEnv = "TELEGRAM_MCP_DEV"
	// UpdatesAPIEnv replaces https://api.github.com. In development mode it
	// may be http or https on a loopback address or a Tailscale CGNAT
	// address (100.64.0.0/10), written as a literal IP (or "localhost").
	UpdatesAPIEnv = "TELEGRAM_MCP_UPDATES_API"
	// UpdatesPublicKeyEnv replaces the embedded release signing key. It is
	// accepted only together with UpdatesAPIEnv.
	UpdatesPublicKeyEnv = "TELEGRAM_MCP_UPDATES_PUBLIC_KEY"
)

// ErrDevFeedRefused reports an update override set without development mode.
// The updater then refuses to check at all until the variables are unset, so
// a stray override is visible instead of silently ignored.
var ErrDevFeedRefused = errors.New(UpdatesAPIEnv + " or " + UpdatesPublicKeyEnv +
	" is set but refused: a test update feed is accepted only when " + DevModeEnv + "=1")

// ErrDevFeedReleaseBuild reports an override in a build stamped with the
// production signing key: release builds never accept a test feed, even with
// TELEGRAM_MCP_DEV=1.
var ErrDevFeedReleaseBuild = errors.New(UpdatesAPIEnv + " or " + UpdatesPublicKeyEnv +
	" is set but refused: this is a release build signed for the production feed, which never accepts a test feed")

// releaseKeyPlaceholder is ReleasePublicKey before release builds stamp it.
const releaseKeyPlaceholder = "REPLACE_WITH_PRODUCTION_MINISIGN_PUBLIC_KEY"

// releaseKeyStamped reports whether this binary was built by the release
// scripts, which always replace the placeholder with the production key.
func releaseKeyStamped() bool { return ReleasePublicKey != releaseKeyPlaceholder }

// tailscaleCGNAT is the address range Tailscale assigns to tailnet devices.
var tailscaleCGNAT = func() *net.IPNet {
	_, n, err := net.ParseCIDR("100.64.0.0/10")
	if err != nil {
		panic(err)
	}
	return n
}()

// ConfigureDevFeed applies the test feed overrides read through getenv. With
// neither override set it changes nothing and returns nil: this is the path
// of every release build, installer and service unit, none of which set
// these variables.
//
// With an override but without DevModeEnv=1, or in a release build (one
// whose ReleasePublicKey was stamped), the overrides are refused: the
// refusal is logged, returned, and reported by every later Check, and the
// updater does not contact any feed. In development mode the overrides are
// validated, applied, and announced with a loud WARNING: DEVELOPMENT MODE
// log line. Signature, checksum, version, high-water mark, staging and
// rollback rules are unchanged; only the feed location and the trust anchor
// differ.
func (u *Updater) ConfigureDevFeed(getenv func(string) string) error {
	api := strings.TrimSpace(getenv(UpdatesAPIEnv))
	key := strings.TrimSpace(getenv(UpdatesPublicKeyEnv))
	if api == "" && key == "" {
		return nil
	}
	if getenv(DevModeEnv) != "1" {
		return u.refuseDevFeed(LogDevFeedRefused, ErrDevFeedRefused)
	}
	if releaseKeyStamped() {
		return u.refuseDevFeed(LogDevFeedReleaseBuild, ErrDevFeedReleaseBuild)
	}
	if api == "" {
		return u.refuseDevFeed(LogDevFeedInvalid, fmt.Errorf("%s requires %s in development mode; the production feed always uses the embedded release key", UpdatesPublicKeyEnv, UpdatesAPIEnv))
	}
	if !devFeedURLAllowed(api) || strings.Contains(api, "?") {
		return u.refuseDevFeed(LogDevFeedInvalid, fmt.Errorf("%s=%q is refused: a test feed must be http or https on a loopback or 100.64.0.0/10 address, without credentials, query or fragment", UpdatesAPIEnv, api))
	}
	if key != "" {
		if _, _, err := parseMinisignPublicKey(key); err != nil {
			return u.refuseDevFeed(LogDevFeedInvalid, fmt.Errorf("%s is refused: %w", UpdatesPublicKeyEnv, err))
		}
	}
	u.mu.Lock()
	u.API, u.devFeed, u.refused = api, true, nil
	if key != "" {
		u.PublicKey = key
	}
	u.mu.Unlock()
	message := LogDevFeedReleaseKey
	if key != "" {
		message = LogDevFeedTestKey
	}
	// The app's log keeps only static messages, so the message alone must
	// carry the warning; the attributes help in tests and on the console.
	u.logger().Warn(message, "feed", api, "public_key_env", key != "")
	return nil
}

// Log messages of ConfigureDevFeed. They are static so the app's redacting
// log handler keeps them (internal/httpserver allowlists them).
const (
	LogDevFeedTestKey      = "WARNING: DEVELOPMENT MODE: TELEGRAM_MCP_UPDATES_API replaces GitHub Releases and updates are verified with TELEGRAM_MCP_UPDATES_PUBLIC_KEY, a test key, NOT the release key. Never use this in production."
	LogDevFeedReleaseKey   = "WARNING: DEVELOPMENT MODE: TELEGRAM_MCP_UPDATES_API replaces GitHub Releases; updates are verified with the embedded release key. Never use this in production."
	LogDevFeedRefused      = "WARNING: TELEGRAM_MCP_UPDATES_API or TELEGRAM_MCP_UPDATES_PUBLIC_KEY is set but refused: a test update feed is accepted only when TELEGRAM_MCP_DEV=1; update checks are disabled until it is unset"
	LogDevFeedReleaseBuild = "WARNING: TELEGRAM_MCP_UPDATES_API or TELEGRAM_MCP_UPDATES_PUBLIC_KEY is set but refused: release builds never accept a test update feed; update checks are disabled until it is unset"
	LogDevFeedInvalid      = "WARNING: DEVELOPMENT MODE: the test update feed is invalid and was refused; update checks are disabled until it is unset"
)

func (u *Updater) refuseDevFeed(message string, err error) error {
	u.refuse(err)
	u.logger().Warn(message, "error", err.Error())
	return err
}

func (u *Updater) refuse(err error) {
	u.mu.Lock()
	u.refused = err
	u.mu.Unlock()
}

// devFeedURLAllowed is the development-mode URL rule for both the release
// API and its assets: http or https on a literal loopback or tailnet address.
// Host names other than localhost are refused so DNS cannot move the feed.
func devFeedURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if isLoopbackHost(host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil && tailscaleCGNAT.Contains(ip)
}
