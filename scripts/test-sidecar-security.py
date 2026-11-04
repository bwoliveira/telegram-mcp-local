#!/usr/bin/env python3
"""Regression checks for fail-closed build downloads and sidecar compliance."""
import hashlib
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]


class BuildInputSecurityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.pins = (ROOT / "scripts/sidecars/pins.sh").read_text()
        cls.whisper = (ROOT / "scripts/sidecars/build-whisper.sh").read_text()
        cls.ffmpeg = (ROOT / "scripts/sidecars/build-ffmpeg.sh").read_text()
        cls.package = (ROOT / "scripts/sidecars/package.sh").read_text()
        cls.windows = (ROOT / "scripts/build-windows.sh").read_text()

    def test_every_download_uses_shared_verifier(self):
        for text in (self.whisper, self.ffmpeg, self.package, self.windows):
            self.assertNotRegex(text, r"curl[^\n]*\|\s*(?:tar|sh|bash)")
            self.assertIn("download_verified", text)

    def test_pins_are_literal_sha256(self):
        names = [
            "WHISPER_SOURCE_SHA256", "WHISPER_WINDOWS_CPU_SHA256",
            "WHISPER_WINDOWS_CUDA_SHA256", "CUDA_12_4_EULA_SHA256",
            "FFMPEG_SOURCE_SHA256",
            "FFMPEG_SIGNATURE_SHA256", "WEBVIEW2_BOOTSTRAPPER_SHA256",
            "WEBVIEW2_SIGNER_CERT_SHA256",
        ]
        for name in names:
            match = re.search(rf'^{name}="([0-9a-f]{{64}})"$', self.pins, re.M)
            self.assertIsNotNone(match, name)

    def test_ffmpeg_requires_pgp_and_forbids_gpl(self):
        self.assertIn("FFMPEG_SIGNING_FINGERPRINT", self.ffmpeg)
        self.assertIn("[GNUPG:] VALIDSIG", self.ffmpeg)
        self.assertIn("enable-(gpl|nonfree)", self.ffmpeg)
        self.assertIn("FFmpeg-LGPL-2.1.txt", self.ffmpeg)
        self.assertIn("source.tar.xz", self.ffmpeg)
        self.assertIn("CONFIG_${setting} 0", self.ffmpeg)

    def run_ffmpeg_configuration(self, system="Linux", host="x86_64", arch="x86_64",
                                 cross="", config=None, buildconf="--disable-gpl"):
        # Execute the actual configure/build/license section with local build
        # doubles. No downloads, compiler, target execution or signing inputs.
        with tempfile.TemporaryDirectory(prefix="ffmpeg-config-") as directory:
            work = Path(directory)
            (work / "configure").write_text("#!/bin/sh\nexit 0\n")
            (work / "configure").chmod(0o755)
            (work / "ffmpeg").write_text(
                '#!/bin/sh\nprintf "executed\\n" >> "$WORK/executed"\n'
                'printf "%s\\n" "$TEST_BUILDCONF"\n')
            (work / "ffmpeg").chmod(0o755)
            (work / "config.h").write_text(config if config is not None else
                                          "#define CONFIG_GPL 0\n#define CONFIG_NONFREE 0\n")
            init = self.ffmpeg.split('CROSS="${2:-}"', 1)[1].split('WORK="$(mktemp -d)"', 1)[0]
            section = self.ffmpeg.split("flags=(", 1)[1].split('cp "$bin" "$OUT/"', 1)[0]
            shell = ('set -euo pipefail\n'
                     'uname() { case "$1" in -s) printf "%s\\n" "$TEST_SYSTEM";; '
                     '-m) printf "%s\\n" "$TEST_HOST";; esac; }\n'
                     'make() { :; }; strip() { :; }\n' + init + "flags=(" + section)
            env = dict(os.environ, WORK=str(work), ARCH=arch, CROSS=cross,
                       TEST_SYSTEM=system, TEST_HOST=host, TEST_BUILDCONF=buildconf)
            result = subprocess.run(["bash", "-c", shell], cwd=work, env=env,
                                    text=True, capture_output=True, timeout=10)
            return result, (work / "executed").exists(), (
                (work / "ffmpeg-license-config.txt").read_text()
                if (work / "ffmpeg-license-config.txt").exists() else "")

    def test_ffmpeg_native_executes_buildconf_but_cross_builds_do_not(self):
        for system, host, arch, cross, execute in (
                ("Linux", "x86_64", "x86_64", "", True),
                ("Linux", "x86_64", "x86_64", "x86_64-w64-mingw32-", False),
                ("Darwin", "arm64", "arm64", "", True),
                ("Darwin", "arm64", "x86_64", "", False),
                ("Darwin", "x86_64", "arm64", "", False)):
            with self.subTest(system=system, host=host, arch=arch, cross=cross):
                result, executed, proof = self.run_ffmpeg_configuration(system, host, arch, cross)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(executed, execute)
                self.assertEqual(proof, "#define CONFIG_GPL 0\n#define CONFIG_NONFREE 0\n")

    def test_ffmpeg_cross_builds_require_generated_lgpl_proof(self):
        for cross, system, arch in (("x86_64-w64-mingw32-", "Linux", "x86_64"),
                                    ("", "Darwin", "arm64")):
            for config in ("", "#define CONFIG_GPL 1\n#define CONFIG_NONFREE 0\n",
                           "#define CONFIG_GPL 0\n#define CONFIG_NONFREE 1\n"):
                with self.subTest(cross=cross, system=system, config=config):
                    result, executed, _ = self.run_ffmpeg_configuration(
                        system=system, arch=arch, cross=cross, config=config)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("is not proven disabled", result.stderr)
                    self.assertFalse(executed)

    def test_ffmpeg_native_rejects_forbidden_buildconf(self):
        for flag in ("--enable-gpl", "--enable-nonfree"):
            result, executed, _ = self.run_ffmpeg_configuration(buildconf=flag)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(executed)
            self.assertIn("forbidden GPL or nonfree", result.stderr)

    def test_package_requires_corresponding_source_and_sbom(self):
        for required in ("ffmpeg-build-config.txt", "ffmpeg-license-config.txt",
                         "FFmpeg-LGPL-2.1.txt", "source.tar.xz", "sbom.py",
                         "NVIDIA-CUDA-12.4-EULA-and-third-party-notices.pdf"):
            self.assertIn(required, self.package)

    def test_webview_uses_fixed_delivery_url_and_authenticode(self):
        self.assertNotIn("go.microsoft.com/fwlink", self.windows)
        self.assertIn("WEBVIEW2_BOOTSTRAPPER_SHA256", self.windows)
        self.assertIn("Get-AuthenticodeSignature", self.windows)
        self.assertIn("osslsigncode", self.windows)

    def test_sidecar_sbom_is_cyclonedx_16(self):
        text = (ROOT / "scripts/sidecars/sbom.py").read_text()
        self.assertIn('"specVersion": "1.6"', text)
        compile(text, "sbom.py", "exec")

    def test_inventory_script_is_valid_python(self):
        text = (ROOT / "scripts/build-inventory.py").read_text()
        compile(text, "build-inventory.py", "exec")

    def test_sidecar_manifest_requires_sbom_and_notices_for_every_archive(self):
        archives = (
            "transcription_darwin_universal.tar.gz",
            "transcription_linux_amd64.tar.gz",
            "transcription_linux_arm64.tar.gz",
            "transcription_windows_amd64.zip",
            "transcription_windows_amd64_cuda.zip",
        )
        with tempfile.TemporaryDirectory(prefix="sidecar-manifest-") as directory:
            root = Path(directory)
            script_dir = root / "scripts/sidecars"
            script_dir.mkdir(parents=True)
            (root / "internal/sidecar").mkdir(parents=True)
            script = script_dir / "manifest.py"
            script.write_text((ROOT / "scripts/sidecars/manifest.py").read_text())
            output = root / "release"
            output.mkdir()
            payload = root / "payload"
            payload.mkdir()
            legal = {
                "whisper.cpp-LICENSE.txt": "whisper full license\n",
                "FFmpeg-LGPL-2.1.txt": "ffmpeg 2.1 full license\n",
                "FFmpeg-LGPL-3.txt": "ffmpeg 3 full license\n",
            }
            for name, text in {**legal, "whisper-cli": "binary", "ffmpeg": "binary"}.items():
                (payload / name).write_text(text)
            notice = "Telegram MCP transcription sidecar\n"
            for name, text in legal.items():
                notice += f"\n===== {name} =====\n\n{text}\n"
            notice += "\nCorresponding FFmpeg source and build provenance are packaged in the archive.\n"
            archive_script = ROOT / "scripts/sidecars/archive.py"
            sbom_script = ROOT / "scripts/sidecars/sbom.py"
            for name in archives:
                subprocess.run([sys.executable, str(archive_script), "--format",
                                "zip" if name.endswith(".zip") else "tar.gz",
                                "--payload-dir", str(payload), "--output", str(output / name)], check=True)
                subprocess.run([sys.executable, str(sbom_script), "--artifact", str(output / name),
                                "--payload-dir", str(payload), "--output", str(output / (name + ".cdx.json"))], check=True)
                (output / (name + ".THIRD-PARTY-NOTICES.txt")).write_text(notice)
            missing = output / (archives[0] + ".cdx.json")
            valid_sbom = missing.read_bytes()
            missing.unlink()
            refused = subprocess.run([sys.executable, str(script), "sidecars-2", str(output)],
                                     text=True, capture_output=True)
            self.assertNotEqual(refused.returncode, 0)
            self.assertIn("missing required release file", refused.stderr)
            missing.write_bytes(valid_sbom)
            stale_archive = output / archives[0]
            stale_archive.write_bytes(stale_archive.read_bytes() + b"stale")
            stale = subprocess.run([sys.executable, str(script), "sidecars-2", str(output)],
                                   text=True, capture_output=True)
            self.assertNotEqual(stale.returncode, 0)
            self.assertIn("SBOM root does not bind", stale.stderr)
            subprocess.run([sys.executable, str(archive_script), "--format", "tar.gz",
                            "--payload-dir", str(payload), "--output", str(stale_archive)], check=True)
            accepted = subprocess.run([sys.executable, str(script), "sidecars-2", str(output)],
                                      text=True, capture_output=True)
            self.assertEqual(accepted.returncode, 0, accepted.stdout + accepted.stderr)
            manifest = json.loads((output / "manifest.json").read_text())
            self.assertEqual(manifest["version"], "sidecars-2")
            for package in manifest["packages"].values():
                for key in ("sbom", "notices"):
                    metadata = package[key]
                    body = (output / metadata["url"].rsplit("/", 1)[-1]).read_bytes()
                    self.assertEqual(metadata["size"], len(body))
                    self.assertEqual(metadata["sha256"], hashlib.sha256(body).hexdigest())

    def test_model_is_in_curated_notices(self):
        notices = (ROOT / "THIRD_PARTY_NOTICES.md").read_text()
        self.assertIn("ggml-large-v3-turbo-q5_0.bin", notices)
        self.assertIn("394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2", notices)
        self.assertIn("Copyright (c) 2022 OpenAI", notices)


if __name__ == "__main__":
    unittest.main()
