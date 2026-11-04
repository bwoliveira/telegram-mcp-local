#!/usr/bin/env python3
"""Stdlib-only tests for the fail-closed Linux native payload inventory.

A fake Debian root (dpkg admindir, md5sums, copyright files, os-release) stands
in for the build container; dpkg-query runs against it with --admindir. No
network, Go toolchain or real AppImage is needed.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/build-inventory.py"
sys.path.insert(0, str(ROOT / "scripts"))
import native_inventory as native  # noqa: E402

TRIPLET = "usr/lib/x86_64-linux-gnu"
DEP5 = "Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\n\n"


def elf(machine=62, body=b""):
    header = bytearray(64)
    header[:6] = b"\x7fELF\x02\x01"
    struct.pack_into("<H", header, 18, machine)
    return bytes(header) + body


def md5(data):
    return hashlib.md5(data, usedforsecurity=False).hexdigest()


class FakeDebian:
    """A host root with installed packages that dpkg-query --admindir can read."""

    def __init__(self, root):
        self.root = root
        self.packages = {}
        (root / "var/lib/dpkg/info").mkdir(parents=True)
        (root / "var/lib/dpkg/updates").mkdir()
        # Multi-arch database layout: per-architecture "pkg:arch.list" files.
        (root / "var/lib/dpkg/info/format").write_text("1\n")
        (root / "etc").mkdir()
        (root / "etc/os-release").write_text('ID=debian\nVERSION_ID="13"\nVERSION_CODENAME=trixie\n')

    def add(self, name, files, copyright_text="Format: none\nLicense: Expat\n", source=None, arch="amd64",
            version="1.0-1"):
        """files: {absolute path: bytes}; copyright is installed unless None."""
        files = dict(files)
        if copyright_text is not None:
            files[f"/usr/share/doc/{name}/copyright"] = copyright_text.encode()
        for path, data in files.items():
            target = self.root / path.lstrip("/")
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        self.packages[name] = {"files": files, "source": source, "arch": arch, "version": version}
        self.write()

    def write(self):
        status = []
        for name, record in sorted(self.packages.items()):
            stanza = [f"Package: {name}", "Status: install ok installed", "Maintainer: Test <t@example.invalid>",
                      f"Architecture: {record['arch']}", f"Version: {record['version']}"]
            if record["arch"] != "all":
                stanza.append("Multi-Arch: same")
            if record["source"]:
                stanza.append(f"Source: {record['source']}")
            stanza.append("Description: test package")
            status.append("\n".join(stanza))
            key = f"{name}:{record['arch']}" if record["arch"] != "all" else name
            info = self.root / "var/lib/dpkg/info"
            (info / f"{key}.list").write_text("".join(p + "\n" for p in sorted(record["files"])))
            (info / f"{key}.md5sums").write_text("".join(
                f"{md5(data)}  {path.lstrip('/')}\n" for path, data in sorted(record["files"].items())))
        (self.root / "var/lib/dpkg/status").write_text("\n\n".join(status) + "\n")


class NativeFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="native-inventory-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.host = FakeDebian(self.base / "host")
        self.repo = self.base / "repo"
        (self.repo / "build/linux").mkdir(parents=True)
        self.desktop = "[Desktop Entry]\nName=Telegram MCP\nExec=telegram-mcp\nIcon=telegram-mcp\nType=Application\n"
        (self.repo / native.DESKTOP_SOURCE).write_text(self.desktop)
        self.icon = b"\x89PNG icon"
        (self.repo / native.ICON_SOURCE).write_bytes(self.icon)
        self.data = self.base / "data"
        self.apprun = elf(body=b"AppImageKit AppRun")
        self.runtime_bytes = None
        self.patchelf = self.base / "patchelf"
        self.patchelf.write_text(
            f"#!{sys.executable}\n"
            "import pathlib, sys\n"
            "assert sys.argv[1:3] == ['--set-rpath', '$ORIGIN'] and len(sys.argv) == 4\n"
            "path = pathlib.Path(sys.argv[3])\n"
            "path.write_bytes(path.read_bytes() + b'RUNPATH')\n")
        self.patchelf.chmod(0o755)
        self.write_pins()
        self.reviews({})
        # Debian packages for the AppImage payload.
        self.common_lgpl = ("GNU LESSER GENERAL PUBLIC LICENSE\n" + "Complete licence terms. " * 30 + "\n").encode()
        self.host.add("base-files", {"/usr/share/common-licenses/LGPL-2.1": self.common_lgpl},
                      arch="all", copyright_text="Format: none\nLicense: GPL-compatible-system-files\n")
        self.lib = elf(body=b"libfoo implementation")
        self.host.add("libfoo1", {f"/{TRIPLET}/libfoo.so.1.2.3": self.lib}, source="foo (1.0-1)",
                      copyright_text=DEP5 + "Files: *\nCopyright: Foo\nLicense: LGPL-2.1+\n"
                      " On Debian systems, the complete text is in /usr/share/common-licenses/LGPL-2.1.\n")
        os.symlink("libfoo.so.1.2.3", self.host.root / TRIPLET / "libfoo.so.1")
        self.host.packages["libfoo1"]["files"][f"/{TRIPLET}/libfoo.so.1"] = None
        self.host.packages["libfoo1"]["files"].pop(f"/{TRIPLET}/libfoo.so.1")
        self.host.add("libgtk-3-bin", {f"/{TRIPLET}/libgtk-3-0t64/gtk-query-immodules-3.0": elf(body=b"q")})
        self.host.add("libglib2.0-bin", {"/usr/bin/glib-compile-schemas": elf(body=b"c")})
        self.host.add("libgdk-pixbuf2.0-bin", {"/usr/bin/gdk-pixbuf-query-loaders": elf(body=b"l")})
        self.helper = elf(body=b"WebKitWebProcess")
        self.host.add("libwebkit2gtk-4.1-0", {f"/{TRIPLET}/webkit2gtk-4.1/WebKitWebProcess": self.helper},
                      source="webkit2gtk (2.54-1)",
                      copyright_text=DEP5 + "Files: *\nCopyright: Apple\nLicense: BSD-2-clause\n\n"
                      "Files: Source/WebCore/*\nCopyright: KDE\nLicense: LGPL-2+\n")

    def write_pins(self):
        legal = self.data / "legal"
        legal.mkdir(parents=True, exist_ok=True)
        texts = {"mit.txt": "MIT License\nCopyright test\n", "lgpl.txt": "GNU LESSER GENERAL PUBLIC LICENSE\n"}
        for name, text in texts.items():
            (legal / name).write_text(text)
        runtime_sha = native.sha256_bytes(self.runtime_bytes) if self.runtime_bytes else "0" * 64

        def item(name, sha, legal_names=("mit.txt",), static=()):
            return {"name": name, "version": "pinned", "role": "test", "source": "https://example.com/" + name,
                    "license": "MIT", "legal": list(legal_names), "static": list(static),
                    "architectures": {"amd64": {"url": "https://example.com/" + name, "sha256": sha},
                                      "arm64": {"url": "https://example.com/" + name, "sha256": sha}}}
        pins = {"schema": "telegram-mcp-native-pins/1", "inputs": {
            "appimage-type2-runtime": item("runtime", runtime_sha, static=[
                {"id": "libfuse", "name": "libfuse", "version": "3.15.0", "license": "LGPL-2.1",
                 "source": "https://example.com/fuse.tar.xz", "legal": ["lgpl.txt"]}]),
            "appimagekit-apprun": item("AppRun", native.sha256_bytes(self.apprun)),
            "linuxdeploy": item("linuxdeploy", "1" * 64),
            "linuxdeploy-plugin-gtk": item("linuxdeploy-plugin-gtk", "2" * 64)},
            "legal": {name: {"sha256": native.sha256_bytes(text.encode()), "url": "https://example.com/" + name}
                      for name, text in texts.items()}}
        for architecture in ("amd64", "arm64"):
            pins["inputs"]["linuxdeploy"]["architectures"][architecture]["patchelf_sha256"] = \
                native.sha256_file(self.patchelf)
        (self.data / "pinned-inputs.json").write_text(json.dumps(pins))

    def reviews(self, packages):
        (self.data / "license-review.json").write_text(json.dumps(
            {"schema": "telegram-mcp-native-license-review/1", "packages": packages}))

    def appdir(self, name="AppDir"):
        root = self.base / name
        files = {
            "usr/bin/telegram-mcp": elf(body=b"go app"),
            "usr/bin/telegram-mcp-bridge": elf(body=b"go bridge"),
            "usr/bin/THIRD-PARTY-NOTICES.txt": b"go notices\n",
            native.ICON_PATH: self.icon,
            "usr/share/applications/telegram-mcp.desktop": self.desktop.encode(),
            "telegram-mcp.desktop": (self.desktop + "X-AppImage-Version=0.0.0\n").encode(),
            "AppRun": b"#!/bin/bash\n# autogenerated by linuxdeploy\nexec \"$this_dir\"/AppRun.wrapped \"$@\"\n",
            "AppRun.wrapped": self.apprun,
            "apprun-hooks/linuxdeploy-plugin-gtk.sh": b"export GTK_DATA_PREFIX=x\nexport GDK_PIXBUF_MODULE_FILE=y\n",
            "usr/lib/libfoo.so.1": self.lib,
            "usr/lib/x86_64-linux-gnu/webkit2gtk-4.1/WebKitWebProcess": self.helper,
            "usr/lib/gtk-3.0/3.0.0/immodules.cache": b"# cache\n",
            "usr/share/doc/libfoo1/copyright": self.host.packages["libfoo1"]["files"][
                "/usr/share/doc/libfoo1/copyright"],
        }
        for path, data in files.items():
            target = root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        (root / "AppRun").chmod(0o755)
        os.symlink("telegram-mcp.png", root / ".DirIcon")
        os.symlink(native.ICON_PATH, root / "telegram-mcp.png")
        return root

    def go_hashes(self, root, profile="linux-appimage"):
        return {path: native.sha256_file(root / path) for path in native.PROFILES[profile]["go"]}

    def classify(self, root, profile="linux-appimage", embedded=False, **kwargs):
        return native.classify(root, profile, "amd64", kwargs.pop("go", None) or self.go_hashes(root, profile),
                               host_root=str(self.host.root), repo=self.repo, data_dir=self.data,
                               embedded=embedded, patchelf=self.patchelf, **kwargs)

    def assertFails(self, pattern, function, *args, **kwargs):
        with self.assertRaises(native.NativeInventoryError) as context:
            function(*args, **kwargs)
        self.assertRegex(str(context.exception), pattern)
        return str(context.exception)


class ClassificationTests(NativeFixture):
    def test_every_file_and_symlink_is_mapped_with_debian_metadata(self):
        root = self.appdir()
        model = self.classify(root)
        records = {record["path"]: record for record, _ in model["files"]}
        on_disk = {p.relative_to(root).as_posix() for p in root.rglob("*") if p.is_symlink() or p.is_file()}
        self.assertEqual(set(records) - set(native.EMBEDDED), on_disk)
        self.assertEqual(records["usr/lib/libfoo.so.1"]["class"], "debian-package")
        self.assertEqual(records["usr/lib/libfoo.so.1"]["package"], "libfoo1:amd64")
        self.assertEqual(records["usr/lib/libfoo.so.1"]["origin"], f"/{TRIPLET}/libfoo.so.1.2.3")
        self.assertFalse(records["usr/lib/libfoo.so.1"]["modified"])
        self.assertEqual(records["usr/lib/x86_64-linux-gnu/webkit2gtk-4.1/WebKitWebProcess"]["package"],
                         "libwebkit2gtk-4.1-0:amd64")
        self.assertEqual(records["usr/share/doc/libfoo1/copyright"]["class"], "debian-package")
        self.assertEqual(records["AppRun.wrapped"], {**records["AppRun.wrapped"], "class": "pinned-input",
                                                     "input": "appimagekit-apprun"})
        self.assertEqual(records["AppRun"]["input"], "linuxdeploy")
        self.assertEqual(records["usr/lib/gtk-3.0/3.0.0/immodules.cache"]["tool"], "gtk-query-immodules-3.0")
        self.assertEqual(records[".DirIcon"]["resolves_to"], native.ICON_PATH)
        packages = {p["name"]: p for p in model["packages"]}
        self.assertEqual(packages["libfoo1"]["source"], "foo")
        self.assertEqual(packages["libfoo1"]["source_version"], "1.0-1")
        self.assertEqual(packages["libfoo1"]["architecture"], "amd64")
        self.assertIn("License: LGPL-2.1+", packages["libfoo1"]["copyright"])
        self.assertIn("libgtk-3-bin", packages)  # build tool that generated a cache
        self.assertEqual(set(model["inputs"]), {"appimage-type2-runtime", "appimagekit-apprun",
                                                "linuxdeploy", "linuxdeploy-plugin-gtk"})

    def test_unmapped_native_file_fails_closed(self):
        root = self.appdir()
        (root / "usr/lib/libstray.so.1").write_bytes(elf(body=b"untracked"))
        self.assertFails(r"unmapped payload file .*usr/lib/libstray\.so\.1", self.classify, root)

    def test_unowned_host_file_fails_closed(self):
        root = self.appdir()
        data = elf(body=b"copied from /usr/local")
        (self.host.root / TRIPLET / "libunowned.so.0").write_bytes(data)
        (root / "usr/lib/libunowned.so.0").write_bytes(data)
        self.assertFails(r"no Debian package owns .*libunowned", self.classify, root)

    def test_modified_installed_bytes_fail_md5sums(self):
        root = self.appdir()
        (self.host.root / TRIPLET / "libfoo.so.1.2.3").write_bytes(elf(body=b"tampered"))
        self.assertFails(r"does not match libfoo1:amd64's md5sums", self.classify, root)

    def test_unrelated_same_architecture_elf_cannot_inherit_package_provenance(self):
        root = self.appdir()
        (root / "usr/lib/libfoo.so.1").write_bytes(self.helper)
        self.assertFails(r"does not match the trusted Debian bytes or an allowed build transformation",
                         self.classify, root)

    def test_build_modified_copy_is_recorded(self):
        root = self.appdir()
        (root / "usr/lib/libfoo.so.1").write_bytes(self.lib + b"RUNPATH")
        record = {r["path"]: r for r, _ in self.classify(root)["files"]}["usr/lib/libfoo.so.1"]
        self.assertTrue(record["modified"])
        self.assertEqual(record["origin_sha256"], native.sha256_bytes(self.lib))

    def test_missing_copyright_fails(self):
        root = self.appdir()
        (self.host.root / "usr/share/doc/libwebkit2gtk-4.1-0/copyright").unlink()
        self.assertFails(r"libwebkit2gtk-4\.1-0:amd64 has no usable /usr/share/doc/libwebkit2gtk-4\.1-0/copyright",
                         self.classify, root)

    def test_missing_referenced_common_license_fails(self):
        root = self.appdir()
        path = self.host.root / "usr/share/common-licenses/LGPL-2.1"
        path.unlink()
        self.assertFails(r"references an unusable common license: /usr/share/common-licenses/LGPL-2\.1",
                         self.classify, root)

    def test_empty_copyright_fails(self):
        root = self.appdir()
        path = "/usr/share/doc/libwebkit2gtk-4.1-0/copyright"
        self.host.packages["libwebkit2gtk-4.1-0"]["files"][path] = b"  \n"
        (self.host.root / path.lstrip("/")).write_bytes(b"  \n")
        self.host.write()
        self.assertFails(r"is empty", self.classify, root)

    def test_untracked_executable_cannot_pose_as_asset_or_pin(self):
        root = self.appdir()
        (root / native.ICON_PATH).write_bytes(elf(body=b"x"))
        self.assertFails(r"untracked native executable posing as project asset", self.classify, root)
        root = self.appdir("other")
        (root / "AppRun.wrapped").write_bytes(elf(body=b"different AppRun"))
        self.assertFails(r"AppRun\.wrapped does not match pinned input appimagekit-apprun", self.classify, root)

    def test_wrong_architecture_and_go_payload_mismatch(self):
        root = self.appdir()
        (root / "usr/lib/libfoo.so.1").write_bytes(elf(machine=183))
        self.assertFails(r"not built for amd64", self.classify, root)
        root = self.appdir("go")
        hashes = self.go_hashes(root)
        hashes["usr/bin/telegram-mcp"] = "0" * 64
        self.assertFails(r"Go payload usr/bin/telegram-mcp differs", self.classify, root, go=hashes)

    def test_project_assets_are_byte_checked(self):
        root = self.appdir()
        (root / "telegram-mcp.desktop").write_text(self.desktop + "Exec=evil\nX-AppImage-Version=0.0.0\n")
        self.assertFails(r"not build/linux/telegram-mcp\.desktop plus", self.classify, root)

    def test_symlink_escape_and_dangling_fail(self):
        root = self.appdir()
        os.symlink("../../../etc/passwd", root / "usr/lib/escape")
        self.assertFails(r"escapes the payload", self.classify, root)
        root = self.appdir("dangling")
        os.symlink("missing.so", root / "usr/lib/dangling.so")
        self.assertFails(r"missing payload file", self.classify, root)

    def test_embedded_manifest_round_trip_and_stale_entry(self):
        root = self.appdir()
        model = self.classify(root)
        for name, content in native.embedded_outputs(model).items():
            target = root / native.EMBED_DIR / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
        final = self.classify(root, embedded=True)
        native.verify_embedded(root, final)
        manifest = json.loads((root / native.EMBED_DIR / native.MANIFEST_NAME).read_text())
        self.assertEqual(manifest["counts"]["files"], len(manifest["files"]))
        manifest["files"] = [f for f in manifest["files"] if f["path"] != "usr/lib/libfoo.so.1"]
        (root / native.EMBED_DIR / native.MANIFEST_NAME).write_bytes(native.canonical(manifest))
        self.assertFails(r"embedded native inventory is missing or stale: usr/share/doc/telegram-mcp/native-payload\.json",
                         native.verify_embedded, root, self.classify(root, embedded=True))
        os.unlink(root / native.EMBED_DIR / native.NOTICES_NAME)
        self.assertFails(r"embedded native inventory missing from payload", self.classify, root, embedded=True)

    def test_notices_contain_copyright_offer_and_pinned_legal_texts(self):
        model = self.classify(self.appdir())
        text = native.notices_bytes(model).decode()
        self.assertIn("Written offer: for at least three years", text)
        self.assertIn("apt-get source foo=1.0-1", text)
        self.assertIn("https://snapshot.debian.org/package/foo/1.0-1/", text)
        self.assertIn("License: LGPL-2.1+", text)
        self.assertIn("--- /usr/share/common-licenses/LGPL-2.1", text)
        self.assertIn("Complete licence terms.", text)
        self.assertIn("GNU LESSER GENERAL PUBLIC LICENSE", text)  # Debian common licence and runtime libfuse
        self.assertIn("--appimage-extract", text)

    def test_sbom_parts_have_hashes_and_closed_edges(self):
        root = self.appdir()
        model = self.classify(root)
        go_refs = {path: "urn:go:" + path for path in native.PROFILES["linux-appimage"]["go"]}
        components, edges, direct = native.sbom_parts(model, go_refs, runtime_sha256="f" * 64)
        refs = {c["bom-ref"]: c for c in components}
        files = [c for c in components if c["type"] == "file"]
        self.assertEqual(len(files), len(model["files"]))
        for component in files:
            values = {p["name"]: p["value"] for p in component["properties"]}
            if values["telegram-mcp:native:class"] not in ("native-inventory",) and \
                    values["telegram-mcp:native:file-type"] == "file":
                self.assertEqual(component["hashes"][0]["alg"], "SHA-256")
        foo = next(c for c in files if c["name"] == "usr/lib/libfoo.so.1")
        self.assertEqual(edges[foo["bom-ref"]], ["pkg:deb/debian/libfoo1@1.0-1?arch=amd64&distro=debian-13"])
        go = next(c for c in files if c["name"] == "usr/bin/telegram-mcp")
        self.assertEqual(edges[go["bom-ref"]], ["urn:go:usr/bin/telegram-mcp"])
        deb = refs["pkg:deb/debian/libfoo1@1.0-1?arch=amd64&distro=debian-13"]
        license_texts = [item["license"]["text"]["content"] for item in deb["licenses"]]
        self.assertTrue(any("License: LGPL-2.1+" in text for text in license_texts))
        self.assertTrue(any("Complete licence terms." in text for text in license_texts))
        for source, targets in edges.items():
            self.assertIn(source, refs)
            for target in targets:
                self.assertTrue(target in refs or target.startswith("urn:go:"), target)
        runtime = next(ref for ref in direct if ref.startswith("urn:telegram-mcp:pinned:appimage-type2-runtime"))
        self.assertEqual(len(edges[runtime]), 1)


class LicenseMarkerTests(unittest.TestCase):
    def check(self, text, expected):
        self.assertEqual(native.classify_copyright(text)[0], expected, text)

    def test_lgpl_is_not_gpl(self):
        self.check(DEP5 + "Files: *\nLicense: LGPL-2.1+\n\nLicense: LGPL-2.1+\n On Debian systems, see "
                   "/usr/share/common-licenses/LGPL-2.1.\n", "no-gpl-marker")
        self.check("This library is free software; you can redistribute it under the terms of the GNU Lesser "
                   "General Public License version 2.1.\n", "no-gpl-marker")
        self.check("Licensed under the GNU Library General Public License, LGPLv2+.\n", "no-gpl-marker")

    def test_gpl_and_agpl_are_review_required(self):
        self.check(DEP5 + "Files: *\nLicense: GPL-2+\n", "review-required")
        self.check(DEP5 + "Files: src/*\nLicense: AGPL-3+\n", "review-required")
        self.check(DEP5 + "Files: *\nLicense: LGPL-2+ and GPL-3+\n", "review-required")
        self.check("Released under the GNU General Public License v2.\n", "review-required")
        self.check("Licensed under GPLv3.\n", "review-required")

    def test_alternative_definition_and_build_only_gpl(self):
        self.check(DEP5 + "Files: *\nLicense: LGPL-3+ or GPL-2+\n\nLicense: GPL-2+\n text\n",
                   "gpl-only-as-alternative-or-definition")
        self.check(DEP5 + "Files: *\nLicense: Expat\n\nFiles: tests/* build-aux/* debian/* m4/foo.m4\n"
                   "License: GPL-3+\n", "gpl-build-or-test-files-only")
        self.check(DEP5 + "Files: *\nLicense: Expat\n\nFiles: tests/* src/lib.c\nLicense: GPL-3+\n",
                   "review-required")
        self.check(DEP5 + "Files: rtsp/*\nLicense: MIT/X11 (BSD like) LGPL-2+\n", "no-gpl-marker")
        self.check(DEP5 + "Files: *\nLicense: Apache-2.0-with-GPL2-LGPL2-Exception\n",
                   "gpl-only-as-alternative-or-definition")


class ReviewTests(NativeFixture):
    def gpl_package(self, text=DEP5 + "Files: *\nLicense: GPL-2+\n"):
        self.host.add("libgpl0", {f"/{TRIPLET}/libgpl.so.0": elf(body=b"gpl")}, copyright_text=text)
        self.count = getattr(self, "count", 0) + 1
        root = self.appdir(f"AppDir{self.count}")
        (root / "usr/lib/libgpl.so.0").write_bytes(elf(body=b"gpl"))
        return root

    def test_unreviewed_gpl_package_fails_closed(self):
        message = self.assertFails(r"forbidden GPL/AGPL legal markers need an explicit review",
                                   self.classify, self.gpl_package())
        self.assertIn("libgpl0 1.0-1", message)
        self.assertIn("Files: * | License: GPL-2+", message)

    def test_review_is_pinned_to_the_exact_copyright_text(self):
        text = DEP5 + "Files: *\nLicense: GPL-2+\n"
        digest = native.sha256_bytes(text.encode())
        self.reviews({"libgpl0": [{"copyright_sha256": digest, "decision": "allowed", "reason": "test reason"}]})
        model = self.classify(self.gpl_package(text))
        package = next(p for p in model["packages"] if p["name"] == "libgpl0")
        self.assertEqual(package["license_review"], "test reason")
        self.reviews({"libgpl0": [{"copyright_sha256": "0" * 64, "decision": "allowed", "reason": "stale"}]})
        self.assertFails(r"need an explicit review", self.classify, self.gpl_package(text))
        self.reviews({"libgpl0": [{"copyright_sha256": digest, "decision": "forbidden", "reason": "GPL library"}]})
        self.assertFails(r"libgpl0 1\.0-1 is forbidden by its license review: GPL library",
                         self.classify, self.gpl_package(text))


class DebProfileTests(NativeFixture):
    def deb(self):
        root = self.base / "deb"
        notices = b"go notices\n"
        files = {"opt/telegram-mcp/telegram-mcp": elf(body=b"app"),
                 "opt/telegram-mcp/telegram-mcp-bridge": elf(body=b"bridge"),
                 native.ICON_PATH: self.icon,
                 "usr/share/applications/telegram-mcp.desktop":
                     self.desktop.replace("Exec=telegram-mcp", "Exec=/opt/telegram-mcp/telegram-mcp").encode(),
                 "usr/share/doc/telegram-mcp/copyright": notices,
                 "usr/share/doc/telegram-mcp/THIRD-PARTY-NOTICES.txt": notices}
        for path, data in files.items():
            (root / path).parent.mkdir(parents=True, exist_ok=True)
            (root / path).write_bytes(data)
        (root / "usr/bin").mkdir(parents=True)
        os.symlink("/opt/telegram-mcp/telegram-mcp", root / "usr/bin/telegram-mcp")
        return root

    def test_deb_payload_is_fully_covered_without_debian_packages(self):
        model = self.classify(self.deb(), "linux-deb")
        classes = {r["path"]: r["class"] for r, _ in model["files"]}
        self.assertEqual(classes["usr/bin/telegram-mcp"], "symlink")
        self.assertEqual(classes["usr/share/applications/telegram-mcp.desktop"], "project-asset")
        self.assertEqual(model["packages"], [])

    def test_deb_extra_native_file_fails(self):
        root = self.deb()
        (root / "opt/telegram-mcp/libextra.so").write_bytes(elf())
        self.assertFails(r"unmapped payload file in linux-deb: opt/telegram-mcp/libextra\.so",
                         self.classify, root, "linux-deb")


def fake_runtime(update=b""):
    """A minimal ELF64 type-2 AppImage runtime with appimagetool's sections."""
    if True:
        names = [b"", b".shstrtab", b".digest_md5", b".upd_info", b".sha256_sig", b".sig_key", b".text"]
        strtab = b"\0".join(names) + b"\0"
        offsets, position = [], 0
        for name in names:
            offsets.append(position)
            position += len(name) + 1
        body = bytearray(512)
        body[:len(strtab)] = strtab
        sections = [(0, 0), (64, len(strtab)), (300, 16), (320, 32), (360, 32), (400, 32), (440, 40)]
        image = bytearray(elf()) + body
        image[440:450] = b"runtime!!!"
        if update:
            image[320:320 + len(update)] = update
        shoff = len(image)
        struct.pack_into("<HH", image, 16, 2, 62)
        image[8:11] = b"AI\x02"
        struct.pack_into("<Q", image, 40, shoff)
        struct.pack_into("<HHH", image, 58, 64, len(names), 1)
        for index, (offset, size) in enumerate(sections):
            image += struct.pack("<IIQQQQIIQQ", offsets[index], 1, 0, 0, offset, size, 0, 0, 1, 0)
        return bytes(image)


