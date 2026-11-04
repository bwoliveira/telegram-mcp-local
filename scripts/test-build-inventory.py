#!/usr/bin/env python3
"""Stdlib-only behavioral tests for release inventories (no network needed)."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/build-inventory.py"


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="build-inventory-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.cache = self.root / "cache"
        self.cache.mkdir()
        self.out = self.root / "output"
        self.preamble = self.root / "credits.md"
        self.preamble.write_text("# Curated credits\n\nPreserve upstream authors.\n")
        self.license = self.root / "LICENSE"
        self.license.write_text("Project license\n")
        self.goroot = self.root / "go-toolchain"
        self.goroot.mkdir()
        (self.goroot / "VERSION").write_text("go1.26.5\ntime 2026-01-01T00:00:00Z\n")
        self.go_license_text = "Copyright The Go Authors.\r\n\r\nFull Go redistribution terms.\r\nFinal disclaimer.\r\n"
        (self.goroot / "LICENSE").write_bytes(self.go_license_text.encode())
        self.tools = self.root / "tools"
        self.tools.mkdir()
        go = self.tools / "go"
        go.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
with open(os.environ["GO_CALLS"], "a") as f:
    f.write(json.dumps(sys.argv[1:]) + "\\n")
if sys.argv[1:] == ["env", "GOMODCACHE"]:
    print(os.environ["TEST_GOMODCACHE"])
elif sys.argv[1:] == ["env", "-json", "GOROOT", "GOVERSION"]:
    print(os.environ.get("TEST_GO_ENV_JSON", json.dumps({
        "GOROOT": os.environ["TEST_GOROOT"], "GOVERSION": os.environ["TEST_GOVERSION"]})))
elif sys.argv[1:4] == ["version", "-m", "-json"] and len(sys.argv) == 5:
    path = pathlib.Path(sys.argv[4])
    info = path.with_name(path.name + ".buildinfo.json")
    if not info.exists() and "fixtures" in os.environ and path.parent.name.startswith("cli-extracted-"):
        target = path.parent.name.removeprefix("cli-extracted-")
        info = pathlib.Path(os.environ["fixtures"]) / (target + ".buildinfo.json")
    print(info.read_text())
else:
    raise SystemExit("unexpected go invocation: " + repr(sys.argv))
''')
        go.chmod(0o755)
        self.calls = self.root / "go-calls.jsonl"
        self.env = dict(os.environ, PATH=str(self.tools) + os.pathsep + os.environ["PATH"],
                        TEST_GOMODCACHE=str(self.cache), GO_CALLS=str(self.calls),
                        TEST_GOROOT=str(self.goroot), TEST_GOVERSION="go1.26.5")

    def module(self, path="example.com/linked", version="v1.2.3", files=None):
        def escape(value):
            return "".join("!" + c.lower() if "A" <= c <= "Z" else c for c in value)
        folder = self.cache / (escape(path) + "@" + escape(version))
        folder.mkdir(parents=True, exist_ok=True)
        for name, text in (files or {"LICENSE": "MIT License\nCopyright Linked Author\n"}).items():
            file = folder / name
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text(text)
        return {"Path": path, "Version": version, "Sum": "h1:module-zip-sum"}

    def binary(self, name, deps):
        binary = self.root / name
        binary.write_bytes(b"test executable " + name.encode())
        info = {"GoVersion": "go1.26.5", "Path": "example.com/project/cmd/" + name,
                "Main": {"Path": "example.com/project", "Version": "(devel)"},
                "Deps": deps,
                "Settings": [{"Key": "GOOS", "Value": "windows"},
                             {"Key": "GOARCH", "Value": "amd64"}]}
        binary.with_name(name + ".buildinfo.json").write_text(json.dumps(info))
        return binary

    def run_inventory(self, binaries, *extra, ok=True):
        args = [sys.executable, str(SCRIPT), "--output-dir", str(self.out),
                "--preamble", str(self.preamble), "--project-license", str(self.license)]
        for binary in binaries:
            args += ["--binary", str(binary)]
        result = subprocess.run(args + list(extra), env=self.env, text=True, capture_output=True)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def outputs(self):
        return {p.name: p.read_bytes() for p in self.out.iterdir()}

    def test_exact_toolchain_full_license_in_all_notices_and_boms(self):
        app = self.binary("app", [])
        bridge = self.binary("bridge", [self.module()])
        artifact = self.root / "installer"
        artifact.write_bytes(b"installer")
        self.run_inventory([bridge, app], "--artifact", str(artifact))
        initial = self.outputs()
        for name in ("THIRD-PARTY-NOTICES.txt", "app.THIRD-PARTY-NOTICES.txt",
                     "bridge.THIRD-PARTY-NOTICES.txt"):
            text = initial[name].decode()
            self.assertIn(self.go_license_text, text)
            self.assertEqual(text.count(self.go_license_text), 1)
            self.assertIn("go1.26.5", text)
            self.assertNotIn("excludes the Go standard library", text)
        for name in ("app", "bridge", "installer"):
            document = json.loads(initial[name + ".cdx.json"])
            go_components = [c for c in document["components"] if c["name"] == "Go standard library"]
            self.assertEqual(len(go_components), 1)
            go = go_components[0]
            self.assertEqual(go["version"], "go1.26.5")
            self.assertEqual(go["licenses"][0]["license"]["text"]["content"], self.go_license_text)
            self.assertNotIn(str(self.goroot), json.dumps(document))
            self.assertNotIn("excludes the Go standard library", json.dumps(document))
            payload_refs = ([c["bom-ref"] for c in document["components"] if c["name"] in ("app", "bridge")]
                            if name == "installer" else [document["metadata"]["component"]["bom-ref"]])
            for ref in payload_refs:
                edge = next(d for d in document["dependencies"] if d["ref"] == ref)
                self.assertIn(go["bom-ref"], edge["dependsOn"])
        self.run_inventory([app, bridge], "--artifact", str(artifact), "--check")
        self.run_inventory([app, bridge], "--artifact", str(artifact))
        self.assertEqual(initial, self.outputs())

    def test_toolchain_version_mismatch_fails_before_any_write(self):
        app = self.binary("app", [])
        bridge = self.binary("bridge", [])
        info_file = bridge.with_name("bridge.buildinfo.json")
        info = json.loads(info_file.read_text())
        info["GoVersion"] = "go1.26.4"
        info_file.write_text(json.dumps(info))
        self.out.mkdir()
        sentinel = self.out / "THIRD-PARTY-NOTICES.txt"
        sentinel.write_bytes(b"previous complete inventory")
        for binaries in ([bridge], [app, bridge]):
            with self.subTest(binaries=[b.name for b in binaries]):
                result = self.run_inventory(binaries, ok=False)
                self.assertIn("Go toolchain version mismatch", result.stderr)
                self.assertEqual(self.outputs(), {sentinel.name: b"previous complete inventory"})
        (self.goroot / "VERSION").write_text("go1.26.4\n")
        result = self.run_inventory([app], ok=False)
        self.assertIn("Go toolchain version mismatch", result.stderr)
        self.assertEqual(self.outputs(), {sentinel.name: b"previous complete inventory"})

    def test_toolchain_legal_inputs_fail_closed_without_writes(self):
        app = self.binary("app", [])
        for filename in ("LICENSE", "VERSION"):
            source = self.goroot / filename
            original = source.read_bytes()
            for case in ("missing", "empty", "whitespace", "invalid-utf8", "symlink", "directory"):
                with self.subTest(filename=filename, case=case):
                    source.unlink()
                    if case == "empty":
                        source.write_bytes(b"")
                    elif case == "whitespace":
                        source.write_bytes(b" \r\n\t")
                    elif case == "invalid-utf8":
                        source.write_bytes(b"\xff")
                    elif case == "symlink":
                        source.symlink_to(self.license)
                    elif case == "directory":
                        source.mkdir()
                    try:
                        result = self.run_inventory([app], ok=False)
                        self.assertIn("Go toolchain", result.stderr)
                        self.assertNotIn("Traceback", result.stderr)
                        self.assertFalse(self.out.exists())
                    finally:
                        if source.is_dir() and not source.is_symlink():
                            source.rmdir()
                        elif source.exists() or source.is_symlink():
                            source.unlink()
                        source.write_bytes(original)
                        if self.out.exists():
                            shutil.rmtree(self.out)

    def test_toolchain_environment_metadata_is_validated(self):
        app = self.binary("app", [])
        for key, values in (("TEST_GOVERSION", ("", "not-a-go-version")),
                            ("TEST_GOROOT", ("", "relative/toolchain"))):
            original = self.env[key]
            for value in values:
                with self.subTest(key=key, value=value):
                    self.env[key] = value
                    result = self.run_inventory([app], ok=False)
                    self.assertIn("Go toolchain", result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
                    self.assertFalse(self.out.exists())
            self.env[key] = original
        for raw in ("not-json", "[]", "null", "{}", '{"GOROOT":42,"GOVERSION":"go1.26.5"}',
                    json.dumps({"GOROOT": str(self.goroot), "GOVERSION": []})):
            with self.subTest(raw=raw):
                self.env["TEST_GO_ENV_JSON"] = raw
                result = self.run_inventory([app], ok=False)
                self.assertIn("Go toolchain", result.stderr)
                self.assertNotIn("Traceback", result.stderr)
                self.assertFalse(self.out.exists())

    def test_toolchain_license_drift_and_missing_text_fail_check_without_writes(self):
        app = self.binary("app", [])
        artifact = self.root / "installer"
        artifact.write_bytes(b"installer")
        self.run_inventory([app], "--artifact", str(artifact))
        initial = self.outputs()
        mtimes = {p.name: p.stat().st_mtime_ns for p in self.out.iterdir()}
        source = self.goroot / "LICENSE"
        source.write_bytes((self.go_license_text + "Additional full upstream terms.\r\n").encode())
        result = self.run_inventory([app], "--artifact", str(artifact), "--check", ok=False)
        stale_names = result.stderr.split("missing or stale generated inventory: ", 1)[1].strip().split(", ")
        self.assertEqual(set(stale_names), set(initial))
        self.assertEqual(initial, self.outputs())
        self.assertEqual(mtimes, {p.name: p.stat().st_mtime_ns for p in self.out.iterdir()})
        source.unlink()
        for mode in ((), ("--check",)):
            result = self.run_inventory([app], "--artifact", str(artifact), *mode, ok=False)
            self.assertIn("Go toolchain LICENSE", result.stderr)
            self.assertEqual(initial, self.outputs())
            self.assertEqual(mtimes, {p.name: p.stat().st_mtime_ns for p in self.out.iterdir()})

    def test_linked_modules_only_and_deterministic_per_binary_and_artifact(self):
        shared = self.module()
        unique = self.module("example.com/Upper/Lib", "v2.0.0+incompatible",
                             {"LICENCE.txt": "BSD license\n", "NOTICE": "Keep this notice\n"})
        self.module("example.com/test-only", files={"LICENSE": "must not leak"})
        app = self.binary("app.exe", [unique, shared])
        bridge = self.binary("bridge.exe", [shared])
        artifact = self.root / "Setup.exe"
        artifact.write_bytes(b"installer bytes")
        self.run_inventory([bridge, app], "--artifact", str(artifact))
        generated = self.outputs()
        self.assertEqual(set(generated), {"app.exe.cdx.json", "bridge.exe.cdx.json", "Setup.exe.cdx.json",
                                          "app.exe.THIRD-PARTY-NOTICES.txt", "bridge.exe.THIRD-PARTY-NOTICES.txt",
                                          "Setup.exe.THIRD-PARTY-NOTICES.txt", "THIRD-PARTY-NOTICES.txt"})
        notices = generated["THIRD-PARTY-NOTICES.txt"].decode()
        self.assertTrue(notices.startswith(self.preamble.read_text()))
        self.assertEqual(notices.count("Copyright Linked Author"), 1)
        self.assertIn("Keep this notice", notices)
        self.assertIn("Project license", notices)
        self.assertNotIn("test-only", notices)
        for name, expected in (("app.exe", 3), ("bridge.exe", 2)):
            bom = json.loads(generated[name + ".cdx.json"])
            self.assertEqual((bom["bomFormat"], bom["specVersion"]), ("CycloneDX", "1.6"))
            self.assertEqual(len(bom["components"]), expected)
            root = bom["metadata"]["component"]
            self.assertEqual(root["name"], name)
            self.assertEqual(root["hashes"], [{"alg": "SHA-256", "content": hashlib.sha256((self.root / name).read_bytes()).hexdigest()}])
            self.assertEqual(set(bom["dependencies"][0]["dependsOn"]), {c["bom-ref"] for c in bom["components"]})
            self.assertNotIn("test-only", json.dumps(bom))
            self.assertNotIn(str(self.root), json.dumps(bom))
        artifact_bom = json.loads(generated["Setup.exe.cdx.json"])
        self.assertEqual(artifact_bom["metadata"]["component"]["name"], "Setup.exe")
        self.assertEqual(len(artifact_bom["metadata"]["component"]["hashes"][0]["content"]), 64)
        self.assertEqual(len(artifact_bom["components"]), 5)  # two executables + two modules + Go
        self.assertEqual(artifact_bom["metadata"]["component"]["hashes"][0]["content"],
                         hashlib.sha256(artifact.read_bytes()).hexdigest())
        references = {artifact_bom["metadata"]["component"]["bom-ref"]} | {c["bom-ref"] for c in artifact_bom["components"]}
        for edge in artifact_bom["dependencies"]:
            self.assertIn(edge["ref"], references)
            self.assertTrue(set(edge["dependsOn"]).issubset(references))
        info_file = app.with_name("app.exe.buildinfo.json")
        info = json.loads(info_file.read_text())
        info["Deps"].reverse()
        info["Settings"].append({"Key": "-ldflags", "Value": "-X private.key=must-not-leak"})
        info_file.write_text(json.dumps(info))
        self.run_inventory([app, bridge], "--artifact", str(artifact))
        self.assertEqual(generated, self.outputs())
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        self.assertTrue(any(c[:3] == ["version", "-m", "-json"] for c in calls))
        self.assertTrue(all(c in (["env", "GOMODCACHE"], ["env", "-json", "GOROOT", "GOVERSION"])
                            or c[:3] == ["version", "-m", "-json"] for c in calls))

    def test_check_detects_changed_missing_and_binary_drift_without_writing(self):
        binary = self.binary("app", [self.module()])
        self.run_inventory([binary], "--check", ok=False)
        self.assertFalse(self.out.exists())
        self.run_inventory([binary])
        initial = self.outputs()
        mtimes = {p.name: p.stat().st_mtime_ns for p in self.out.iterdir()}
        self.run_inventory([binary], "--check")
        self.assertEqual(mtimes, {p.name: p.stat().st_mtime_ns for p in self.out.iterdir()})
        stale = self.out / "app.cdx.json"
        stale.write_text("stale")
        self.run_inventory([binary], "--check", ok=False)
        self.assertEqual(stale.read_text(), "stale")
        stale.write_bytes(initial[stale.name])
        missing = self.out / "app.THIRD-PARTY-NOTICES.txt"
        missing.unlink()
        self.run_inventory([binary], "--check", ok=False)
        self.assertFalse(missing.exists())
        missing.write_bytes(initial[missing.name])
        binary.write_bytes(b"changed executable")
        self.run_inventory([binary], "--check", ok=False)
        self.assertEqual(initial, self.outputs())

    def test_missing_legal_text_fails_before_writing_anything(self):
        dep = self.module(files={"LICENSE": "  \n", "license.go": "package library\n"})
        binary = self.binary("app", [dep])
        self.out.mkdir()
        sentinel = self.out / "THIRD-PARTY-NOTICES.txt"
        sentinel.write_text("previous complete inventory")
        result = self.run_inventory([binary], ok=False)
        self.assertIn("no non-empty", result.stderr)
        self.assertEqual(self.outputs(), {sentinel.name: b"previous complete inventory"})

    def test_missing_module_cache_is_an_error_not_a_download(self):
        binary = self.binary("app", [{"Path": "example.com/missing", "Version": "v1.0.0"}])
        self.assertIn("missing from GOMODCACHE", self.run_inventory([binary], ok=False).stderr)
        self.assertFalse(self.out.exists())

    def test_nested_case_insensitive_legal_names_and_full_text_preserved(self):
        files = {"license.md": "MIT\r\nCopyright A\r\n",
                 "vendor/part/LICENCE": "Attribution B\n",
                 "nested/COPYING.LESSER": "Full terms C\n",
                 "NOTICE.txt": "Notice D\n", "COPYRIGHT": "Copyright E\n",
                 "Unlicense": "Public domain F\n", "LICENSE-MIT": "Terms G\n",
                 "license.go": "excluded source", "LICENSE": "\n"}
        dep = self.module(files=files)
        binary = self.binary("app", [dep])
        self.run_inventory([binary])
        bom = json.loads((self.out / "app.cdx.json").read_text())
        actual = {item["license"]["name"].removeprefix("Upstream legal text: "):
                  item["license"]["text"]["content"] for item in bom["components"][0]["licenses"]}
        self.assertEqual(actual, {k: v for k, v in files.items() if k not in ("LICENSE", "license.go")})
        self.assertTrue(all("id" not in item["license"] for item in bom["components"][0]["licenses"]))

    def test_versioned_replacement_uses_actual_license_and_purl(self):
        replacement = self.module("example.com/Fork/lib", "v1.9.0", {"LICENSE": "Fork license"})
        original = {"Path": "example.com/original", "Version": "v1.0.0", "Replace": replacement}
        binary = self.binary("app", [original, replacement])
        self.run_inventory([binary])
        bom = json.loads((self.out / "app.cdx.json").read_text())
        self.assertEqual(len(bom["components"]), 2)
        component = bom["components"][0]
        self.assertEqual(component["purl"], "pkg:golang/example.com/Fork/lib@v1.9.0")
        self.assertIn("example.com/original@v1.0.0", json.dumps(component["properties"]))
        notices = (self.out / "THIRD-PARTY-NOTICES.txt").read_text()
        self.assertIn("Replaces: example.com/original@v1.0.0", notices)
        self.assertEqual(notices.count("Fork license"), 1)

    def test_local_and_malformed_replacements_fail_closed(self):
        for replace in ({"Path": "../local"}, {}, [], "invalid", {"Path": "example.com/lib", "Version": "(devel)"}):
            with self.subTest(replace=replace):
                dep = {"Path": "example.com/original", "Version": "v1.0.0", "Replace": replace}
                binary = self.binary("app", [dep])
                self.run_inventory([binary], ok=False)
        self.assertFalse(self.out.exists())

    def test_invalid_module_identity_cannot_escape_cache(self):
        for path, version in (("../outside", "v1.0.0"), ("/absolute", "v1.0.0"),
                              ("C:/absolute", "v1.0.0"), ("example.com/a/../../x", "v1.0.0"),
                              ("example.com/a", "v1.0.0/../../x"), ("example.com/a", ""),
                              ("example.com/a!b", "v1.0.0")):
            with self.subTest(path=path, version=version):
                binary = self.binary("app", [{"Path": path, "Version": version}])
                self.run_inventory([binary], ok=False)
        self.assertFalse(self.out.exists())

    @unittest.skipUnless(hasattr(os, "symlink"), "symlinks not supported")
    def test_cache_symlinks_and_output_symlinks_are_rejected(self):
        dep = self.module()
        folder = self.cache / "example.com/linked@v1.2.3"
        (folder / "LICENSE").unlink()
        (folder / "LICENSE").symlink_to(self.license)
        binary = self.binary("app", [dep])
        self.assertIn("unsafe legal file", self.run_inventory([binary], ok=False).stderr)
        (folder / "LICENSE").unlink()
        (folder / "LICENSE").write_text("valid text")
        self.out.mkdir()
        target = self.out / "app.cdx.json"
        target.symlink_to(self.license)
        self.assertIn("symlink", self.run_inventory([binary], ok=False).stderr)
        self.assertEqual(self.license.read_text(), "Project license\n")
        target.unlink()
        (folder / "nested").symlink_to(self.root, target_is_directory=True)
        self.assertIn("symlink directory", self.run_inventory([binary], ok=False).stderr)

    def test_invalid_buildinfo_and_missing_binary_fail(self):
        binary = self.binary("app", [])
        info = binary.with_name("app.buildinfo.json")
        for invalid in ("", "{}", "[]", "not json", '{"GoVersion":"go1.26.5","Path":"x","Deps":{}}'):
            with self.subTest(invalid=invalid):
                info.write_text(invalid)
                self.run_inventory([binary], ok=False)
        self.run_inventory([self.root / "missing.exe"], ok=False)
        self.assertFalse(self.out.exists())

    def test_stdlib_only_has_toolchain_but_no_module_components(self):
        binary = self.binary("stdlib", [])
        self.run_inventory([binary])
        bom = json.loads((self.out / "stdlib.cdx.json").read_text())
        self.assertEqual([c["name"] for c in bom["components"]], ["Go standard library"])
        self.assertEqual(bom["dependencies"][0]["dependsOn"], [bom["components"][0]["bom-ref"]])
        self.assertFalse(any("purl" in c for c in bom["components"]))
        self.assertIn("Project license", (self.out / "THIRD-PARTY-NOTICES.txt").read_text())

    def test_duplicate_basenames_and_output_input_collision_are_rejected(self):
        binary = self.binary("app", [])
        other = self.root / "other" / "app"
        other.parent.mkdir()
        other.write_bytes(b"different binary")
        self.assertIn("basenames must be unique", self.run_inventory([binary, other], ok=False).stderr)
        self.run_inventory([binary], "--artifact", str(binary), ok=False)
        collision = self.binary("app.cdx.json", [])
        self.run_inventory([binary, collision], "--output-dir", str(self.root), ok=False)
        self.assertEqual(collision.read_bytes(), b"test executable app.cdx.json")

    def test_conflicting_checksums_fail_and_duplicates_are_deduplicated(self):
        dep = self.module()
        binary = self.binary("app", [dep, {**dep, "Sum": "h1:different"}])
        self.assertIn("conflicting checksums", self.run_inventory([binary], ok=False).stderr)
        binary = self.binary("app", [dep, dep])
        self.run_inventory([binary])
        components = json.loads((self.out / "app.cdx.json").read_text())["components"]
        self.assertEqual(len(components), 2)
        self.assertEqual([c["name"] for c in components], ["example.com/linked", "Go standard library"])
        other = self.binary("bridge", [{**dep, "Sum": "h1:different"}])
        self.assertIn("conflicting checksums", self.run_inventory([binary, other], ok=False).stderr)

    def test_license_and_preamble_changes_are_detected_by_check(self):
        binary = self.binary("app", [self.module()])
        self.run_inventory([binary])
        self.preamble.write_text("Changed curated credits")
        self.run_inventory([binary], "--check", ok=False)
        self.run_inventory([binary])
        (self.cache / "example.com/linked@v1.2.3/LICENSE").write_text("Changed legal text")
        self.run_inventory([binary], "--check", ok=False)
        self.run_inventory([binary])
        self.license.write_text("")
        self.assertIn("must not be empty", self.run_inventory([binary], ok=False).stderr)

    def test_non_utf8_legal_file_and_go_failure_fail_closed(self):
        dep = self.module()
        (self.cache / "example.com/linked@v1.2.3/LICENSE").write_bytes(b"\xff\xfe")
        binary = self.binary("app", [dep])
        self.run_inventory([binary], ok=False)
        go = self.tools / "go"
        go.write_text(f"#!{sys.executable}\nimport sys\nprint('go probe failed', file=sys.stderr)\nsys.exit(7)\n")
        result = self.run_inventory([binary], ok=False)
        self.assertIn("go probe failed", result.stderr)
        self.assertFalse(self.out.exists())

    def test_explicit_gomodcache_avoids_cache_probe_but_not_toolchain_probe(self):
        binary = self.binary("app", [self.module()])
        self.run_inventory([binary], "--gomodcache", str(self.cache))
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0][:3], ["version", "-m", "-json"])
        self.assertEqual(calls[1], ["env", "-json", "GOROOT", "GOVERSION"])
        self.run_inventory([binary], "--gomodcache", "relative-cache", ok=False)

    def test_notices_shell_keeps_real_generated_outputs(self):
        binary = self.binary("app with spaces.exe", [self.module()])
        result = subprocess.run(["bash", str(ROOT / "scripts/notices.sh"), "--output-dir", str(self.out),
                                 "--binary", str(binary)], env=self.env, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, (self.out / "THIRD-PARTY-NOTICES.txt").read_bytes())
        self.assertIn(b"Copyright Linked Author", result.stdout)
        self.assertTrue((self.out / (binary.name + ".cdx.json")).is_file())
        preview = subprocess.run(["bash", str(ROOT / "scripts/notices.sh")], env=self.env,
                                 text=True, capture_output=True)
        self.assertEqual(preview.returncode, 0, preview.stderr)
        self.assertIn("curated preview only", preview.stderr)

    def test_artifact_drift_and_missing_artifact_fail_check(self):
        binary = self.binary("app", [self.module()])
        artifact = self.root / "installer.exe"
        artifact.write_bytes(b"installer version 1")
        self.run_inventory([binary], "--artifact", str(artifact))
        original = self.outputs()
        self.run_inventory([binary], "--artifact", str(artifact), "--check")
        artifact.write_bytes(b"installer version 2")
        self.run_inventory([binary], "--artifact", str(artifact), "--check", ok=False)
        self.assertEqual(original, self.outputs())
        artifact.unlink()
        self.run_inventory([binary], "--artifact", str(artifact), ok=False)
        self.assertEqual(original, self.outputs())

    def test_windows_packaging_flow_with_explicit_mock_build_tools(self):
        # Exercise the shell end-to-end, not real compilation or a real NSIS
        # installer. The separate offline integration exercises real Go output.
        repo = self.root / "mock-repo"
        scripts = repo / "scripts"
        scripts.mkdir(parents=True)
        for name in ("notices.sh", "build-inventory.py", "build-windows.sh"):
            shutil.copy2(ROOT / "scripts" / name, scripts / name)
        sidecars = scripts / "sidecars"
        sidecars.mkdir()
        (sidecars / "pins.sh").write_text(
            'WEBVIEW2_BOOTSTRAPPER_URL="https://example.invalid/webview.exe"\n'
            'WEBVIEW2_BOOTSTRAPPER_SHA256="' + hashlib.sha256(b"mock WebView bootstrapper").hexdigest() + '"\n'
            'WEBVIEW2_SIGNER_CERT_SHA256="' + hashlib.sha256(b"mock signer").hexdigest() + '"\n')
        (sidecars / "verify-download.sh").write_text('''sha256_file() { sha256sum "$1" | cut -d' ' -f1; }
verify_sha256() { [ "$(sha256_file "$1")" = "$2" ]; }
download_verified() { printf 'mock WebView bootstrapper' > "$3"; verify_sha256 "$3" "$2"; }
''')
        (repo / "LICENSE").write_text("Project license\n")
        (repo / "THIRD_PARTY_NOTICES.md").write_text("Curated authors\n")
        windows = repo / "build/windows"
        windows.mkdir(parents=True)
        (windows / "installer.nsi").write_text("mock NSIS input")
        (windows / "icon.ico").write_bytes(b"mock icon")
        app_dep = self.module("example.com/app-only", files={"LICENSE": "App-only legal text"})
        bridge_dep = self.module("example.com/bridge-only", files={"LICENSE": "Bridge-only legal text"})
        go = self.tools / "go"
        go.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ["GO_CALLS"], "a") as f:
    f.write(json.dumps(args) + "\\n")
if args[0] == "run":
    pass
elif args[0] == "build":
    out = pathlib.Path(args[args.index("-o") + 1])
    out.write_bytes(b"mock executable " + out.name.encode())
    dep = json.loads(os.environ["MOCK_BRIDGE_DEP" if "bridge" in out.name else "MOCK_APP_DEP"])
    out.with_name(out.name + ".buildinfo.json").write_text(json.dumps({
        "GoVersion": "go1.26.5", "Path": "example.com/project/" + out.name,
        "Main": {"Path": "example.com/project", "Version": "(devel)"}, "Deps": [dep]}))
elif args == ["env", "GOMODCACHE"]:
    print(os.environ["TEST_GOMODCACHE"])
elif args == ["env", "-json", "GOROOT", "GOVERSION"]:
    print(json.dumps({"GOROOT": os.environ["TEST_GOROOT"], "GOVERSION": os.environ["TEST_GOVERSION"]}))
elif args[:3] == ["version", "-m", "-json"]:
    path = pathlib.Path(args[3])
    print(path.with_name(path.name + ".buildinfo.json").read_text())
else:
    raise SystemExit("unexpected mock go args: " + repr(args))
''')
        curl = self.tools / "curl"
        curl.write_text(f"#!{sys.executable}\nimport pathlib, sys\nargs=sys.argv[1:]\npathlib.Path(args[args.index('-o')+1]).write_bytes(b'mock WebView bootstrapper')\n")
        osslsigncode = self.tools / "osslsigncode"
        osslsigncode.write_text("#!/bin/sh\nprintf '%s\\n' 'Leaf hash match: ok' 'Subject: /C=US/O=Microsoft Corporation/CN=Microsoft Corporation'\n")
        makensis = self.tools / "makensis"
        makensis.write_text(f"#!{sys.executable}\n" + '''import pathlib, sys
values = dict(arg[2:].split("=", 1) for arg in sys.argv[1:] if arg.startswith("-D"))
source = pathlib.Path(values["SRC"])
notices = (source / "THIRD-PARTY-NOTICES.txt").read_text()
assert "App-only legal text" in notices and "Bridge-only legal text" in notices
pathlib.Path(values["OUT"]).write_bytes(b"mock NSIS installer bytes")
''')
        seven_zip = self.tools / "7z"
        seven_zip.write_text(f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
out = pathlib.Path(next(arg[2:] for arg in sys.argv[1:] if arg.startswith("-o")))
out.mkdir(parents=True, exist_ok=True)
for name, key in (("telegram-mcp.exe", "MOCK_APP_DEP"), ("telegram-mcp-bridge.exe", "MOCK_BRIDGE_DEP")):
    binary = out / name
    binary.write_bytes(b"mock executable " + name.encode())
    binary.with_name(name + ".buildinfo.json").write_text(json.dumps({
        "GoVersion": "go1.26.5", "Path": "example.com/project/" + name,
        "Main": {"Path": "example.com/project", "Version": "(devel)"},
        "Deps": [json.loads(os.environ[key])]}))
(out / "MicrosoftEdgeWebview2Setup.exe").write_bytes(b"mock WebView bootstrapper")
(out / "THIRD-PARTY-NOTICES.txt").write_text("extracted notices")
''')
        for tool in (go, curl, osslsigncode, makensis, seven_zip):
            tool.chmod(0o755)
        env = dict(self.env, VERSION="1.2.3", MOCK_APP_DEP=json.dumps(app_dep), MOCK_BRIDGE_DEP=json.dumps(bridge_dep))
        result = subprocess.run(["bash", str(scripts / "build-windows.sh")], env=env,
                                text=True, capture_output=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        out = repo / "build/bin"
        self.assertEqual({p.name for p in out.glob("*.cdx.json")},
                         {"Telegram-MCP-Setup.exe.cdx.json"})
        bom = json.loads((out / "Telegram-MCP-Setup.exe.cdx.json").read_text())
        self.assertEqual(bom["metadata"]["component"]["hashes"][0]["content"],
                         hashlib.sha256((out / "Telegram-MCP-Setup.exe").read_bytes()).hexdigest())
        self.assertEqual(len(bom["components"]), 6)
        webview = next(c for c in bom["components"] if c["name"] == "MicrosoftEdgeWebview2Setup.exe")
        self.assertEqual(webview["type"], "file")
        self.assertEqual(webview["hashes"][0]["content"], hashlib.sha256(b"mock WebView bootstrapper").hexdigest())
        artifact_notice = out / "Telegram-MCP-Setup.exe.THIRD-PARTY-NOTICES.txt"
        self.assertIn("App-only legal text", artifact_notice.read_text())
        self.assertIn("Bridge-only legal text", artifact_notice.read_text())

    def test_linux_and_macos_notice_calls_include_each_built_binary(self):
        wails = self.module("github.com/wailsapp/wails/v3", "v3.0.0-alpha.1",
                            {"LICENSE": "MIT License\nCopyright Wails Authors\n"})
        bridge = self.module("example.com/bridge-only", files={"LICENSE": "Bridge authors\n"})
        for platform in ("linux", "macos"):
            with self.subTest(platform=platform):
                script = (ROOT / f"scripts/build-{platform}.sh").read_text()
                call = next(line for line in script.splitlines() if line.startswith("scripts/notices.sh"))
                self.assertLess(script.index("go build"), script.index(call))
                if platform == "linux":
                    binaries = [self.binary("telegram-mcp", [wails]),
                                self.binary("telegram-mcp-bridge", [bridge])]
                    setup = 'work="$TEST_ROOT"\n'
                    notices = self.root / "THIRD-PARTY-NOTICES.txt"
                else:
                    binaries = [self.binary("app-arm64", [wails]), self.binary("app-amd64", [wails]),
                                self.binary("bridge-arm64", [bridge]), self.binary("bridge-amd64", [bridge])]
                    setup = ('APP="$TEST_ROOT/App with spaces.app"\n'
                             'mkdir -p "$APP/Contents/Resources"\n'
                             'apps=("$TEST_ROOT/app-arm64" "$TEST_ROOT/app-amd64")\n'
                             'bridges=("$TEST_ROOT/bridge-arm64" "$TEST_ROOT/bridge-amd64")\n')
                    notices = self.root / "App with spaces.app/Contents/Resources/THIRD-PARTY-NOTICES.txt"
                    self.assertLess(script.index(call), script.index('"${sign[@]}"'))
                self.calls.unlink(missing_ok=True)
                result = subprocess.run(["bash", "-c", "set -euo pipefail\n" + setup + call],
                                        cwd=ROOT, env=dict(self.env, TEST_ROOT=str(self.root)),
                                        text=True, capture_output=True, timeout=30)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("Copyright Wails Authors", notices.read_text())
                self.assertIn("Bridge authors", notices.read_text())
                calls = [json.loads(line) for line in self.calls.read_text().splitlines()]
                self.assertEqual({c[3] for c in calls if c[:3] == ["version", "-m", "-json"]},
                                 {str(binary) for binary in binaries})

    def test_final_linux_and_macos_inventory_uses_extracted_payloads(self):
        linux = (ROOT / "scripts/build-linux.sh").read_text()
        self.assertIn('"$repo/scripts/extract-appimage.py"', linux)
        self.assertIn('"$repo/scripts/build-inventory.py" --artifact-only ${mode#generate}', linux)
        self.assertIn('for mode in generate --check; do', linux)
        # Both Linux packages are covered file-by-file from the extracted artifact.
        self.assertIn('--native-root "$appimage_extract" --native-profile linux-appimage', linux)
        self.assertIn('--native-root "$deb_extract" --native-profile linux-deb', linux)
        self.assertLess(linux.index('--native-embed --native-root "$appdir"'),
                        linux.index('appimagetool-prefix/AppRun'))
        self.assertLess(linux.index('appimagetool-prefix/AppRun'),
                        linux.index('--native-root "$appimage_extract"'))
        self.assertIn('OUT="${OUT:-$repo/build/bin}"', linux)
        macos = (ROOT / "scripts/build-macos.sh").read_text()
        self.assertIn('lipo -thin "$arch" "$universal"', macos)
        self.assertIn('--component "$mounted_app/Contents/MacOS/telegram-mcp"', macos)

    def test_release_cli_loop_generates_target_specific_notices_after_each_build(self):
        # Run the actual five-target release loop, replacing compilation only.
        # Real notices/inventory generation and real tar/zip packaging are used.
        targets = ("darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64", "windows-amd64")
        for target in targets:
            dep = self.module("example.com/" + target, files={"LICENSE": "Copyright " + target + "\n"})
            self.binary(target, [dep])
        script = (ROOT / "scripts/release.sh").read_text()
        loop = "for target in " + script.split("for target in ", 1)[1].split('if [ -z "$mac_artifact" ]', 1)[0]
        self.assertLess(loop.index("go build"), loop.index("scripts/notices.sh"))
        self.assertNotIn('scripts/notices.sh >', script)
        self.out.mkdir()
        shell = """set -euo pipefail
        go() {
          [ "$1" = build ] || return 1
          while [ "$1" != -o ]; do shift; done
          cp "$fixtures/$GOOS-$GOARCH" "$2"
          cp "$fixtures/$GOOS-$GOARCH.buildinfo.json" "$2.buildinfo.json"
        }
        """ + loop
        result = subprocess.run(["bash", "-c", shell], cwd=ROOT,
                                env=dict(self.env, fixtures=str(self.root), release_work=str(self.root),
                                         OUT=str(self.out), VERSION="1.2.3"),
                                text=True, capture_output=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(list(self.out.iterdir())), len(targets) * 3)
        for target in targets:
            os_name, arch = target.split("-")
            name = f"telegram-mcp_{os_name}_{arch}"
            if os_name == "windows":
                with zipfile.ZipFile(self.out / (name + ".zip")) as archive:
                    notices = archive.read("THIRD-PARTY-NOTICES.txt").decode()
            else:
                with tarfile.open(self.out / (name + ".tar.gz")) as archive:
                    member = archive.extractfile("THIRD-PARTY-NOTICES.txt")
                    self.assertIsNotNone(member)
                    assert member is not None
                    notices = member.read().decode()
            self.assertIn("Copyright " + target, notices)
            for other in targets:
                if other != target:
                    self.assertNotIn("Copyright " + other, notices)

    def test_windows_release_caller_uses_real_binaries(self):
        windows = (ROOT / "scripts/build-windows.sh").read_text()
        self.assertIn('scripts/notices.sh "$stage/telegram-mcp.exe" "$stage/telegram-mcp-bridge.exe"', windows)
        self.assertIn('python3 scripts/build-inventory.py --artifact-only --check', windows)
        self.assertLess(windows.index('GOOS=windows GOARCH=amd64'), windows.index('scripts/notices.sh'))

    @unittest.skipUnless(shutil.which("go"), "Go is required for the offline build-info integration")
    def test_real_go_binaries_and_module_cache_offline(self):
        # A real Go file-proxy/zip, real compilation and real build-info parser.
        # No downloaded dependencies and no fake Go output in this integration.
        proxy = self.root / "proxy"
        source = self.root / "source"
        source.mkdir()
        module_path, version = "example.com/linked", "v1.2.3"
        proxy_version = proxy / module_path / "@v"
        proxy_version.mkdir(parents=True)
        mod = f"module {module_path}\n\ngo 1.23\n"
        (proxy_version / (version + ".mod")).write_text(mod)
        (proxy_version / (version + ".info")).write_text(json.dumps({"Version": version, "Time": "2026-01-01T00:00:00Z"}))
        with zipfile.ZipFile(proxy_version / (version + ".zip"), "w") as archive:
            for name, content in {"go.mod": mod, "linked.go": "package linked\nconst Value = 42\n",
                                  "LICENSE": "Real upstream license\nCopyright Integration Author\n"}.items():
                archive.writestr(f"{module_path}@{version}/{name}", content)
        (source / "go.mod").write_text(f"module example.com/application\n\ngo 1.23\n\nrequire {module_path} {version}\n")
        (source / "main.go").write_text('package main\nimport ("fmt"; "example.com/linked")\nfunc main() { fmt.Println(linked.Value) }\n')
        real_go = shutil.which("go")
        assert real_go is not None
        env = dict(os.environ, GOMODCACHE=str(self.cache), GOPROXY=proxy.as_uri(), GOSUMDB="off",
                   GOTOOLCHAIN="local", GOWORK="off", GOFLAGS="", CGO_ENABLED="0", GOOS="linux", GOARCH="amd64")
        binary = self.root / "real-app"
        built = subprocess.run([real_go, "build", "-mod=mod", "-trimpath", "-o", str(binary), "."],
                               cwd=source, env=env, text=True, capture_output=True, timeout=120)
        self.assertEqual(built.returncode, 0, built.stderr)
        self.env = env
        self.run_inventory([binary])
        bom = json.loads((self.out / "real-app.cdx.json").read_text())
        self.assertEqual([c["purl"] for c in bom["components"] if "purl" in c],
                         ["pkg:golang/example.com/linked@v1.2.3"])
        go_env = subprocess.run([real_go, "env", "-json", "GOROOT", "GOVERSION"], env=env,
                                text=True, capture_output=True, check=True)
        toolchain = json.loads(go_env.stdout)
        full_license = (Path(toolchain["GOROOT"]) / "LICENSE").read_bytes().decode("utf-8")
        go_component = next(c for c in bom["components"] if c["name"] == "Go standard library")
        self.assertEqual(go_component["version"], toolchain["GOVERSION"])
        self.assertEqual(go_component["licenses"][0]["license"]["text"]["content"], full_license)
        self.assertIn(full_license, (self.out / "THIRD-PARTY-NOTICES.txt").read_bytes().decode("utf-8"))
        self.assertIn("Copyright Integration Author", (self.out / "THIRD-PARTY-NOTICES.txt").read_text())
        self.run_inventory([binary], "--check")


if __name__ == "__main__":
    unittest.main()
