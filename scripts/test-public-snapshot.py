#!/usr/bin/env python3
"""Offline snapshot contract tests. Scanner doubles test wiring, not detection."""
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import zipfile
import zlib

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts/public-snapshot.py"
SPEC = importlib.util.spec_from_file_location("public_snapshot", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
PUBLIC_SNAPSHOT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PUBLIC_SNAPSHOT)


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="public-snapshot-test-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.repo = self.base / "repo"
        self.repo.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "Synthetic Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        (self.repo / "release").mkdir()
        (self.repo / "release/public-exclude.txt").write_text("private/\n")
        (self.repo / "release/public-patterns.json").write_text(json.dumps({"patterns": {"private-home": r"/root[/]\\.hermes"}}))
        self.deny = self.base / "private-terms.txt"
        self.deny.write_text("synthetic-private-peer\n")
        self.scanner = self.base / "scanner"
        self.scanner.write_text("#!/usr/bin/env python3\nimport sys,json\nfrom pathlib import Path\nif 'version' in sys.argv: print('test-double'); sys.exit(0)\nassert sys.argv[1] == 'dir'\np=Path(sys.argv[sys.argv.index('--report-path')+1]); p.write_text('[]')\n")
        self.scanner.chmod(0o755)
        self.zbar = self.base / "zbarimg"
        self.zbar.write_text("""#!/usr/bin/env python3
import sys
from pathlib import Path
if '--version' in sys.argv:
    print('0.23.93')
    raise SystemExit(0)
p = Path(sys.argv[-1])
data = p.read_bytes()
if b'PUBLIC_SNAPSHOT_ZBAR_SELF_TEST' in data:
    print('public-snapshot-zbar-self-test')
    raise SystemExit(0)
if b'SYNTHETIC_BARCODE' in data:
    print('synthetic-barcode-payload')
    raise SystemExit(0)
raise SystemExit(4)
""")
        self.zbar.chmod(0o755)
        self.tesseract = self.base / "tesseract"
        self.tesseract.write_text("""#!/usr/bin/env python3
import sys
from pathlib import Path
if '--version' in sys.argv:
    print('tesseract 5.5.0')
    raise SystemExit(0)
data = Path(sys.argv[1]).read_bytes()
marker = b'SYNTHETIC_OCR:'
if marker in data:
    print(data.split(marker, 1)[1].decode('utf-8'))
raise SystemExit(0)
""")
        self.tesseract.chmod(0o755)
        self.env = dict(os.environ, PUBLIC_SNAPSHOT_DENY_FILE=str(self.deny),
                        GITLEAKS_BIN=str(self.scanner),
                        PUBLIC_SNAPSHOT_ZBARIMG=str(self.zbar),
                        PUBLIC_SNAPSHOT_TESSERACT=str(self.tesseract))
        self.put("release/reviewed-images.txt", "# path<TAB>sha256<TAB>reviewed-on<TAB>reviewer\n")
        self.put("release/reviewed-opaque.txt", "# path<TAB>sha256<TAB>justification\n")
        self.put("README.md", "Public fixture\n")
        self.put("private/account.txt", "synthetic-private-peer\n")
        self.commit()

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], stderr=subprocess.STDOUT).decode().strip()

    def put(self, path, content):
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)

    def commit(self):
        self.put("release/public-scanners.json", json.dumps({
            "gitleaks": {"version": "test-double", "sha256": hashlib.sha256(self.scanner.read_bytes()).hexdigest()},
            "zbarimg": {"version": "0.23.93"},
            "tesseract": {"version": "5.5.0"},
        }))
        self.git("add", ".")
        self.git("commit", "-qm", "test: synthetic snapshot fixture")

    def run_snapshot(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), "--repo", str(self.repo), "--ref", "HEAD", "--output", str(self.base / "out"), *args], env=self.env, capture_output=True, text=True)

    def test_archive_excludes_private_files_and_untracked_state(self):
        self.put("untracked.txt", "synthetic-private-peer")
        self.put("README.md", "Uncommitted private change")
        result = self.run_snapshot()
        self.assertEqual(0, result.returncode, result.stderr)
        out = self.base / "out"
        manifest = json.loads((out / "manifest.json").read_text())
        self.assertEqual("passed", manifest["status"])
        self.assertEqual("Public fixture\n", (out / "tree/README.md").read_text())
        self.assertFalse((out / "tree/private").exists())
        self.assertFalse((out / "tree/untracked.txt").exists())
        self.assertFalse((out / "tree/.git").exists())
        entry = next(x for x in manifest["included"] if x["path"] == "README.md")
        self.assertEqual(hashlib.sha256(b"Public fixture\n").hexdigest(), entry["sha256"])
        self.assertEqual(["private/account.txt"], [x["path"] for x in manifest["excluded"]])

    def test_private_term_blocks_without_echoing_value(self):
        self.put("leak.txt", "synthetic-private-peer")
        self.commit()
        result = self.run_snapshot()
        self.assertEqual(1, result.returncode)
        self.assertNotIn("synthetic-private-peer", result.stdout + result.stderr)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("private-inventory", manifest["personal_data"]["failures"][0]["category"])

    def test_missing_empty_and_in_checkout_inventory_fail(self):
        for variant in ("missing", "empty", "inside"):
            with self.subTest(variant=variant):
                if variant == "missing":
                    self.env.pop("PUBLIC_SNAPSHOT_DENY_FILE", None)
                elif variant == "empty":
                    self.env["PUBLIC_SNAPSHOT_DENY_FILE"] = str(self.deny)
                    self.deny.write_text("# no terms\n")
                else:
                    self.env["PUBLIC_SNAPSHOT_DENY_FILE"] = str(self.repo / "README.md")
                self.assertEqual(2, self.run_snapshot().returncode)
                self.assertFalse((self.base / "out").exists())

    def test_gitleaks_failure_is_not_success(self):
        self.scanner.write_text("#!/bin/sh\nif [ \"$1\" = version ]; then printf 'test-double'; exit 0; fi\nexit 1\n")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("failed", manifest["gitleaks"]["status"])

    def test_existing_output_and_overlap_are_rejected(self):
        (self.base / "out").mkdir()
        sentinel = self.base / "out/sentinel"
        sentinel.write_text("keep me")
        self.assertEqual(2, self.run_snapshot().returncode)
        self.assertEqual("keep me", sentinel.read_text())
        self.assertEqual(2, self.run_snapshot("--output", str(self.repo / "output")).returncode)
        self.assertFalse((self.repo / "output").exists())

    def test_symlinks_fail_closed(self):
        (self.repo / "link").symlink_to("README.md")
        self.commit()
        self.assertEqual(2, self.run_snapshot().returncode)

    def test_source_policy_not_dirty_policy_is_used(self):
        (self.repo / "release/public-exclude.txt").write_text("README.md\n")
        result = self.run_snapshot()
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertTrue((self.base / "out/tree/README.md").exists())
        self.assertFalse((self.base / "out/tree/private").exists())

    def test_overlays_are_hashed_and_count_checked(self):
        rule = [{"name": "synthetic-rewrite", "path": "README.md", "pattern": "Public", "replacement": "Reviewed", "count": 1}]
        self.put("release/public-overlays.json", json.dumps(rule))
        self.commit()
        result = self.run_snapshot()
        self.assertEqual(0, result.returncode, result.stderr)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual(1, manifest["counts"]["overlays"])
        self.assertNotEqual(manifest["overlays"][0]["before_sha256"], manifest["overlays"][0]["after_sha256"])
        shutil.rmtree(self.base / "out")
        self.put("README.md", "changed upstream")
        self.commit()
        self.assertEqual(2, self.run_snapshot().returncode)

    def test_generic_patterns_and_email_check(self):
        shutil.copyfile(ROOT / "release/public-patterns.json", self.repo / "release/public-patterns.json")
        # Build private examples from parts so the test source is safe to export.
        text = "\n".join(("/root/" + ".hermes/cache", "/Users/" + "fixture/data", "10." + "4.3.2", "100." + "64.3.2", "home" + "lab-ci", "CT" + "999", "host" + ".internal", "fixture@" + "mail.example.com"))
        self.put("leaks.txt", text)
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual(8, len(manifest["personal_data"]["failures"]))

    def test_utf16_binary_is_scanned(self):
        (self.repo / "payload.bin").write_bytes("synthetic-private-peer".encode("utf-16-le"))
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)

    def test_qr_detection_and_decoder_errors_fail_closed(self):
        self.reviewed_image("image.png", marker=b"SYNTHETIC_BARCODE")
        self.commit()
        result = self.run_snapshot()
        self.assertEqual(1, result.returncode)
        self.assertNotIn("synthetic-barcode-payload", result.stdout + result.stderr)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("decodable-barcode", manifest["barcodes"]["failures"][0]["category"])
        shutil.rmtree(self.base / "out")
        self.zbar.write_text("""#!/usr/bin/env python3
import sys
from pathlib import Path
if '--version' in sys.argv:
    print('0.23.93')
    raise SystemExit(0)
if b'PUBLIC_SNAPSHOT_ZBAR_SELF_TEST' in Path(sys.argv[-1]).read_bytes():
    print('public-snapshot-zbar-self-test')
    raise SystemExit(0)
raise SystemExit(3)
""")
        self.assertEqual(1, self.run_snapshot().returncode)

    def test_replace_refs_do_not_change_the_selected_commit(self):
        original = self.git("rev-parse", "HEAD")
        self.put("README.md", "Unreviewed replacement\n")
        self.commit()
        replacement = self.git("rev-parse", "HEAD")
        self.git("replace", original, replacement)
        result = self.run_snapshot("--ref", original)
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual("Public fixture\n", (self.base / "out/tree/README.md").read_text())

    def test_local_export_attributes_cannot_change_or_omit_blobs(self):
        self.put("payload.txt", "$Format:%s$\n")
        self.commit()
        attributes = self.repo / ".git/info/attributes"
        for rule in ("payload.txt export-subst\n", "payload.txt export-ignore\n"):
            with self.subTest(rule=rule):
                attributes.write_text(rule)
                self.assertEqual(2, self.run_snapshot().returncode)
                manifest = json.loads((self.base / "out/manifest.json").read_text())
                self.assertEqual("failed", manifest["status"])
                shutil.rmtree(self.base / "out")

    def test_noop_or_unpinned_gitleaks_is_rejected(self):
        self.env["GITLEAKS_BIN"] = "/bin/true"
        self.assertEqual(2, self.run_snapshot().returncode)
        shutil.rmtree(self.base / "out")
        self.env["GITLEAKS_BIN"] = str(self.scanner)
        self.scanner.write_text("#!/bin/sh\nprintf 'test-double'\n")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertFalse(manifest["gitleaks"]["report_valid"])

    def test_unicode_normalization_and_non_ascii_utf16(self):
        self.deny.write_text("Jos\u00e9\n\u79d8\u5bc6\n")
        self.put("name.txt", "Jose\u0301")
        (self.repo / "binary.bin").write_bytes("\u79d8\u5bc6".encode("utf-16-le"))
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual({"name.txt", "binary.bin"}, {x["path"] for x in manifest["personal_data"]["failures"]})

    def test_tiff_images_are_scanned_by_magic(self):
        self.reviewed_image("image.dat", format="TIFF", marker=b"SYNTHETIC_BARCODE")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual(1, manifest["barcodes"]["scanned"])

    def test_synthetic_exception_is_exact_path_and_category_scoped(self):
        line = "host" + ".internal"
        digest = hashlib.sha256(line.encode()).hexdigest()
        self.put("release/public-patterns.json", json.dumps({"patterns": {"host": r"host[.]internal"}, "approved_synthetic_lines": {"fixture.txt": {digest: ["host"]}}}))
        self.put("fixture.txt", line)
        self.commit()
        self.assertEqual(0, self.run_snapshot().returncode)
        shutil.rmtree(self.base / "out")
        self.put("unreviewed.txt", line)
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        shutil.rmtree(self.base / "out")
        (self.repo / "unreviewed.txt").unlink()
        self.deny.write_text(line)
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)

    @staticmethod
    def png_chunk(kind, payload):
        return (struct.pack(">I", len(payload)) + kind + payload
                + struct.pack(">I", zlib.crc32(kind + payload) & 0xffffffff))

    @classmethod
    def png_bytes(cls, metadata=()):
        ihdr = struct.pack(">IIBBBBB", 1, 1, 8, 2, 0, 0, 0)
        chunks = [(b"IHDR", ihdr), *metadata,
                  (b"IDAT", zlib.compress(b"\x00\x00\x00\x00")), (b"IEND", b"")]
        return b"\x89PNG\r\n\x1a\n" + b"".join(cls.png_chunk(*chunk) for chunk in chunks)

    @staticmethod
    def jpeg_segment(marker, payload):
        return b"\xff" + bytes([marker]) + struct.pack(">H", len(payload) + 2) + payload

    @classmethod
    def jpeg_bytes(cls, metadata=()):
        app0 = cls.jpeg_segment(0xe0, b"JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")
        return b"\xff\xd8" + app0 + b"".join(metadata) + b"\xff\xd9"

    @staticmethod
    def webp_chunk(kind, payload):
        return kind + struct.pack("<I", len(payload)) + payload + (b"\x00" if len(payload) % 2 else b"")

    @classmethod
    def webp_bytes(cls, flags, metadata=()):
        vp8x = bytes([flags]) + b"\x00\x00\x00" + b"\x00\x00\x00" + b"\x00\x00\x00"
        body = cls.webp_chunk(b"VP8X", vp8x) + b"".join(metadata) + cls.webp_chunk(b"VP8 ", b"fixture")
        return b"RIFF" + struct.pack("<I", len(body) + 4) + b"WEBP" + body

    def reviewed_image(self, path, *, format="PNG", marker=b""):
        signatures = {
            "PNG": self.png_bytes(((b"sYnt", marker),)),
            "WEBP": self.webp_bytes(0, (self.webp_chunk(b"FICT", marker),)),
            "TIFF": b"II*\x00SYNTHETIC" + marker,
        }
        data = signatures[format]
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        digest = hashlib.sha256(data).hexdigest()
        policy = self.repo / "release/reviewed-images.txt"
        with policy.open("a") as handle:
            handle.write(f"{path}\t{digest}\t2026-11-03\tSynthetic Fixture\n")
        return target

    def test_missing_or_broken_zbar_fails_before_approval(self):
        self.env["PUBLIC_SNAPSHOT_ZBARIMG"] = str(self.base / "missing-zbarimg")
        result = self.run_snapshot()
        self.assertEqual(2, result.returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("failed", manifest["status"])
        shutil.rmtree(self.base / "out")
        self.zbar.write_text("#!/bin/sh\nexit 9\n")
        self.assertEqual(2, self.run_snapshot().returncode)

    def test_image_magic_not_extension_drives_barcode_scan(self):
        self.reviewed_image("private-telegram-id.dat", marker=b"SYNTHETIC_BARCODE")
        self.commit()
        result = self.run_snapshot()
        self.assertEqual(1, result.returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual(1, manifest["barcodes"]["scanned"])
        self.assertEqual("decodable-barcode", manifest["barcodes"]["failures"][0]["category"])

    def test_webp_with_barcode_is_scanned_by_magic(self):
        self.reviewed_image("asset.bin", format="WEBP", marker=b"SYNTHETIC_BARCODE")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual(1, manifest["barcodes"]["scanned"])

    def test_visible_personal_data_in_image_is_found_by_ocr(self):
        marker = b"SYNTHETIC_OCR:owner-private@example.test 1234567890128"
        self.reviewed_image("private-account.png", marker=marker)
        self.deny.write_text("owner-private@example.test\n1234567890128\n")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("failed", manifest["ocr"]["status"])
        self.assertEqual(1, manifest["ocr"]["scanned"])

    def test_new_or_changed_image_without_review_entry_fails(self):
        (self.repo / "unreviewed.png").write_bytes(self.png_bytes())
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("failed", manifest["image_review"]["status"])

    def test_image_metadata_is_stripped_before_review_hashes(self):
        private = b"synthetic-private-peer"
        png_clean = self.png_bytes()
        png_source = self.png_bytes((
            (b"tEXt", b"Comment\x00synthetic note"),
            (b"zTXt", b"Private\x00\x00" + zlib.compress(private)),
            (b"iTXt", b"Description\x00\x00\x00\x00\x00synthetic note"),
            (b"eXIf", b"synthetic exif"),
        ))
        self.assertIn(b"zTXt", png_source)
        self.assertNotIn(private, png_source)
        jpeg_clean = self.jpeg_bytes()
        jpeg_source = self.jpeg_bytes((
            self.jpeg_segment(0xe1, b"Exif\x00\x00synthetic exif"),
            self.jpeg_segment(0xe1, b"http://ns.adobe.com/xap/1.0/\x00synthetic xmp"),
            self.jpeg_segment(0xfe, b"synthetic comment"),
        ))
        webp_clean = self.webp_bytes(0)
        webp_source = self.webp_bytes(0x0c, (
            self.webp_chunk(b"EXIF", b"synthetic exif"),
            self.webp_chunk(b"XMP ", b"synthetic xmp"),
        ))
        expected = {"asset.png": png_clean, "asset.jpg": jpeg_clean, "asset.webp": webp_clean}
        sources = {"asset.png": png_source, "asset.jpg": jpeg_source, "asset.webp": webp_source}
        policy = self.repo / "release/reviewed-images.txt"
        for path, data in sources.items():
            (self.repo / path).write_bytes(data)
            with policy.open("a") as handle:
                handle.write(f"{path}\t{hashlib.sha256(expected[path]).hexdigest()}\t2026-11-03\tSynthetic Fixture\n")
        self.commit()

        result = self.run_snapshot()
        self.assertEqual(0, result.returncode, result.stderr)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("passed", manifest["status"])
        self.assertEqual(3, manifest["counts"]["sanitized_images"])
        self.assertEqual(set(expected), {item["path"] for item in manifest["sanitized_images"]})
        for path, data in expected.items():
            exported = (self.base / "out/tree" / path).read_bytes()
            self.assertEqual(data, exported)
            entry = next(item for item in manifest["included"] if item["path"] == path)
            self.assertEqual(hashlib.sha256(data).hexdigest(), entry["sha256"])

    def test_compressed_and_zip_payloads_fail_closed(self):
        (self.repo / "private-account.txt.gz").write_bytes(gzip.compress(b"synthetic-private-peer\n", mtime=0))
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as zipped:
            zipped.writestr("account.txt", "owner-private@example.test")
        (self.repo / "private-account.zip").write_bytes(archive.getvalue())
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("failed", manifest["formats"]["status"])
        self.assertEqual({"private-account.txt.gz", "private-account.zip"},
                         {item["path"] for item in manifest["formats"]["failures"]})

    def test_any_exported_self_hosted_workflow_fails(self):
        self.put(".github/workflows/rogue.yml", "name: rogue\non: [pull_request]\njobs:\n  test:\n    runs-on:\n      - self-hosted\n      - private-runner-label\n")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual("failed", manifest["workflows"]["status"])
        self.assertEqual(1, len(manifest["workflows"]["failures"]))
    def test_netpbm_qr_is_classified_and_scanned_by_magic(self):
        target = self.repo / "qr.data"
        PUBLIC_SNAPSHOT.write_zbar_self_test(target)
        digest = hashlib.sha256(target.read_bytes()).hexdigest()
        with (self.repo / "release/reviewed-images.txt").open("a") as handle:
            handle.write(f"qr.data\t{digest}\t2026-11-03\tSynthetic Fixture\n")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual(1, manifest["barcodes"]["scanned"])
        self.assertEqual("decodable-barcode", manifest["barcodes"]["failures"][0]["category"])

    def test_unknown_binary_and_bom_nul_data_are_opaque(self):
        (self.repo / "unknown.bin").write_bytes(b"\xff\xfe\xfd\xfc")
        (self.repo / "bom.bin").write_bytes(b"\xef\xbb\xbfvisible\x00hidden")
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual({"unknown.bin", "bom.bin"},
                         {failure["path"] for failure in manifest["formats"]["failures"]})

    def test_review_metadata_must_be_complete(self):
        image = self.reviewed_image("asset.png")
        digest = hashlib.sha256(image.read_bytes()).hexdigest()
        (self.repo / "release/reviewed-images.txt").write_text(f"asset.png\t{digest}\t\t\n")
        self.commit()
        self.assertEqual(2, self.run_snapshot().returncode)

    def test_workflow_gate_handles_quoted_hash_and_unicode_escape(self):
        self.put(".github/workflows/hash.yml", 'jobs:\n  test:\n    runs-on: ["label#1", self-hosted]\n')
        self.put(".github/workflows/escaped.yml", 'jobs:\n  test:\n    runs-on: "self\\u002dhosted"\n')
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual({".github/workflows/hash.yml", ".github/workflows/escaped.yml"},
                         set(manifest["workflows"]["failures"]))

    def test_workflow_gate_parses_hex_escape_alias_and_rejects_expression(self):
        self.put(".github/workflows/hex.yml", 'jobs:\n  test:\n    runs-on: "self\\x2dhosted"\n')
        self.put(".github/workflows/alias.yml",
                 'runner: &private "self\\x2dhosted"\njobs:\n  test:\n    runs-on: *private\n')
        self.put(".github/workflows/expression.yml",
                 'jobs:\n  test:\n    runs-on: "${{ matrix.runner }}"\n')
        self.commit()
        self.assertEqual(1, self.run_snapshot().returncode)
        manifest = json.loads((self.base / "out/manifest.json").read_text())
        self.assertEqual({".github/workflows/hex.yml", ".github/workflows/alias.yml",
                          ".github/workflows/expression.yml"},
                         set(manifest["workflows"]["failures"]))

    def test_workflow_gate_fallback_rejects_escaped_or_complex_keys(self):
        root = self.base / "workflow-tree"
        workflows = root / ".github/workflows"
        workflows.mkdir(parents=True)
        cases = {
            "escaped.yml": 'jobs:\n  test:\n    runs-on: "self\\x2dhosted"\n',
            "inline.yml": 'jobs: {test: {runs-on: "self\\x2dhosted"}}\n',
            "escaped-key.yml": 'jobs:\n  test:\n    "runs\\x2don": self-hosted\n',
            "explicit-key.yml": 'jobs:\n  test:\n    ? runs-on\n    : self-hosted\n',
        }
        for name, text in cases.items():
            (workflows / name).write_text(text)
        (workflows / "public.yml").write_text(
            "jobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n"
            "      - run: echo '${{ github.ref }}'\n")
        with mock.patch.object(PUBLIC_SNAPSHOT, "YAML", None, create=True):
            result = PUBLIC_SNAPSHOT.scan_workflows(root)
        self.assertEqual("failed", result["status"])
        self.assertEqual({f".github/workflows/{name}" for name in cases},
                         set(result["failures"]))


if __name__ == "__main__":
    unittest.main(verbosity=2)
