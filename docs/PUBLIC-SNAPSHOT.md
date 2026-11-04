# Preparing a public source snapshot

This tool prepares a private staging artifact, not a release. It exports one
reviewed commit with `git archive`, never copies a working checkout, never runs
`git init`, and never creates, renames, pushes or publishes a repository.
Ignored files, untracked state, commit metadata, refs and Git history are not
exported. Git replacement objects are disabled, and the archive's complete file
inventory, modes and content hashes must match the tracked blobs before policy
changes are applied. Export attributes that alter or omit blobs fail closed.
A successful scan is not authorization to publish.

## Run an offline rehearsal

Requirements: Git, Python 3, the pinned Gitleaks binary installed by
`scripts/install-gitleaks.sh`, `zbarimg` 0.23.93 and Tesseract 5.5.0. Both image
scanners are mandatory. The exporter verifies their versions and makes zbarimg
decode a generated known QR before it inspects the candidate; a missing,
broken or mismatched scanner fails the export.
No real account or application service is needed.

Create an owner-reviewed, UTF-8 sensitive-term inventory **outside every
checkout and output directory**. Supply one exact identifier, account/chat name,
username, phone representation, email or internal hostname per line. Blank lines
and lines starting with `#` are ignored. Include phone formatting variants when
applicable. Do not add public legal attribution to the private-data inventory.
The inventory is mandatory and must contain at least one term: a missing/empty
inventory fails closed. Keep it private with owner-only permissions.

```sh
scripts/install-gitleaks.sh "$HOME/.cache/snapshot-tools/gitleaks"
export GITLEAKS_BIN="$HOME/.cache/snapshot-tools/gitleaks"
export PUBLIC_SNAPSHOT_DENY_FILE="$HOME/.config/release/private-terms.txt"
python3 scripts/public-snapshot.py --ref main \
  --output "$HOME/.cache/public-source-rehearsal"
```

The output must be a **new** directory outside the checkout; an existing target
is never overwritten. `--ref` accepts a commit or branch and defaults to `main`.
Use the candidate branch during review; use the frozen, integrated `main` commit
on launch day. Dirty or untracked policy edits do not affect an export: the
exclusion, pattern and overlay policies come from the selected commit itself.
Links, submodules, path traversal and special archive members are rejected.

Output layout:

- `tree/`: the proposed source tree, with no Git metadata;
- `manifest.json`: source commit/tree IDs, policy and private-inventory SHA-256,
  included/excluded file paths, sizes, executable bits and SHA-256, overlay and
  image-metadata sanitization before/after hashes, scanner versions/results and
  counts;
- `reports/gitleaks.json`: a fully redacted Gitleaks directory-scan report.

Keep the whole staging directory private. **Only the reviewed `tree/` is a
candidate for a future initial commit.** Do not commit the private inventory,
manifest or scanner reports. Failed outputs retain `status: failed` and are not
release candidates. Error diagnostics never echo matched private terms or QR
payloads. Exit codes: `0` passed, `1` findings/scanner failure, `2` prerequisite
or export error. Gitleaks is mandatory; its version and binary SHA-256 must match
`release/public-scanners.json`, and a valid empty JSON report is required for
success. The pin matches the existing Linux x86-64 installer. Other scanner
platforms require an explicitly reviewed versioned checksum, not a bypass.

## Versioned policy and deliberate omissions

`release/public-exclude.txt` is the reviewed exclusion inventory. Each rule has
an explanatory group comment; every matching tracked file and original hash is
listed in the manifest. It omits:

- real-account reports, spikes, screenshots/evidence, integration and readiness
  journals, local debugging records, workstream requests and owner decisions;
- historical product/commercial plans, agent-only
  instructions and private release coordination (`docs/RELEASE.md` stays in the
  development archive, with this document as the public procedure);
- unreviewed legacy onboarding screenshots. No repository generator produces
  those three images with fake-engine, masked-number, invalid-QR fixtures, so
  they are excluded, not cosmetically blurred or declared synthetic;
- site-owned deployment/preview scripts, private site design/QA notes, their
  operational README and account-specific preview test. Their owner must
  sanitize and review them before changing these exclusions. The site source,
  legal notices, generator and other tests remain.

The experimental connection command stays included because an offline session-
format regression builds it. It is not installed by the production packaging;
its presence does not authorize real-account experiments. Removing code merely
to hide internal documentation must not break the exported offline test suite.

`release/public-overlays.json` contains three narrowly scoped, count-checked
export-only changes. It selects `ubuntu-latest` in the exported CI workflow,
regardless of private-development routing, and strips the private coordination journal
from the exported parity document while sanitizing its old branding-credit
allowlist reference. The development parity document is not edited; its tool table remains
available to the offline catalogue tests. Original attribution is retained.
Each overlay fails if its expected match count changes. Review public CI after
integrating any workflow changes; remove the future public repository from all
private runner groups as a separate owner-controlled launch gate.

