#!/usr/bin/env python3
"""Tests for scripts/check-appimage-abi.py with real compiled ELF fixtures.

A tiny "system" library is built in two releases that share one version node
(as libgpg-error does): the old one lacks a function the new one adds. A
"bundled" library linked against the new release must then fail against a
baseline carrying the old one, and pass against the new one.
"""

import importlib.util
import os
import platform
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location("check_appimage_abi", ROOT / "scripts/check-appimage-abi.py")
assert SPEC is not None and SPEC.loader is not None
abi = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(abi)
ARCH = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine(), "")


def cc(*args):
    subprocess.run(["cc", *args], check=True, capture_output=True, text=True)


@unittest.skipUnless(ARCH, "fixtures are compiled for amd64 or arm64 hosts")
class ABICheckTests(unittest.TestCase):
    def setUp(self):
        if shutil.which("cc") is None:
            self.fail("a C compiler is required to build the ELF fixtures")
        self.base = Path(tempfile.mkdtemp(prefix="appimage-abi-"))
        self.addCleanup(shutil.rmtree, self.base)
        src = self.base / "src"
        src.mkdir()
        (src / "sys.map").write_text("SYS_1.0 { global: *; };\n")
        (src / "sys-new.map").write_text("SYS_1.0 { global: *; };\nSYS_2.0 { global: sys_v2; } SYS_1.0;\n")
        (src / "sys-old.c").write_text("int sys_old(void) { return 1; }\n")
        (src / "sys-new.c").write_text("int sys_old(void) { return 1; }\nint sys_added(void) { return 2; }\n")
        (src / "sys-v2.c").write_text("int sys_old(void) { return 1; }\nint sys_added(void) { return 2; }\n"
                                      "int sys_v2(void) { return 3; }\n")
        (src / "bundled.c").write_text("int sys_added(void);\nint bundled(void) { return sys_added(); }\n")
        (src / "bundled-v2.c").write_text("int sys_v2(void);\nint bundled(void) { return sys_v2(); }\n")
        (src / "main.c").write_text("int bundled(void);\nint main(void) { return bundled(); }\n")
        self.releases = {}
        for name, source, script in (("old", "sys-old.c", "sys.map"), ("new", "sys-new.c", "sys.map"),
                                     ("v2", "sys-v2.c", "sys-new.map")):
            out = self.base / f"lib-{name}"
            out.mkdir()
            cc("-shared", "-fPIC", "-o", str(out / "libsys.so.1"), "-Wl,-soname,libsys.so.1",
               f"-Wl,--version-script={src / script}", str(src / source))
            self.releases[name] = out

    def appdir(self, source, release, versioned=False):
        appdir = self.base / f"AppDir-{source}-{release}-{versioned}"
        lib = appdir / "usr/lib"
        lib.mkdir(parents=True)
        src = self.base / "src"
        libdir = self.releases[release]
        version = []
        if versioned:
            (src / "bundled.map").write_text("B_1.0 { global: *; };\n")
            version = [f"-Wl,--version-script={src / 'bundled.map'}"]
        cc("-shared", "-fPIC", "-o", str(lib / "libbundled.so.1"), "-Wl,-soname,libbundled.so.1",
           *version, str(src / source), "-L", str(libdir), "-l:libsys.so.1")
        (appdir / "usr/bin").mkdir()
        cc("-o", str(appdir / "usr/bin/app"), str(src / "main.c"), "-L", str(lib), "-l:libbundled.so.1",
           f"-Wl,-rpath-link,{libdir}")
        (lib / "libbundled.so").symlink_to("libbundled.so.1")
        return appdir

    def baseline(self, release, os_id="ubuntu", version="24.04"):
        root = self.base / f"baseline-{release}-{os_id}-{version}"
        triplet = abi.TRIPLETS[ARCH]
        for directory in (f"usr/lib/{triplet}", "etc"):
            (root / directory).mkdir(parents=True, exist_ok=True)
        (root / "etc/os-release").write_text(f'ID={os_id}\nVERSION_ID="{version}"\n')
        shutil.copy(self.releases[release] / "libsys.so.1", root / f"usr/lib/{triplet}/libsys.so.1.0")
        (root / f"usr/lib/{triplet}/libsys.so.1").symlink_to("libsys.so.1.0")
        # usr-merged layout: /lib is an absolute symlink that must stay inside root.
        (root / "lib").symlink_to("/usr/lib")
        for name in ("libc.so.6",):
            system = Path(f"/lib/{triplet}/{name}")
            shutil.copy(system.resolve(), root / f"usr/lib/{triplet}/{name}")
        return root

    def loader(self, root):
        triplet = abi.TRIPLETS[ARCH]
        interpreter = {"amd64": "ld-linux-x86-64.so.2", "arm64": "ld-linux-aarch64.so.1"}[ARCH]
        source = next(p for p in (Path(f"/lib/{triplet}/{interpreter}"), Path(f"/lib64/{interpreter}"),
                                  Path(f"/lib/{interpreter}")) if p.exists())
        shutil.copy(source.resolve(), root / f"usr/lib/{triplet}/{interpreter}")

    def run_check(self, appdir, root, baseline_os="ubuntu:24.04"):
        return abi.check(appdir, ARCH, root, baseline_os)

    def test_symbol_added_to_an_existing_version_node_fails(self):
        appdir = self.appdir("bundled.c", "new")
        root = self.baseline("old")
        self.loader(root)
        with self.assertRaisesRegex(abi.ABIError, r"sys_added@SYS_1\.0 from libsys\.so\.1, which ubuntu 24\.04 "
                                                  r"does not export"):
            self.run_check(appdir, root)

    def test_matching_baseline_passes_and_reports_system_libraries(self):
        appdir = self.appdir("bundled.c", "new")
        root = self.baseline("new")
        self.loader(root)
        unbundled = self.run_check(appdir, root)
        self.assertEqual(unbundled["libsys.so.1"], {"SYS_1.0"})
        self.assertIn("libc.so.6", unbundled)
        self.assertNotIn("libbundled.so.1", unbundled)

    def test_missing_version_node_fails(self):
        appdir = self.appdir("bundled-v2.c", "v2")
        root = self.baseline("new")
        self.loader(root)
        with self.assertRaisesRegex(abi.ABIError, r"needs SYS_2\.0 from libsys\.so\.1, which ubuntu 24\.04 "
                                                  r"does not define"):
            self.run_check(appdir, root)

    def test_library_absent_from_baseline_fails(self):
        appdir = self.appdir("bundled.c", "new")
        root = self.baseline("new")
        self.loader(root)
        (root / f"usr/lib/{abi.TRIPLETS[ARCH]}/libsys.so.1").unlink()
        with self.assertRaisesRegex(abi.ABIError, r"needs libsys\.so\.1, which is neither bundled nor installed"):
            self.run_check(appdir, root)

    def test_bundled_library_must_satisfy_its_dependents(self):
        appdir = self.appdir("bundled.c", "new", versioned=True)
        root = self.baseline("new")
        self.loader(root)
        self.run_check(appdir, root)
        # Replace the bundled library with one lacking the executable's bundled@B_1.0.
        lib = appdir / "usr/lib/libbundled.so.1"
        lib.unlink()
        other = self.base / "src/other.c"
        other.write_text("int unrelated(void) { return 0; }\n")
        cc("-shared", "-fPIC", "-o", str(lib), "-Wl,-soname,libbundled.so.1",
           f"-Wl,--version-script={self.base / 'src/bundled.map'}", str(other))
        with self.assertRaisesRegex(abi.ABIError, r"usr/bin/app needs bundled@B_1\.0 from libbundled\.so\.1, "
                                                  r"which bundled does not export"):
            self.run_check(appdir, root)

    def test_wrong_baseline_distribution_fails_closed(self):
        appdir = self.appdir("bundled.c", "new")
        root = self.baseline("new", "debian", "13")
        with self.assertRaisesRegex(abi.ABIError, r"is debian 13, not ubuntu 24\.04"):
            self.run_check(appdir, root)

    def test_command_line_exit_status(self):
        appdir = self.appdir("bundled.c", "new")
        root = self.baseline("old")
        self.loader(root)
        script = str(ROOT / "scripts/check-appimage-abi.py")
        env = dict(os.environ)
        failed = subprocess.run(["python3", "-I", script, str(appdir), "--arch", ARCH, "--baseline-root", str(root)],
                                capture_output=True, text=True, env=env)
        self.assertEqual(failed.returncode, 1, failed.stdout + failed.stderr)
        self.assertIn("sys_added@SYS_1.0", failed.stderr)
        passed_root = self.baseline("new")
        self.loader(passed_root)
        passed = subprocess.run(["python3", "-I", script, str(appdir), "--arch", ARCH,
                                 "--baseline-root", str(passed_root)], capture_output=True, text=True, env=env)
        self.assertEqual(passed.returncode, 0, passed.stdout + passed.stderr)
        self.assertIn("libsys.so.1 (needs up to SYS_1.0)", passed.stdout)

    def test_build_script_runs_the_check_on_the_final_artifact(self):
        script = (ROOT / "scripts/build-linux.sh").read_text()
        call = 'python3 "$repo/scripts/check-appimage-abi.py" "$appimage_extract" --arch "$arch"'
        self.assertIn(call, script)
        self.assertIn("--baseline-root / --baseline-os ubuntu:24.04", script)
        self.assertLess(script.index('python3 "$repo/scripts/extract-appimage.py" "$appimage"'), script.index(call))
        dockerfile = (ROOT / "build/linux/Dockerfile").read_text()
        self.assertIn("FROM ubuntu:24.04@sha256:", dockerfile)


if __name__ == "__main__":
    unittest.main()
