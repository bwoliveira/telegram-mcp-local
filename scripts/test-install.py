#!/usr/bin/env python3
"""Offline installer contract: synthetic releases, isolated HOME, no sudo."""
import functools
import hashlib
import http.server
import io
import json
import os
import re
import sys
import shutil
from pathlib import Path
import subprocess
import tarfile
import tempfile
import threading
import unittest

INSTALLER = Path(__file__).resolve().parents[1] / "install.sh"
# Production trust anchor pinned in install.sh and published in
# docs/security/release-signing.md (minisign key ID CF15D44BF3EA0B47).
PRODUCTION_PUBLIC_KEY = "RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB"
PRODUCTION_RELEASE_VERSION = "1.0.0"
GITHUB_BASE = "https://github.com/bwoliveira/telegram-mcp-local/releases/latest/download"


def repin_installer(text, public_key, release_version):
    """Return installer text pinned to another key/version, or fail loudly.

    Each pinned assignment must exist exactly once, so a structural change to
    install.sh cannot make a test silently exercise the production pin.
    """
    text, keys = re.subn(r"(?m)^public_key=" + re.escape(PRODUCTION_PUBLIC_KEY) + "$",
                         lambda _: "public_key=" + public_key, text)
    text, versions = re.subn(r"(?m)^release_version=" + re.escape(PRODUCTION_RELEASE_VERSION) + "$",
                             lambda _: "release_version=" + release_version, text)
    if (keys, versions) != (1, 1):
        raise AssertionError(f"install.sh pins changed: keys={keys} versions={versions}")
    return text


class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, format, *args):
        pass


class InstallTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture_tools = None
        cls.minisign = "" if os.environ.get("MINISIGN_FORCE_FIXTURE") == "1" else (shutil.which("minisign") or "")
        if not cls.minisign:
            cls.fixture_tools = tempfile.TemporaryDirectory()
            cls.minisign = str(Path(cls.fixture_tools.name) / "minisign")
            subprocess.run(["go", "build", "-o", cls.minisign,
                            "./scripts/testdata/minisign-fixture"],
                           cwd=INSTALLER.parent, check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        if cls.fixture_tools:
            cls.fixture_tools.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.minisign = self.__class__.minisign
        self.secret_key = self.root / "test.key"
        public_file = self.root / "test.pub"
        subprocess.run([self.minisign, "-G", "-W", "-s", str(self.secret_key),
                        "-p", str(public_file)], check=True, capture_output=True)
        self.public_key = public_file.read_text().splitlines()[1]
        self.releases = self.root / "releases"
        self.releases.mkdir()
        self.home = self.root / "home"
        self.home.mkdir()
        self.tools = self.root / "tools"
        self.tools.mkdir()
        (self.tools / "minisign").symlink_to(self.minisign)
        handler = functools.partial(QuietHandler, directory=str(self.releases))
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_server)
        self.base = f"http://127.0.0.1:{self.server.server_port}"

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def release(self, system="linux", arch="amd64", checksum=None):
        asset = f"telegram-mcp_{system}_{arch}.tar.gz"
        with tarfile.open(self.releases / asset, "w:gz") as archive:
            for name, content in [("telegram-mcp", b"#!/bin/sh\nprintf 'fixture-cli\\n'\n"),
                                  ("THIRD-PARTY-NOTICES.txt", b"Synthetic license notice\n")]:
                info = tarfile.TarInfo(name)
                info.size = len(content)
                info.mode = 0o755 if name == "telegram-mcp" else 0o644
                archive.addfile(info, io.BytesIO(content))
        digest = hashlib.sha256((self.releases / asset).read_bytes()).hexdigest()
        manifest = ("# version: 1.2.3\n"
                    "# source_commit: " + "a" * 40 + "\n"
                    "# date: 2026-11-03T12:00:00Z\n"
                    f"{checksum or digest}  {asset}\n")
        (self.releases / "checksums.txt").write_text(manifest)

        self.sign_manifest()

    def sign_manifest(self):
        subprocess.run([self.minisign, "-S", "-l", "-s", str(self.secret_key), "-m",
                        str(self.releases / "checksums.txt")], check=True, capture_output=True)

    def run_install(self, system="Linux", arch="x86_64", args=(), extra_env=None, installer=INSTALLER):
        uname = self.tools / "uname"
        uname.write_text(f'#!/bin/sh\ncase "$1" in -s) printf "%s\\n" "{system}";; -m) printf "%s\\n" "{arch}";; *) exit 1;; esac\n')
        uname.chmod(0o755)
        env = dict(os.environ, HOME=str(self.home), PATH=f"{self.tools}:/usr/bin:/bin",
                   TELEGRAM_MCP_RELEASE_BASE_URL=self.base, TMPDIR=str(self.root),
                   TELEGRAM_MCP_MINISIGN_PUBLIC_KEY=self.public_key,
                   TELEGRAM_MCP_RELEASE_VERSION="1.2.3")
        env.update(extra_env or {})
        return subprocess.run(["/bin/sh", str(installer), *args], env=env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15)

    def test_missing_signature_rejected(self):
        self.release()
        (self.releases / "checksums.txt.minisig").unlink()
        result = self.run_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature", result.stderr.lower())
        self.assertFalse((self.home / ".local/bin").exists())

    def test_minisign_required(self):
        # A hermetic PATH has every dependency except minisign, even on hosts
        # where it is normally installed. No OpenSSL fallback is permitted.
        without_minisign = self.root / "tools-without-minisign"
        without_minisign.mkdir()
        (without_minisign / "uname").symlink_to(self.tools / "uname")
        for name in ("curl", "tar", "sha256sum", "mktemp", "rm", "awk", "mkdir",
                     "cp", "chmod", "mv", "openssl"):
            found = shutil.which(name)
            if found:
                (without_minisign / name).symlink_to(found)
        result = self.run_install(extra_env={"PATH": str(without_minisign)})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("minisign is required", result.stderr)
        self.assertIn("install", result.stderr.lower())
        self.assertFalse((self.home / ".local/bin").exists())

    def test_invalid_signature_preserves_existing_install(self):
        self.release()
        target = self.home / ".local/bin/telegram-mcp"
        target.parent.mkdir(parents=True)
        target.write_text("existing executable")
        (self.releases / "checksums.txt.minisig").write_text("invalid signature\n")
        result = self.run_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature verification failed", result.stderr)
        self.assertEqual(target.read_text(), "existing executable")

    def test_wrong_public_key_rejected(self):
        self.release()
        other_public = self.root / "other.pub"
        subprocess.run([self.minisign, "-G", "-W", "-s", str(self.root / "other.key"),
                        "-p", str(other_public)], check=True, capture_output=True)
        result = self.run_install(extra_env={
            "TELEGRAM_MCP_MINISIGN_PUBLIC_KEY": other_public.read_text().splitlines()[1]})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature verification failed", result.stderr)
        self.assertFalse((self.home / ".local/bin").exists())

    def test_empty_fixture_key_falls_back_to_pinned_production_key(self):
        self.release()
        result = self.run_install(extra_env={"TELEGRAM_MCP_MINISIGN_PUBLIC_KEY": ""})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature verification failed", result.stderr)
        self.assertFalse((self.home / ".local/bin").exists())

    def test_tampered_manifest_verified_before_parsing(self):
        self.release()
        # Invalid syntax would otherwise fail during checksum parsing.
        (self.releases / "checksums.txt").write_text("unsigned tampered manifest\n")
        result = self.run_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature verification failed", result.stderr)
        self.assertFalse((self.home / ".local/bin").exists())

    def install_github_curl(self):
        # Capture argv at the curl boundary. HTTPS fixture downloads are copied
        # locally; the local HTTP case still uses the real curl and HTTP server.
        curl = self.tools / "curl"
        curl.write_text(f'''#!{sys.executable}
import json, os, pathlib, shutil, sys
args = sys.argv[1:]
with open(os.environ["CURL_LOG"], "a") as log:
    log.write(json.dumps(args) + "\\n")
if args[-1].startswith("https://github.com/"):
    shutil.copyfile(pathlib.Path(os.environ["FIXTURE_RELEASES"]) / args[-1].rsplit("/", 1)[1], args[args.index("--output") + 1])
else:
    os.execv({shutil.which("curl")!r}, ["curl", *args])
''')
        curl.chmod(0o755)

    def run_github_install(self, installer=INSTALLER, extra_env=None):
        """Run an install whose base is the real HTTPS GitHub URL (served offline)."""
        self.install_github_curl()
        log = self.root / "curl.jsonl"
        log.write_text("")
        env = {"TELEGRAM_MCP_RELEASE_BASE_URL": GITHUB_BASE, "CURL_LOG": str(log),
               "FIXTURE_RELEASES": str(self.releases)}
        env.update(extra_env or {})
        return self.run_install(extra_env=env, installer=installer)

    def test_tracked_installer_pins_production_key_and_version(self):
        text = INSTALLER.read_text()
        self.assertNotIn("REPLACE_WITH_PRODUCTION", text)
        self.assertEqual(re.findall(r"(?m)^\s*public_key=([^\s#]+)\s*$", text),
                         [PRODUCTION_PUBLIC_KEY, "${TELEGRAM_MCP_MINISIGN_PUBLIC_KEY:-$public_key}"])
        self.assertEqual(re.findall(r"(?m)^\s*release_version=([^\s#]+)\s*$", text),
                         [PRODUCTION_RELEASE_VERSION])

    def test_network_release_cannot_replace_pinned_production_key(self):
        # A release signed by any other key fails over HTTPS even when the
        # environment names that key and version: only loopback fixtures
        # honor the override, so the tracked installer trusts only production.
        self.release()
        result = self.run_github_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature verification failed", result.stderr)
        self.assertFalse((self.home / ".local/bin").exists())

    def test_placeholder_installer_fails_closed(self):
        self.release()
        placeholder = self.root / "install-placeholder.sh"
        placeholder.write_text(repin_installer(INSTALLER.read_text(),
                                               "REPLACE_WITH_PRODUCTION_MINISIGN_PUBLIC_KEY",
                                               "REPLACE_WITH_PRODUCTION_RELEASE_VERSION"))
        result = self.run_github_install(installer=placeholder)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature verification failed", result.stderr)
        self.assertFalse((self.home / ".local/bin").exists())

    def test_curl_transport_flags(self):
        self.release()
        self.install_github_curl()
        pinned_installer = self.root / "install-pinned.sh"
        pinned_installer.write_text(repin_installer(INSTALLER.read_text(), self.public_key, "1.2.3"))
        for base, protocol in [(GITHUB_BASE, "=https"),
                               (self.base, "=http,https")]:
            with self.subTest(base=base):
                log = self.root / "curl.jsonl"
                log.write_text("")
                result = self.run_install(extra_env={
                    "TELEGRAM_MCP_RELEASE_BASE_URL": base, "CURL_LOG": str(log),
                    "FIXTURE_RELEASES": str(self.releases)}, installer=pinned_installer)
                self.assertEqual(result.returncode, 0, result.stderr)
                calls = [json.loads(line) for line in log.read_text().splitlines()]
                self.assertEqual(len(calls), 3)
                self.assertEqual({call[-1].rsplit("/", 1)[1] for call in calls},
                                 {"telegram-mcp_linux_amd64.tar.gz", "checksums.txt", "checksums.txt.minisig"})
                for call in calls:
                    self.assertIn("--proto", call)
                    self.assertEqual(call[call.index("--proto") + 1], protocol)
                    self.assertEqual(call[call.index("--proto-redir") + 1], "=https")
                    self.assertIn("--tlsv1.2", call)
                    self.assertEqual(call[0], "-q")

    def test_default_install_all_platforms(self):
        for system, uname, arch, machine in [("linux", "Linux", "amd64", "x86_64"),
                                             ("linux", "Linux", "arm64", "aarch64"),
                                             ("darwin", "Darwin", "amd64", "x86_64"),
                                             ("darwin", "Darwin", "arm64", "arm64")]:
            with self.subTest(system=system, arch=arch):
                self.release(system, arch)
                result = self.run_install(uname, machine)
                self.assertEqual(result.returncode, 0, result.stderr)
                binary = self.home / ".local/bin/telegram-mcp"
                self.assertEqual(subprocess.check_output([str(binary)], text=True), "fixture-cli\n")
                self.assertEqual((binary.parent / "THIRD-PARTY-NOTICES.txt").read_text(), "Synthetic license notice\n")

    def test_custom_prefix(self):
        self.release()
        prefix = self.root / "custom path"
        result = self.run_install(args=("--prefix", str(prefix)))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((prefix / "telegram-mcp").is_file())
        self.assertFalse((self.home / ".local/bin").exists())

    def test_signed_older_release_is_rejected_by_pinned_installer_version(self):
        self.release()
        result = self.run_install(extra_env={"TELEGRAM_MCP_RELEASE_VERSION": "1.2.4"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("replayed or unexpected", result.stderr)
        self.assertFalse((self.home / ".local/bin").exists())

    def test_remote_host_cannot_use_loopback_http_or_override_key(self):
        self.release()
        for base in [
            f"http://localhost:1234@127.0.0.1:{self.server.server_port}",
            f"http://127.0.0.1:{self.server.server_port}@evil.example/releases",
            "http://evil.example:8080/releases",
            "https://evil.example/releases",
        ]:
            with self.subTest(base=base):
                result = self.run_install(extra_env={"TELEGRAM_MCP_RELEASE_BASE_URL": base})
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("release base", result.stderr.lower())
                self.assertFalse((self.home / ".local/bin").exists())

    def test_bad_checksum_preserves_existing_binary(self):
        self.release(checksum="0" * 64)
        target = self.home / ".local/bin/telegram-mcp"
        target.parent.mkdir(parents=True)
        target.write_text("existing executable")
        result = self.run_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", result.stderr.lower())
        self.assertEqual(target.read_text(), "existing executable")

    def test_missing_malformed_duplicate_checksums(self):
        self.release()
        manifest = self.releases / "checksums.txt"
        valid = manifest.read_text()
        for value in ["", valid + valid, "bad-hash  telegram-mcp_linux_amd64.tar.gz\n",
                      "0" * 64 + "  another-file.tar.gz\n"]:
            with self.subTest(manifest=value):
                manifest.write_text(value)
                self.sign_manifest()
                result = self.run_install()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("checksum", result.stderr.lower())
                self.assertFalse((self.home / ".local/bin").exists())

    def test_missing_archive_notice(self):
        self.release()
        asset = self.releases / "telegram-mcp_linux_amd64.tar.gz"
        with tarfile.open(asset, "w:gz") as archive:
            info = tarfile.TarInfo("telegram-mcp")
            info.size = 1
            archive.addfile(info, io.BytesIO(b"x"))
        digest = hashlib.sha256(asset.read_bytes()).hexdigest()
        (self.releases / "checksums.txt").write_text(
            "# version: 1.2.3\n# source_commit: " + "a" * 40 +
            "\n# date: 2026-11-03T12:00:00Z\n" + f"{digest}  {asset.name}\n")
        self.sign_manifest()
        result = self.run_install()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("archive", result.stderr.lower())
        self.assertFalse((self.home / ".local/bin").exists())

    def test_unsupported_architecture(self):
        result = self.run_install(arch="riscv64")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unsupported", result.stderr.lower())
        self.assertFalse((self.home / ".local/bin").exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
