#!/usr/bin/env python3
"""Offline packaging regressions; no release, network, or account is used.

Run with: python3 scripts/test-packaging.py
Preview tests replace only the Go compiler/program boundary with a probe, so
these tests check script wiring, not the actual engine or daemon.
"""
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest
from brand_guard import (OLD_DESKTOP_FILE, OLD_ICON_FILE, OLD_PREVIEW_CLI, OLD_FETCH_SCRIPT, OLD_BRIDGE_BINARY, OLD_APP_BINARY, OLD_COMMAND_PATH, OLD_CLI_MANIFEST, OLD_ENV_PREFIX, OLD_PRODUCT_SLUG, OLD_ASSET_PREFIX, OLD_PROTOCOL_LIBRARY, OLD_EXTERNAL_CLI)

ROOT = Path(__file__).resolve().parent.parent


class PackagingTests(unittest.TestCase):
    def test_public_snapshot_contract(self):
        subprocess.run([sys.executable, str(ROOT / "scripts/test-public-snapshot.py")],
                       cwd=ROOT, check=True)

    def test_removed_external_cli_and_seed(self):
        for name in (
            OLD_CLI_MANIFEST, OLD_FETCH_SCRIPT,
            OLD_PREVIEW_CLI, "scripts/preview/seed.sql",
        ):
            with self.subTest(path=name):
                self.assertFalse((ROOT / name).exists())
        for name in (
            "scripts/build-linux.sh", "scripts/build-macos.sh",
            "scripts/build-windows.sh", "scripts/preview.sh",
            "scripts/release.sh", "scripts/notices.sh",
            "build/windows/installer.nsi",
        ):
            with self.subTest(path=name):
                text = (ROOT / name).read_text()
                self.assertNotIn(OLD_EXTERNAL_CLI, text.lower())
                self.assertNotIn(OLD_PROTOCOL_LIBRARY, text.lower())
                self.assertNotIn(OLD_COMMAND_PATH, text)
                self.assertNotIn(OLD_ENV_PREFIX, text)

    def test_packaged_bridge_and_release_cli(self):
        for platform in ("linux", "macos", "windows"):
            with self.subTest(platform=platform):
                text = (ROOT / f"scripts/build-{platform}.sh").read_text()
                self.assertIn("./cmd/telegram-mcp-bridge", text)
                self.assertIn("telegram-mcp-bridge", text)
                self.assertNotIn(OLD_BRIDGE_BINARY, text)
        installer = (ROOT / "build/windows/installer.nsi").read_text()
        self.assertIn('File "${SRC}\\telegram-mcp-bridge.exe"', installer)
        self.assertIn('Rename "$INSTDIR\\telegram-mcp-bridge.exe"', installer)
        self.assertIn('Delete /REBOOTOK "$INSTDIR\\telegram-mcp-bridge.exe"', installer)
        self.assertIn('Exec \'"$INSTDIR\\${EXE}"\'', installer)
        # The app is renamed aside and replaced with retries; a silent
        # install must never keep the old program under the new version.
        self.assertIn('Rename "$INSTDIR\\${EXE}" "$INSTDIR\\${EXE}.old"', installer)
        self.assertIn('SetOverwrite try', installer)
        self.assertIn('Goto replace_exe', installer)
        self.assertIn('SetErrorLevel 4', installer)
        self.assertLess(installer.index('Goto replace_exe'), installer.index('WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion"'))
        release = (ROOT / "scripts/release.sh").read_text()
        self.assertIn("./cmd/telegram-mcp\n", release)
        self.assertIn("bin=telegram-mcp;", release)
        self.assertIn('release_checksums "$OUT"', release)

    def test_operational_packaging_identities(self):
        with (ROOT / "build/darwin/Info.plist").open("rb") as stream:
            info = plistlib.load(stream)
        self.assertEqual(info["CFBundleExecutable"], "telegram-mcp")
        self.assertEqual(info["CFBundleName"], "Telegram MCP")
        self.assertEqual(info["CFBundleDisplayName"], "Telegram MCP")
        self.assertEqual(info["CFBundleIdentifier"], "com.bwoliveira.telegram-mcp")
        self.assertEqual(info["CFBundleShortVersionString"], "@VERSION@")
        self.assertIn("Bruno Orlandi", info["NSHumanReadableCopyright"])
        winres = json.loads((ROOT / "build/windows/winres.json").read_text())
        manifest = winres["RT_MANIFEST"]["#1"]["0409"]
        self.assertEqual(manifest["identity"]["name"], "com.bwoliveira.telegram-mcp")
        version = winres["RT_VERSION"]["#1"]["0000"]["info"]["0409"]
        self.assertEqual(version["InternalName"], "telegram-mcp")
        self.assertEqual(version["OriginalFilename"], "telegram-mcp.exe")
        self.assertEqual(version["ProductName"], "Telegram MCP")
        self.assertIn("Bruno Orlandi", version["LegalCopyright"])
        desktop = (ROOT / "build/linux/telegram-mcp.desktop").read_text()
        for entry in ("Name=Telegram MCP", "Exec=telegram-mcp", "Icon=telegram-mcp", "StartupWMClass=telegram-mcp"):
            self.assertIn(entry + "\n", desktop)
        self.assertFalse((ROOT / OLD_DESKTOP_FILE).exists())
        self.assertTrue((ROOT / "build/linux/telegram-mcp.png").is_file())
        self.assertFalse((ROOT / OLD_ICON_FILE).exists())
        icons = (ROOT / "scripts/icons.sh").read_text()
        self.assertIn('png build/icons/tile.svg "$size"', icons)
        self.assertNotIn("internal/brand/favicon.svg", icons)
        for script in ("icons.sh", "build-linux.sh"):
            self.assertNotIn(OLD_ICON_FILE, (ROOT / "scripts" / script).read_text())
        installer = (ROOT / "build/windows/installer.nsi").read_text()
        self.assertIn('!define EXE "telegram-mcp.exe"', installer)
        self.assertIn('InstallDir "$LOCALAPPDATA\\Programs\\telegram-mcp"', installer)
        self.assertIn('Uninstall\\TelegramMCP"', installer)
        self.assertIn('"com.bwoliveira.telegram-mcp"', installer)
        self.assertIn('"https://telegram-mcp.bwoliveira.xyz"', installer)
        for platform, expected in {
            "linux": ('-o "$work/telegram-mcp"', '"$deb/opt/telegram-mcp"',
                      'Package: telegram-mcp', '"$OUT/telegram-mcp_${VERSION}_${arch}.deb"',
                      'appimage="$OUT/Telegram-MCP-${appimage_arch}.AppImage"'),
            "macos": ('APP="$OUT/Telegram MCP.app"', '"$APP/Contents/MacOS/telegram-mcp"',
                      'DMG="$OUT/Telegram-MCP.dmg"'),
            "windows": ('"$stage/telegram-mcp.exe"', '"$PWD/$OUT/Telegram-MCP-Setup.exe"'),
        }.items():
            text = (ROOT / f"scripts/build-{platform}.sh").read_text()
            for value in expected:
                with self.subTest(platform=platform, value=value):
                    self.assertIn(value, text)
            self.assertNotIn(OLD_APP_BINARY, text)
            self.assertNotIn(OLD_ASSET_PREFIX, text)
        release = (ROOT / "scripts/release.sh").read_text()
        for name in ("Telegram-MCP.dmg", "Telegram-MCP-Setup.exe", "Telegram-MCP-*.AppImage",
                     "telegram-mcp_${os}_${arch}.zip", "telegram-mcp_${os}_${arch}.tar.gz"):
            self.assertIn(name, release)
        self.assertNotIn(OLD_PRODUCT_SLUG, release)
        for path in [*ROOT.glob("scripts/**/*.py"), *ROOT.glob("build/**/*.py")]:
            compile(path.read_bytes(), str(path), "exec")

    def test_single_product_name_in_sources(self):
        for directory in ("cmd", "internal", "build", "scripts"):
            for path in (ROOT / directory).rglob("*"):
                if not path.is_file() or path.name.endswith("_test.go") or path.name.startswith("test-"):
                    continue
                if path.suffix not in (".go", ".json", ".js", ".svg", ".desktop", ".plist", ".nsi", ".sh", ".py"):
                    continue
                text = path.read_text()
                for stale in ("TG MCP Local", "Telegram MCP Local", "para Telegram (não oficial)", "for Telegram (unofficial)", "para Telegram (no oficial)"):
                    with self.subTest(path=str(path.relative_to(ROOT)), stale=stale):
                        self.assertNotIn(stale, text)

    def test_no_external_cli_in_bundle_inputs(self):
        # Scan every packaging entry point and template, not the regression
        # tests themselves (which necessarily name the forbidden dependency).
        inputs = [*ROOT.glob("scripts/**/*.sh"), *ROOT.glob("build/**/*")]
        for path in inputs:
            if not path.is_file():
                continue
            with self.subTest(path=str(path.relative_to(ROOT))):
                self.assertNotIn(OLD_EXTERNAL_CLI, path.name.lower())
                if path.suffix in (".sh", ".py", ".json", ".plist", ".desktop", ".nsi"):
                    self.assertNotIn(OLD_EXTERNAL_CLI, path.read_text().lower())
        for platform in ("linux", "macos", "windows"):
            text = (ROOT / f"scripts/build-{platform}.sh").read_text()
            self.assertIn('work="$(mktemp -d)"', text)
            self.assertIn("./cmd/app", text)
            self.assertIn("./cmd/telegram-mcp-bridge", text)

    def test_release_is_restricted_to_private_fork(self):
        release = (ROOT / "scripts/release-common.sh").read_text()
        self.assertIn('REPOSITORY="bwoliveira/telegram-mcp-local"', release)
        self.assertIn('gh api "repos/$REPOSITORY" --jq .private', release)
        self.assertIn('if [ "$private" != true ]; then', release)
        self.assertNotIn("gh release create", release)
        self.assertNotIn("gh release edit", release)
        self.assertLess(release.index('if [ "$private" != true ]; then'),
                        release.index('mkdir -p "$OUT"'))
        self.assertIn('nothing was uploaded or published.', (ROOT / "scripts/release.sh").read_text())

    def test_notices_without_external_cli(self):
        result = subprocess.run(
            ["bash", str(ROOT / "scripts/notices.sh")], cwd=ROOT,
            check=True, text=True, capture_output=True,
        ).stdout
        for required in ("Bruno Orlandi", "Wails", "modernc.org/sqlite", "whisper.cpp", "FFmpeg"):
            self.assertIn(required, result)
        self.assertNotIn(OLD_EXTERNAL_CLI, result.lower())
        self.assertNotIn(OLD_PROTOCOL_LIBRARY, result.lower())

    def run_preview_probe(self, configured):
        with tempfile.TemporaryDirectory(prefix="telegram-packaging-") as folder:
            scratch = Path(folder)
            tools = scratch / "tools"
            tools.mkdir()
            go = tools / "go"
            go.write_text(f"#!{sys.executable}\n" + '''import os, pathlib, sys
assert sys.argv[1] == "build", sys.argv
assert sys.argv[-1] == "./cmd/telegram-mcp", sys.argv
out = pathlib.Path(sys.argv[sys.argv.index("-o") + 1])
assert out.name == "telegram-mcp", out
out.write_text("#!" + sys.executable + "\\n" + \'\'\'import json, os, sys
from pathlib import Path
Path(os.environ["PROBE_RESULT"]).write_text(json.dumps({
    "args": sys.argv[1:], "env": dict(os.environ),
}))
\'\'\')
out.chmod(0o755)
''')
            go.chmod(0o755)
            for name in (OLD_EXTERNAL_CLI, "sqlite3", "curl"):
                tool = tools / name
                tool.write_text("#!/bin/sh\nexit 97\n")
                tool.chmod(0o755)
            result_file = scratch / "result.json"
            env = dict(os.environ, PATH=f"{tools}:{os.environ['PATH']}",
                       TMPDIR=str(scratch), PROBE_RESULT=str(result_file),
                       TELEGRAM_MCP_ENGINE="must-be-overridden",
                       TELEGRAM_MCP_TOKEN="must-be-overridden",
                       TELEGRAM_MCP_DATA="must-be-overridden",
                       TELEGRAM_MCP_PORT="must-be-overridden")
            env.pop("PREVIEW_DIR", None)
            env.pop("PREVIEW_PORT", None)
            if configured:
                env["PREVIEW_DIR"] = os.path.relpath(scratch / "preview with spaces", ROOT)
                env["PREVIEW_PORT"] = "47901"
            subprocess.run(["bash", str(ROOT / "scripts/preview.sh")],
                           cwd=ROOT, env=env, capture_output=True,
                           text=True, check=True, timeout=20)
            result = json.loads(result_file.read_text())
            self.assertEqual(result["args"], ["serve"])
            runtime = result["env"]
            self.assertEqual(runtime["TELEGRAM_MCP_ENGINE"], "fake")
            self.assertRegex(runtime["TELEGRAM_MCP_TOKEN"], r"^[0-9a-f]{64}$")
            self.assertEqual(runtime["TELEGRAM_MCP_PORT"], "47901" if configured else "47890")
            data = Path(runtime["TELEGRAM_MCP_DATA"])
            self.assertTrue(data.is_dir())
            self.assertTrue(data.is_relative_to(scratch))
            self.assertTrue(data.is_absolute())
            token = data.parent / "token"
            self.assertEqual(token.read_text().strip(), runtime["TELEGRAM_MCP_TOKEN"])
            self.assertEqual(token.stat().st_mode & 0o777, 0o600)
            for key in ("HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"):
                self.assertTrue(Path(runtime[key]).is_relative_to(data.parent), key)
            self.assertEqual(Path(runtime["XDG_RUNTIME_DIR"]).stat().st_mode & 0o777, 0o700)
            self.assertFalse(list(scratch.rglob("*.db")))

    def test_preview_default_isolated_fake_engine(self):
        self.run_preview_probe(configured=False)

    def test_preview_relative_path_and_custom_port(self):
        self.run_preview_probe(configured=True)


if __name__ == "__main__":
    unittest.main(verbosity=2)
