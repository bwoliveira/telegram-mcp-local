# Telegram MCP

> **Beta.** Telegram MCP 1.0.0 is new and may still have bugs. Use it at your own risk, keep the official Telegram app installed, and [report issues on GitHub](https://github.com/bwoliveira/telegram-mcp-local/issues).

Telegram MCP is an independent local MCP integration for a personal Telegram account. It is not a Telegram bot or an official product, and is not affiliated with, sponsored by, or endorsed by Telegram. It does not use the official Telegram logo. Project site and downloads: <https://telegram-mcp.bwoliveira.xyz>.

The project combines a gotd-based MTProto engine, per-account SQLite/FTS5 storage, MCP tools, local media/transcription and a panel. A tool catalogue and offline tests do not by themselves establish Telegram parity. See [Public snapshot and tool usage](docs/PUBLIC-SNAPSHOT.md) for the snapshot scope, usage boundaries and excluded private records.

## Safety and status

- This repository starts from a sanitized source snapshot; the private development history is not published. Account connections, service installation and Telegram writes require the operator's explicit authorization.
- Read-only is the default. Every Telegram mutation requires a preview and explicit human approval; approval must match the action actually executed. Reading must not mark chats as read.
- Messages are untrusted third-party data, not instructions. Never commit sessions, API credentials, tokens, databases, downloaded media or models.
- Use isolated, explicitly authorized fixtures for real-account tests. Destructive actions must not target ordinary conversations.
- The repository contains offline checks for engine, storage, tools and packaging. Run the [CI checks](docs/CI.md) against the exact source being evaluated; historical private results are not public acceptance evidence. Real-account behavior and native distribution require separate verification.

## Documentation

- [Public snapshot and tool usage](docs/PUBLIC-SNAPSHOT.md)
- [Distribution, build commands and acceptance limits](docs/E4-DISTRIBUTION.md)
- [CI checks and their limits](docs/CI.md)
- Canonical interfaces: [MCP tool schemas](internal/mcp/catalogue.go), [engine API](internal/engine/engine.go) and [store API](internal/store/API.md)
- Contracts: [engine](docs/contratos/engine.md), [store](docs/contratos/store.md), [lifecycle](docs/contratos/lifecycle.md), [security](docs/contratos/security.md) and [webhooks](docs/contratos/webhooks.md)
- [HTTP authentication and session boundary](internal/httpserver/README.md)
- [Changelog](CHANGELOG.md) and [third-party notices](THIRD_PARTY_NOTICES.md)

Inherited design documents are historical, not proof of Telegram features. Private plans, PR evidence, parity matrices and account-testing records are not part of this snapshot. Repository documentation is in English; panel translations remain localized. No live site or launch is implied.

`list_unread` and `list_unanswered` exclude conversations with bot users by default. Set `include_bots: true` to include them. Groups and channels are not excluded just because they contain bots. This filter uses persisted peer metadata, not names.

## Desktop first use

The desktop app opens the local panel and stays in the tray/menu bar when its
window closes (unless you turn off that preference). Its own blue glyph shows
connection state; the menu distinguishes disconnected and login-required states.
Settings enables per-user start-at-login: a macOS LaunchAgent, a Windows Run key,
or a Linux XDG autostart entry. Explicit **Quit** stops the app. The panel retains
**Connect Claude Desktop / Claude Code** and the other client setup choices.

On first use, without environment credentials, the desktop panel asks for your
own `api_id` and `api_hash` from <https://my.telegram.org/apps>. The ID must be
numeric and positive; the hash must contain exactly 32 hexadecimal characters.
The app stores them through its existing pinned secrets backend, does not log or
return them, and proceeds to Telegram's QR login. Do not paste them into an AI
conversation. Headless users can still supply `TELEGRAM_API_ID` and
`TELEGRAM_API_HASH` for initial vault seeding; later starts use that same vault.
Existing vault credentials cannot be silently overwritten by environment values.

## CLI installation and remote panel

`install.sh` installs only the CLI on Linux and macOS, amd64 or arm64. It downloads
the latest matching `telegram-mcp_<os>_<arch>.tar.gz`, `checksums.txt` and
`checksums.txt.minisig`, requires [minisign](https://jedisct1.github.io/minisign/)
to verify the manifest against the pinned release key, then verifies SHA-256
before installation, and includes third-party notices. It does not call
sudo or start a service. The default destination is `~/.local/bin`; use
`sh install.sh --prefix /your/writable/bin` for another directory and add it to
`PATH`. Downloads come from the
[latest release](https://github.com/bwoliveira/telegram-mcp-local/releases/latest):

```sh
sh install.sh
# Optional, after choosing where to keep your account data:
telegram-mcp serve
# Or explicitly install the user's headless service:
telegram-mcp service install
```

For offline release fixtures, set `TELEGRAM_MCP_RELEASE_BASE_URL` to the fixture's
asset directory; HTTPS is required except for loopback HTTP fixtures. Run
`python3 scripts/test-install.py` for the success, bad-checksum, invalid-manifest,
unsupported-architecture and incomplete-archive tests. The tests do not contact
GitHub or Telegram.

### Verifying a release manually

Releases are signed with minisign key ID `CF15D44BF3EA0B47`. Verify the manifest
before trusting any checksum in it, then check the downloaded files:

```sh
minisign -V -P RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB \
  -m checksums.txt -x checksums.txt.minisig
sha256sum --check --ignore-missing checksums.txt
```

The key's SHA-256 fingerprint and the rotation policy are in
[docs/security/release-signing.md](docs/security/release-signing.md#production-public-key).

To use a remote machine's loopback-only panel, create a tunnel from your computer:

```sh
ssh -N -L 47831:127.0.0.1:47831 your-server
# In a separate shell on the server:
telegram-mcp open --print
```

Copy the printed `http://127.0.0.1:47831/...` bootstrap URL into your local browser.
**Anyone who has that URL can sign in.** It is single-use and short-lived; do not
share or log it. Linux without `DISPLAY` or `WAYLAND_DISPLAY` prints the URL
automatically. The command does not print the persistent bearer token, and
Host/Origin, session and CSRF checks remain in force. Adjust both tunnel ports if
you configured a different listener port; never expose the listener publicly.

## Packages and updates

See [Distribution](docs/E4-DISTRIBUTION.md) for clean-snapshot build commands,
artifact checks, macOS signing/notarization requirements and test limits.
Installers are published on the
[releases page](https://github.com/bwoliveira/telegram-mcp-local/releases). Linux packages
support Ubuntu 24.04 or newer and Debian 13 or newer; the AppImage needs a
graphical desktop and expects a few system libraries (glibc, graphics, fonts),
listed in [Supported Linux distributions](docs/E4-DISTRIBUTION.md#supported-linux-distributions).
Linux headless use continues through `telegram-mcp serve` or the explicitly
installed user service. The DEB's `telegram-mcp` and the AppImage accept the same
subcommands and run them without a window; only a bare start opens the app.

The desktop updater checks `bwoliveira/telegram-mcp-local` releases without an
embedded GitHub token. `TELEGRAM_MCP_UPDATES_API` overrides the API base for a local
release server. Checks only display a notice; **Install** is an explicit user
action. The updater verifies SHA-256 before replacing the app, checks macOS code
signatures/signer identity, and restarts after the old process exits. DEB installs
remain package-manager/manual updates rather than silently requesting privilege.
The repository includes a local v1-to-v2 executable replacement/restart test;
that fixture is not native packaged-GUI update acceptance on every OS.

## Troubleshooting

After upgrading, restart or reload MCP in your client if tools look outdated.
`tools/list` advertises a ten-minute `ttlMs` cache hint and a SHA-256 catalog
version. The stdio bridge emits `notifications/tools/list_changed` when a later
initialize, ping or catalog response reports a different catalog, including after
a daemon restart. Idle clients and clients that ignore cache hints may still
need a manual reload.

Telegram identifiers that can exceed JavaScript's safe integer range (polls,
web pages, grouped messages and native transcription IDs) are decimal strings
in tool responses, exports and webhooks. Message and peer identifiers retain
their numeric representation. Existing numeric IDs in local snapshots remain
readable; private media references and access hashes remain unexposed.

## Media bytes and Telegram photo compression

Telegram processes uploads sent as `photo`: a PNG may be recompressed and
returned as JPEG, with different bytes and dimensions. This is server behavior,
not a byte-preserving upload. Use `send_media_message` with media type `document`
when the original format and bytes must be preserved. Protected (`noforwards`)
and temporary media remain unavailable for download or transcription regardless
of the send type.

## Origin and license

Foundation source commit: `e579f0a1ddade312dc4cb0f7e628e7bd7f6b19db`.
Development history is retained privately. This sanitized source snapshot omits
private planning and acceptance evidence; see [Public snapshot](docs/PUBLIC-SNAPSHOT.md).
It does not rewrite or publish the private development repository. The original
attribution below describes the development fork; the snapshot does not include
its full Git history.

This fork preserves the history of [whatsapp-mcp-local](https://github.com/BrOrlandi/whatsapp-mcp-local) by [Bruno Orlandi](https://github.com/BrOrlandi), the basis for the architecture, panel and app. His [telegram-cli](https://github.com/BrOrlandi/telegram-cli) supplied adapted mention logic. Original copyright, history and attribution are retained. The repository is [MIT licensed](LICENSE), preserving Orlandi's rights and Bruno Oliveira's additions; dependencies and binaries retain their own licenses. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
