# Release manifest signing

## Trust model and format

Every release builder, including an explicit private `0.0.0` rehearsal, requires
`minisign`, an explicitly supplied secret-key **file path**, and a non-placeholder
public key. There is no unsigned-manifest fallback. The production public key
below is pinned in `install.sh`; the release builder stamps the same key into the
desktop/CLI updater at build time, and the source tree never holds a private key.

## Production public key

| Field | Value |
| --- | --- |
| Algorithm | minisign, Ed25519 (`Ed`, legacy raw-message signatures via `-S -l`) |
| Key ID | `CF15D44BF3EA0B47` |
| Public key (base64 line) | `RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB` |
| SHA-256 fingerprint | `91a0ce3ce37eb754eba45eb9d51a0d090468b366ecbbc8b9137ea279426516d3` |

The fingerprint is `sha256(base64decode(public_line))` of the 42-byte public-key
packet, as described in the ceremony below. The equivalent `minisign.pub` file is:

```text
untrusted comment: minisign public key CF15D44BF3EA0B47
RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB
```

Verify a downloaded release manifest before trusting any checksum in it:

```sh
minisign -V -P RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB \
  -m checksums.txt -x checksums.txt.minisig
```

The launch must publish the same key ID and fingerprint on the release site;
compare both locations before trusting either. Any other key, including a
development or rehearsal key, is not a Telegram MCP release key.

`checksums.txt` is UTF-8 text with these signed header lines, followed by a sorted
SHA-256 list (`<64 lowercase hex digits><two spaces><filename>`):

```text
# version: X.Y.Z
# source_commit: <full 40-character lowercase commit ID>
# date: <UTC RFC3339 timestamp ending in Z>
```

The version and commit come from `build-source.json` for the exact build snapshot.
The date is the time the manifest is written, not the commit time. All regular
output files, including source metadata and Mac verification evidence, are
covered except `checksums.txt` and `checksums.txt.minisig` themselves. Unsafe
filenames and symlink payloads are refused. Re-running signing replaces the
signature, never adds it to its own hash list.

The detached signature is `checksums.txt.minisig`, produced with **`minisign -S
-l`** (legacy `Ed`, raw-message Ed25519). Minisign's default prehashed `ED` format
is not interchangeable with Go's standard-library `crypto/ed25519` verifier.
The builder verifies the result using the configured public key before success;
a mismatched key or signing failure removes the detached signature and fails.
Do not distribute a partial output directory from a failed build.

Release entry points use a temporary Go wrapper to merge the public trust anchor
into existing linker flags, including builds that clear `GOFLAGS`. The linker
symbol is
`github.com/bwoliveira/telegram-mcp-local/internal/updater.ReleasePublicKey`.
Only the public key is passed to Linux build containers. Neither the private key
nor its file is mounted there. Production tag builds additionally refuse to run
while the tracked `install.sh` has a placeholder, pins a key different from the
selected release key, or pins a version different from the tag. A combined Linux/macOS release verifies the Mac
bundle's detached manifest with that same key, reads each external file once,
hashes and stages those exact bytes privately, and imports only the staged copy.
Platform builds therefore cannot silently ship different updater trust anchors
or swap mutable Mac bytes between verification and import. This does not make an
old unsigned updater safe: release acceptance must exercise the integrated
signature-verifying updater.

Signing authenticates the manifest, not the safety of its contents or builder.
Keep native macOS Developer ID/notarization checks, reviewed clean snapshots,
trusted Mac-to-Linux bundle transfer, and owner publication approval. A signed
manifest does not independently prove that Mac build evidence was truthful.

## Owner ceremony

The production key pair already exists (key ID `CF15D44BF3EA0B47`). Steps 1–4
are done; step 5 runs for every release.

1. **Done.** The owner generated the pair with `minisign -G` on their own
   trusted Mac, with a passphrase (not `-W`). The encrypted private key lives
   only at minisign's default `~/.minisign/minisign.key` on that Mac. Never send
   the private key or passphrase in chat, commit it, put its contents in an
   environment variable, or expose it to CI/PR jobs or agents.
2. **Done.** The encrypted private-key file and its recovery information are
   stored in the owner's 1Password Personal vault, item
   `Telegram MCP — release signing key`. Verify an authorized recovery before
   relying on the key. Remove temporary decrypted/exported copies after signing.