class RuntimeTests(NativeFixture):
    def runtime(self, update=b""):
        return fake_runtime(update)

    def test_runtime_matches_pin_outside_appimagetool_sections(self):
        clean = self.runtime()
        self.runtime_bytes = clean
        self.write_pins()
        pins, _ = native.load_pins(self.data)
        artifact = self.base / "a.AppImage"
        artifact.write_bytes(self.runtime(update=b"gh-releases-zsync|x") + b"hsqs" + b"\0" * 32)
        self.assertEqual(native.appimage_runtime(artifact, "amd64", pins),
                         native.sha256_bytes(self.runtime(update=b"gh-releases-zsync|x")))
        tampered = bytearray(self.runtime())
        tampered[445] ^= 1
        artifact.write_bytes(bytes(tampered) + b"hsqs")
        self.assertFails(r"does not match the pinned type-2 runtime", native.appimage_runtime, artifact, "amd64", pins)
        artifact.write_bytes(clean + b"nope")
        self.assertFails(r"SquashFS does not follow", native.appimage_runtime, artifact, "amd64", pins)


class PinTests(NativeFixture):
    def test_tampered_pinned_legal_text_fails(self):
        (self.data / "legal/mit.txt").write_text("changed\n")
        self.assertFails(r"pinned legal text mit\.txt does not match", native.load_pins, self.data)

    def test_repository_pins_and_legal_texts_verify(self):
        pins, texts = native.load_pins()
        self.assertIn("libfuse-LGPL2.txt", texts)
        self.assertEqual(pins["inputs"]["appimagekit-apprun"]["architectures"]["amd64"]["sha256"],
                         "f30140a43a0a59e46db21bdefdf749b9e9f2c6946e92afabbacf98b8ae73fb4f")
        script = (ROOT / "scripts/build-linux.sh").read_text()
        for key in ("appimage-type2-runtime", "appimagekit-apprun", "linuxdeploy"):
            for arch in ("amd64", "arm64"):
                self.assertIn(pins["inputs"][key]["architectures"][arch]["sha256"], script, (key, arch))
        reviews = native.load_reviews()
        self.assertTrue(all(isinstance(v, list) and all(r["reason"].strip() for r in v) for v in reviews.values()))


