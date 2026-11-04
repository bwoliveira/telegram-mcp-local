# Updater rollback contract

Recovery-capable desktop updaters retain recovery material until a later startup
observes a health marker for the exact candidate identity. AppImage and macOS
then remove the previous executable or bundle; Windows promotes the healthy
candidate installer to the retained last-known-good installer. Helpers attempt
restoration and relaunch when candidate health is not confirmed, subject to the
limits below. Ambiguous identity or version state is recoverable: it keeps the
backup and pending record instead of guessing.

## Windows NSIS boundary

NSIS updates keep authenticated installers in immutable version-and-SHA-256
slots. Before starting another update, the updater promotes a confirmed pending
installer. An unresolved pending transaction blocks a conflicting update, so a
new candidate cannot overwrite the healthy rollback installer.

Automatic recovery does not cover the first update performed by the old updater
on AppImage, macOS, or Windows. It also does not cover a manual Windows
installation without a retained last-known-good installer, including a manual
installation of a recovery-capable version. One healthy updater-managed Windows
update establishes recovery material when its matching identity and health
marker are promoted. Without it, reinstall the previous signed release manually.
Copying only the running `.exe` cannot restore the bridge, assets, uninstall
metadata, and registry or shortcut state.

Native macOS and Windows update/recovery acceptance remains a release gate.
Cross-compilation, Linux unit tests, and script-content checks do not replace it.
See [automatic update recovery limits](../../docs/security/release-signing.md#automatic-update-recovery-limits).