3. **Done.** The complete base64 line, key ID and SHA-256 fingerprint are
   recorded in [Production public key](#production-public-key). The fingerprint
   convention is precisely `sha256(base64decode(public_line))`; it is not a hash
   of a `.pub` file's mutable comment or newline.
4. **Repository done; site at launch.** The key is pinned in `install.sh`, and
   release builds stamp it into the desktop updater. Publish the same key ID and
   fingerprint on the release site with the approved launch and compare both
   locations with the owner's record. Do not fetch an allegedly trusted key
   solely from the release being verified.
5. **Every release.** On the owner's Mac, point
   `TELEGRAM_MCP_MINISIGN_SECRET_KEY` at the key file and set
   `TELEGRAM_MCP_MINISIGN_PUBLIC_KEY` to the line above. Commit the new
   `release_version` in `install.sh` before tagging. Sign and verify the exact
   final output, record version/commit/date/fingerprint, and retain private
   build evidence. Publish manifest and signature with the artifacts only after
   the separate publication gate is approved.

The pinned key must never be replaced with a development key. A replacement
follows [Rotation, revocation and rollback](#rotation-revocation-and-rollback).

## Builder inputs

Install `minisign` first (for example `brew install minisign` or the distribution's
`minisign` package). Supply these environment variables to `release.sh` or
`release-mac.sh`:

- `TELEGRAM_MCP_MINISIGN_SECRET_KEY`: absolute path to the secret-key file,
  **outside the repository**, including resolved symlink targets. The variable
  contains a path, not private key material. Minisign may prompt locally for its
  passphrase; do not pass it on a command line.
- `TELEGRAM_MCP_MINISIGN_PUBLIC_KEY`: the actual single base64 public-key line
  from the owner's public record, not the `.pub` filename or comment.

`--unsigned` on a private macOS rehearsal only disables Apple signing; it does
not bypass manifest signing. An isolated, disposable, explicitly supplied test
key is allowed for `0.0.0` rehearsals. Never publish that key as the production
trust anchor. The test suite generates and deletes test-only keys outside Git;
it locates real minisign through PATH, `MINISIGN`, or scratch `**/bin/minisign`.

### Development update feed

To rehearse an update with an installed app, `TELEGRAM_MCP_UPDATES_API` (a
loopback or `100.64.0.0/10` http/https feed) and
`TELEGRAM_MCP_UPDATES_PUBLIC_KEY` (a disposable minisign public key) replace
GitHub Releases and the embedded trust anchor. They are honored only when
`TELEGRAM_MCP_DEV=1` is set exactly, and never by a binary whose
`ReleasePublicKey` was stamped by the release scripts. An accepted override logs
a static `WARNING: DEVELOPMENT MODE` line at every start. A refused override is
logged, disables update checks and is shown in Settings until it is unset; it
is never silently ignored. Signature, checksum, version, high-water-mark,
staging and rollback rules are unchanged. Release builds, installers and
service units must never set these variables. The rehearsal procedure is in
[Testing updates locally](../E4-DISTRIBUTION.md#testing-updates-locally).

## Recipient verification

The CLI install flow **requires minisign** in addition to hashing tools. Install
it before running the installer; missing verification tools must not downgrade
to unauthenticated checksums. The published installer also pins the one release
version it is allowed to install and rejects a correctly signed older/different
manifest, so a release-publishing compromise cannot replay an obsolete build.
The release builder refuses a production tag until both the public key and exact
version are pinned in `install.sh`; update that reviewed script for every release.
Desktop updates verify the pinned Ed25519 key in
Go and must refuse unknown keys, missing/invalid signatures and altered payloads.
For a manual download, use the [production public key](#production-public-key),
cross-checked against the release site:

```sh
minisign -V -P RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB \
  -m checksums.txt -x checksums.txt.minisig
# Only after successful signature verification, from the artifact directory:
sha256sum --check --ignore-missing checksums.txt
```

Also check that the signed version and source commit match the intended release
and that the particular downloaded artifact has an entry. A valid signature by
itself does not prevent replay of an older authorized release.

## Rotation, revocation and rollback

- **Planned rotation:** generate a new owner-held key through the same ceremony.
  Publish its full key/fingerprint and an old-key-signed transition notice at
  both repository and site. Ship a reviewed installer/updater trust-anchor
  transition before switching release signatures. Never silently replace the
  trust root based on data supplied by an untrusted new key.
- **Compromise or loss:** stop release signing/publication, revoke the affected
  key ID and fingerprint visibly at both locations, record the compromise
  interval and affected versions, and remove/quarantine suspect downloads.
  Announce the replacement through an independently authenticated owner channel.
  Existing binaries pinning a compromised key cannot learn revocation safely
  from a signature by that same key; provide an explicitly verified manual
  reinstall/trust-anchor update. Do not claim automatic remote revocation.
- **Rollback:** never overwrite an existing version/tag/asset or re-sign different
  bytes under the same version. Prefer a new, higher patch release containing
  the reviewed rollback fix. If an emergency manual downgrade is necessary,
  require explicit owner/user approval, verify the original signed manifest
  against a non-revoked trusted key, document the reason and security trade-off,
  and preserve the previous release evidence. Do not silently downgrade.

## Automatic update recovery limits

Automatic recovery restores only locally retained material. It does not authorize
an older download, lower the authenticated-version high-water mark, or bypass
signature verification.

Recovery does **not** cover the first update initiated by an old updater on
AppImage, macOS, or Windows: the updater that starts the replacement must already
implement the recovery protocol. Installing a recovery-capable candidate cannot
retroactively protect that transition.

Windows recovery also requires a retained last-known-good installer. A manual
Windows installation without that installer cannot automatically restore the
previous multi-file installation. A healthy updater-managed update establishes
the retained installer when its matching identity and health marker are promoted;
until then, recovery can require manual reinstallation of the previous release
verified against its original signed manifest and a non-revoked trusted key.

Native macOS and Windows update/recovery acceptance remains a release gate.
Linux unit tests, script-content assertions, cross-compilation, and synthetic
fixtures do not satisfy that gate. A rehearsal with the development feed on a
native machine exercises the production update path. It is evidence for that
gate only for the exact builds rehearsed, and it does not replace
notarization, Authenticode or SmartScreen checks of the final artifacts.