class CommandLineTests(NativeFixture):
    """build-inventory.py --native-root end to end with a fake go tool."""

    def setUp(self):
        super().setUp()
        self.tools = self.base / "tools"
        self.tools.mkdir()
        self.buildinfo = self.base / "buildinfo"
        self.buildinfo.mkdir()
        goroot = self.base / "goroot"
        goroot.mkdir()
        (goroot / "VERSION").write_text("go1.26.5\n")
        # validate-inventory.py requires a full-length toolchain licence.
        (goroot / "LICENSE").write_text("Copyright The Go Authors.\n" + "Redistribution terms. " * 40 + "\n")
        cache = self.base / "gomodcache"
        (cache / "example.com/linked@v1.0.0").mkdir(parents=True)
        (cache / "example.com/linked@v1.0.0/LICENSE").write_text("MIT License\n")
        go = self.tools / "go"
        go.write_text(f"#!{sys.executable}\n" + '''import hashlib, json, os, pathlib, sys
args = sys.argv[1:]
if args == ["env", "GOMODCACHE"]:
    print(os.environ["TEST_GOMODCACHE"])
elif args == ["env", "-json", "GOROOT", "GOVERSION"]:
    print(json.dumps({"GOROOT": os.environ["TEST_GOROOT"], "GOVERSION": "go1.26.5"}))
elif args[:3] == ["version", "-m", "-json"]:
    path = pathlib.Path(args[3])
    print((pathlib.Path(os.environ["TEST_BUILDINFO"]) / (path.name + ".json")).read_text())
else:
    raise SystemExit("unexpected go invocation " + repr(args))
''')
        go.chmod(0o755)
        for name in ("telegram-mcp", "telegram-mcp-bridge"):
            (self.buildinfo / (name + ".json")).write_text(json.dumps({
                "GoVersion": "go1.26.5", "Path": "example.com/cmd/" + name,
                "Main": {"Path": "example.com", "Version": "(devel)"},
                "Deps": [{"Path": "example.com/linked", "Version": "v1.0.0", "Sum": "h1:x"}],
                "Settings": [{"Key": "GOOS", "Value": "linux"}, {"Key": "GOARCH", "Value": "amd64"}]}))
        self.preamble = self.base / "credits.md"
        self.preamble.write_text("# Credits\n")
        self.license = self.base / "LICENSE"
        self.license.write_text("Project license\n")
        self.env = dict(os.environ, PATH=f"{self.tools}{os.pathsep}{os.environ['PATH']}",
                        TEST_GOMODCACHE=str(cache), TEST_GOROOT=str(goroot), TEST_BUILDINFO=str(self.buildinfo))
        self.out = self.base / "out"

    def run_tool(self, root, *extra, ok=True):
        args = [sys.executable, str(SCRIPT), "--preamble", str(self.preamble), "--project-license", str(self.license),
                "--binary", str(root / "usr/bin/telegram-mcp"), "--binary", str(root / "usr/bin/telegram-mcp-bridge"),
                "--native-root", str(root), "--native-profile", "linux-appimage", "--native-arch", "amd64",
                "--native-host-root", str(self.host.root), "--native-data-dir", str(self.data),
                "--native-patchelf", str(self.patchelf), *extra]
        result = subprocess.run(args, env=self.env, text=True, capture_output=True)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def prepare(self):
        root = self.appdir()
        # The packaged Go notices must be exactly the notices of the payloads.
        notices = subprocess.run([sys.executable, str(SCRIPT), "--preamble", str(self.preamble),
                                  "--project-license", str(self.license), "--binary", str(root / "usr/bin/telegram-mcp"),
                                  "--binary", str(root / "usr/bin/telegram-mcp-bridge"), "--output-dir",
                                  str(self.base / "go-notices")], env=self.env, capture_output=True, text=True)
        self.assertEqual(notices.returncode, 0, notices.stderr)
        shutil.copy(self.base / "go-notices/THIRD-PARTY-NOTICES.txt", root / "usr/bin/THIRD-PARTY-NOTICES.txt")
        return root

    def snapshot(self):
        return {p.name: (p.read_bytes(), p.stat().st_mtime_ns) for p in self.out.iterdir()}

    def test_embed_generate_check_and_removed_manifest_entry(self):
        root = self.prepare()
        # Asset checks read the real repository's build/linux files.
        shutil.copy(ROOT / native.DESKTOP_SOURCE, root / "usr/share/applications/telegram-mcp.desktop")
        shutil.copy(ROOT / native.ICON_SOURCE, root / native.ICON_PATH)
        desktop = (ROOT / native.DESKTOP_SOURCE).read_text() + "X-AppImage-Version=0.0.0\n"
        (root / "telegram-mcp.desktop").write_text(desktop)
        self.run_tool(root, "--native-embed")
        self.assertTrue((root / native.EMBED_DIR / native.MANIFEST_NAME).is_file())
        artifact = self.base / "Telegram-MCP-x86_64.AppImage"
        artifact.write_bytes(b"not a real AppImage")
        failure = self.run_tool(root, "--artifact-only", "--artifact", str(artifact), "--output-dir", str(self.out),
                                ok=False)
        self.assertIn("not an ELF64 type-2 AppImage", failure.stderr)
        self.assertFalse(self.out.exists())
        # A pinned runtime prefix followed by SquashFS.
        runtime = fake_runtime()
        self.runtime_bytes = runtime
        self.write_pins()
        # Re-embed: the pinned runtime hash is part of the input records.
        self.run_tool(root, "--native-embed")
        artifact.write_bytes(runtime + b"hsqs" + b"\0" * 16)
        self.run_tool(root, "--artifact-only", "--artifact", str(artifact), "--output-dir", str(self.out))
        self.run_tool(root, "--artifact-only", "--check", "--artifact", str(artifact), "--output-dir", str(self.out))
        sbom_path = self.out / (artifact.name + ".cdx.json")
        notices = (self.out / (artifact.name + ".THIRD-PARTY-NOTICES.txt")).read_text()
        self.assertIn("# Native payload inventory", notices)
        self.assertIn("License: LGPL-2.1+", notices)
        sbom = json.loads(sbom_path.read_text())
        names = {c["name"] for c in sbom["components"] if c["type"] == "file"}
        self.assertIn("usr/lib/libfoo.so.1", names)
        # Negative: delete one native manifest entry from the artifact SBOM.
        sbom["components"] = [c for c in sbom["components"] if c["name"] != "usr/lib/libfoo.so.1"]
        sbom_path.write_bytes(native.canonical(sbom))
        before = self.snapshot()
        failure = self.run_tool(root, "--artifact-only", "--check", "--artifact", str(artifact),
                                "--output-dir", str(self.out), ok=False)
        self.assertIn("missing or stale generated inventory: Telegram-MCP-x86_64.AppImage.cdx.json", failure.stderr)
        self.assertEqual(self.snapshot(), before)
        # Negative: delete one entry from the embedded manifest inside the payload.
        self.run_tool(root, "--artifact-only", "--artifact", str(artifact), "--output-dir", str(self.out))
        manifest_path = root / native.EMBED_DIR / native.MANIFEST_NAME
        manifest = json.loads(manifest_path.read_text())
        manifest["files"] = [f for f in manifest["files"] if f["path"] != "usr/lib/libfoo.so.1"]
        manifest_path.write_bytes(native.canonical(manifest))
        before = self.snapshot()
        failure = self.run_tool(root, "--artifact-only", "--check", "--artifact", str(artifact),
                                "--output-dir", str(self.out), ok=False)
        self.assertIn("embedded native inventory is missing or stale", failure.stderr)
        self.assertEqual(self.snapshot(), before)
        # The release-set validator accepts the native SBOM/notices and rejects
        # a notice that dropped one Debian copyright text.
        release = self.base / "release"
        release.mkdir()
        for name in (artifact.name, artifact.name + ".cdx.json", artifact.name + ".THIRD-PARTY-NOTICES.txt"):
            shutil.copy(artifact if name == artifact.name else self.out / name, release / name)
        validator = [sys.executable, str(ROOT / "scripts/validate-inventory.py"), "--artifact",
                     str(release / artifact.name), "--profile", "linux-appimage", "--goos", "linux"]
        result = subprocess.run(validator, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        notice_path = release / (artifact.name + ".THIRD-PARTY-NOTICES.txt")
        notice_path.write_text(notice_path.read_text().replace("License: LGPL-2.1+", "License: removed"))
        result = subprocess.run(validator, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("native notices do not reproduce every SBOM copyright/legal text", result.stdout + result.stderr)
        result = subprocess.run([*validator[:-4], "--profile", "linux-deb"], text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_packaged_go_notices_must_match_payloads(self):
        root = self.appdir()
        shutil.copy(ROOT / native.DESKTOP_SOURCE, root / "usr/share/applications/telegram-mcp.desktop")
        shutil.copy(ROOT / native.ICON_SOURCE, root / native.ICON_PATH)
        (root / "telegram-mcp.desktop").write_text((ROOT / native.DESKTOP_SOURCE).read_text()
                                                   + "X-AppImage-Version=0.0.0\n")
        failure = self.run_tool(root, "--native-embed", ok=False)
        self.assertIn("packaged inventory usr/bin/THIRD-PARTY-NOTICES.txt is not the Go notices", failure.stderr)


if __name__ == "__main__":
    unittest.main()