`docs/CI.md` follows the visibility-based development policy: private runners
while private, GitHub-hosted runners when public. Runner labels in prose are
placeholders; the snapshot intentionally narrows the exported workflow to hosted
runners even during its initial private review.

## Personal-data verification is separate from secret scanning

`release/public-patterns.json` rejects privileged agent-home paths, personal macOS
home paths, private container identifiers, internal DNS suffixes, private/shared
IPv4 ranges and the private development runner label. It inspects included file
names and contents, including ASCII and UTF-16 strings in binaries. Unknown
email addresses fail unless they use the reserved `.invalid` suffix, the exact
public Git transport address, or a path-scoped SHA-256 of a reviewed synthetic
negative-test line. Retina image filenames are not interpreted as email addresses.
Exact line hashes also scope exceptions for code identifiers and synthetic
internal-host/private-IP negative tests; these exceptions cannot override a match
in the owner's private inventory. No broad email/domain or directory is exempted.

Current reviewed line exceptions fall into three groups. Author addresses in
vendored third-party license texts (`scripts/linux-native/legal/`) are retained
verbatim because copyright notices must be preserved. The development update
feed's documented shared address range, its boundary/refusal tests, and the
webhook SSRF tests are technical content, not hosts. Synthetic negative-test
fixtures cover the remaining entries. Tests that only need an arbitrary address
use reserved documentation values (RFC 5737/3849 or `.invalid`) instead. A real
private-network host address is never allowlisted; replace it.

The external inventory is compared case-insensitively; numeric identifiers use
digit boundaries. Its values never enter Git, manifests or diagnostics. This
avoids publishing hashes of small, guessable peer-ID/phone spaces as a substitute
for privacy. The manifest records only the inventory file hash for private audit
reproducibility. Generic patterns catch new hosts/paths; the private inventory
must cover known names/IDs that no general-purpose secret scanner can identify.

All raster images are identified by magic bytes, not file extension. Before any
content scan or review-hash check, the exporter deterministically removes PNG
`tEXt`/`zTXt`/`iTXt`/`eXIf`, JPEG EXIF/XMP/comment segments and WebP EXIF/XMP
chunks. WebP metadata flags are cleared with the removed chunks. The manifest
binds each transformation's input and exported hashes; `release/reviewed-images.txt`
must contain the hash of the sanitized exported bytes. Malformed PNG, JPEG or
WebP containers fail closed. `zbarimg` scans each supported raster and rejects
**any decodable barcode**, including pairing QR payloads. Tesseract OCR scans
the same images; its output is checked against the private inventory, generic
PII patterns and email policy. Scanner errors fail closed. Every image or icon
container must also match an exact path/hash/date/reviewer entry in
`release/reviewed-images.txt`; a new or changed image is blocked until it
receives visual review.

Compressed archives, PDFs, fonts, executable formats, icon containers and
unknown binary data are treated as opaque by magic bytes. They are rejected
unless their exact path and SHA-256 have a documented justification in
`release/reviewed-opaque.txt`. The small current allowlist covers visually
reviewed icon containers and redistributable site fonts; archive payloads are
not allowlisted. Every exported workflow is parsed with PyYAML's safe loader
after overlays. Any `runs-on` value containing `self-hosted` or `homelab-ci`,
any dynamic `runs-on` expression, malformed YAML, custom tag, recursive alias or
duplicate key fails closed. If PyYAML is unavailable, a conservative lexical
fallback accepts only simple literal runner values and rejects escapes, anchors,
aliases, tags and expressions in `runs-on`.

Gitleaks scans the resulting directory with the selected tree's reviewed
configuration and full redaction. The manifest can say `passed` only when every
verifier ran and passed; it records scanner versions and per-verifier counts.
Automated checks do not replace manual review of document content, provenance,
licenses and future policy changes.

## Owner-controlled launch, later

Preserve the existing development history privately. With explicit owner
approval, create the new release repository from **only** the approved tree and
one initial commit; do not mirror, fork/import, push old refs, or force-push the
development repository. These actions are outside the snapshot tool.

Configure both initial-commit author and committer on launch day with the
owner's verified GitHub **noreply** address from GitHub email settings, not a
personal mailbox. Do not guess the numeric noreply prefix. Review `git show
--format=fuller` locally before pushing, verify exactly one commit and no
inherited refs, and rescan both the new tree and its new history. No initial
commit or repository is created by this rehearsal.

Before publication, rerun branding, packaging, complete offline Go vet/race tests
and clean-checkout CI on the exact approved source; review real-account and
platform-distribution limitations honestly. Obtain explicit owner approval for
coordinated public visibility, site and downloads. The snapshot manifest alone
does not establish launch readiness.
