#!/usr/bin/env python3
"""Fail an AppImage whose bundled code needs a newer system than it supports.

An AppImage carries most of its libraries but deliberately leaves some to the
computer it runs on (glibc, the graphics stack, X11/Wayland, fonts, and every
library on linuxdeploy's exclude list, such as libgpg-error). A bundled library
copied from a newer distribution can require a symbol version that those
system libraries lack on the oldest supported distribution, and the dynamic
loader then refuses to start the app ("version `GPG_ERROR_1.49' not found").

Symbol versions alone are not enough: libgpg-error keeps one version node,
GPG_ERROR_1.0, and adds functions to it, so a libgcrypt from Debian 13 needs
gpgrt_add_post_log_func@GPG_ERROR_1.0, which Ubuntu 24.04's libgpg-error lacks.

This check reads, from each ELF file in the AppDir (or the extracted AppImage),
the libraries it needs (DT_NEEDED), the symbol versions it requires from them
(DT_VERNEED) and every versioned undefined symbol (.dynsym/.gnu.version).
Each requirement must be met:

* by a library bundled in the AppImage, which must define the version and
  export each such symbol with it, or else
* by the library of that soname in the baseline root, the oldest supported
  distribution (the build container), which must do the same.

A needed library found neither bundled nor in the baseline also fails. The
baseline's os-release must name the expected distribution, so building on a
newer host cannot silently become the baseline. Only ELF64 little-endian
(amd64, arm64) is read; the parser uses the dynamic segment, as the loader
does, and the dynamic symbol table from its section headers.

  check-appimage-abi.py APPDIR --arch amd64 --baseline-root / --baseline-os ubuntu:24.04
"""

import argparse
import os
import struct
import sys
from pathlib import Path

PT_LOAD, PT_DYNAMIC = 1, 2
SHT_DYNSYM, SHT_GNU_VERSYM = 11, 0x6FFFFFFF
STB_GLOBAL, STB_WEAK = 1, 2
DT_NULL, DT_NEEDED, DT_STRTAB, DT_STRSZ, DT_SONAME = 0, 1, 5, 10, 14
DT_VERDEF, DT_VERDEFNUM = 0x6FFFFFFC, 0x6FFFFFFD
DT_VERNEED, DT_VERNEEDNUM = 0x6FFFFFFE, 0x6FFFFFFF
VER_FLG_WEAK = 0x2
MACHINES = {"amd64": 62, "arm64": 183}
TRIPLETS = {"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}


class ABIError(Exception):
    pass


class Dynamic:
    """The dynamic linking facts of one ELF file."""

    def __init__(self, soname, needed, verneed, verdef, imports=(), exports=frozenset()):
        self.soname = soname
        self.needed = needed        # [soname]
        self.verneed = verneed      # {soname: {version: weak}}
        self.verdef = verdef        # {version}
        self.imports = imports      # [(soname, version, symbol)], strong references only
        self.exports = exports      # {(symbol, version or None)}


def read_dynamic(path, machine):
    """Dynamic facts of path, or None when it is not a dynamic ELF file."""
    data = Path(path).read_bytes()
    if data[:4] != b"\x7fELF":
        return None
    if data[4] != 2 or data[5] != 1:
        raise ABIError(f"{path}: only ELF64 little-endian is supported")
    e_type, e_machine = struct.unpack_from("<HH", data, 16)
    if e_machine != machine:
        raise ABIError(f"{path}: ELF machine {e_machine}, expected {machine}")
    e_phoff, = struct.unpack_from("<Q", data, 0x20)
    e_phentsize, e_phnum = struct.unpack_from("<HH", data, 0x36)
    loads, dynamic = [], None
    for i in range(e_phnum):
        p_type, _flags, p_offset, p_vaddr, _paddr, p_filesz, _memsz, _align = struct.unpack_from(
            "<IIQQQQQQ", data, e_phoff + i * e_phentsize)
        if p_type == PT_LOAD:
            loads.append((p_vaddr, p_offset, p_filesz))
        elif p_type == PT_DYNAMIC:
            dynamic = (p_offset, p_filesz)
    if dynamic is None:
        return None  # static executable (the Go bridge) or object without dynamic linking

    def offset(vaddr):
        for start, file_offset, size in loads:
            if start <= vaddr < start + size:
                return file_offset + vaddr - start
        raise ABIError(f"{path}: address {vaddr:#x} is outside every PT_LOAD segment")

    entries = []
    for pos in range(dynamic[0], dynamic[0] + dynamic[1], 16):
        tag, value = struct.unpack_from("<qQ", data, pos)
        if tag == DT_NULL:
            break
        entries.append((tag, value))
    single = {tag: value for tag, value in entries}
    if DT_STRTAB not in single:
        raise ABIError(f"{path}: dynamic segment without DT_STRTAB")
    strtab = offset(single[DT_STRTAB])
    strsz = single.get(DT_STRSZ, len(data) - strtab)

    def string(index):
        if index >= strsz:
            raise ABIError(f"{path}: string index {index} outside DT_STRTAB")
        start = strtab + index
        end = data.index(b"\0", start)
        return data[start:end].decode("utf-8", "replace")

    needed = [string(value) for tag, value in entries if tag == DT_NEEDED]
    soname = string(single[DT_SONAME]) if DT_SONAME in single else None
    verneed, needed_index = {}, {}
    if DT_VERNEED in single:
        pos = offset(single[DT_VERNEED])
        for _ in range(single.get(DT_VERNEEDNUM, 0)):
            _version, count, file_name, aux, following = struct.unpack_from("<HHIII", data, pos)
            versions = verneed.setdefault(string(file_name), {})
            apos = pos + aux
            for _ in range(count):
                _hash, flags, other, name, anext = struct.unpack_from("<IHHII", data, apos)
                versions[string(name)] = bool(flags & VER_FLG_WEAK) and versions.get(string(name), True)
                needed_index[other & 0x7FFF] = (string(file_name), string(name))
                if not anext:
                    break
                apos += anext
            if not following:
                break
            pos += following
    verdef, defined_index = set(), {}
    if DT_VERDEF in single:
        pos = offset(single[DT_VERDEF])
        for _ in range(single.get(DT_VERDEFNUM, 0)):
            _version, _flags, ndx, count, _hash, aux, following = struct.unpack_from("<HHHHIII", data, pos)
            if count:
                name, _next = struct.unpack_from("<II", data, pos + aux)
                verdef.add(string(name))
                defined_index[ndx & 0x7FFF] = string(name)
            if not following:
                break
            pos += following
    imports, exports = [], set()
    for name, shndx, binding, version_index in dynamic_symbols(path, data):
        if not name or binding not in (STB_GLOBAL, STB_WEAK):
            continue
        if shndx == 0:
            # A weak undefined reference may stay unresolved; an unversioned one
            # binds to whichever loaded library defines it first.
            if binding == STB_GLOBAL and version_index in needed_index:
                file_name, version = needed_index[version_index]
                imports.append((file_name, version, name))
        else:
            exports.add((name, defined_index.get(version_index)))
    return Dynamic(soname, needed, verneed, verdef, imports, frozenset(exports))


def dynamic_symbols(path, data):
    """(name, section index, binding, version index) of each .dynsym entry."""
    e_shoff, = struct.unpack_from("<Q", data, 0x28)
    e_shentsize, e_shnum = struct.unpack_from("<HH", data, 0x3A)
    if not e_shoff or not e_shnum:
        raise ABIError(f"{path}: no section headers; cannot read its dynamic symbols")
    sections = []
    for i in range(e_shnum):
        _name, sh_type, _flags, _addr, sh_offset, sh_size, sh_link, _info, _align, sh_entsize = struct.unpack_from(
            "<IIQQQQIIQQ", data, e_shoff + i * e_shentsize)
        sections.append((sh_type, sh_offset, sh_size, sh_link, sh_entsize))
    dynsym = next((s for s in sections if s[0] == SHT_DYNSYM), None)
    if dynsym is None:
        raise ABIError(f"{path}: dynamic ELF without .dynsym")
    versym = next((s for s in sections if s[0] == SHT_GNU_VERSYM), None)
    _type, sym_offset, sym_size, link, entsize = dynsym
    str_offset, str_size = sections[link][1], sections[link][2]
    entsize = entsize or 24
    for index in range(sym_size // entsize):
        st_name, st_info, _other, st_shndx = struct.unpack_from("<IBBH", data, sym_offset + index * entsize)
        if st_name >= str_size:
            raise ABIError(f"{path}: symbol name outside .dynstr")
        end = data.index(b"\0", str_offset + st_name)
        name = data[str_offset + st_name:end].decode("utf-8", "replace")
        version_index = 1
        if versym is not None:
            version_index, = struct.unpack_from("<H", data, versym[1] + index * 2)
            version_index &= 0x7FFF  # the hidden bit still satisfies explicit sym@VERSION
        yield name, st_shndx, st_info >> 4, version_index


def read_os_release(root):
    for candidate in ("etc/os-release", "usr/lib/os-release"):
        path = resolve(root, "/" + candidate)
        if path and path.is_file():
            values = {}
            for line in path.read_text(errors="replace").splitlines():
                key, sep, value = line.partition("=")
                if sep:
                    values[key.strip()] = value.strip().strip('"')
            return values
    raise ABIError(f"baseline root {root} has no os-release")


def resolve(root, absolute):
    """absolute (a path inside root) with every symlink resolved inside root."""
    root = Path(root)
    parts = [p for p in absolute.split("/") if p]
    current = []
    hops = 0
    while parts:
        part = parts.pop(0)
        if part == ".":
            continue
        if part == "..":
            if current:
                current.pop()
            continue
        candidate = root.joinpath(*current, part)
        if candidate.is_symlink():
            hops += 1
            if hops > 40:
                return None
            target = os.readlink(candidate)
            if target.startswith("/"):
                current = []
            parts = [p for p in target.split("/") if p] + parts
            continue
        if not candidate.exists():
            return None
        current.append(part)
    return root.joinpath(*current)


def baseline_dirs(arch):
    triplet = TRIPLETS[arch]
    return [f"/lib/{triplet}", f"/usr/lib/{triplet}", "/lib64", "/usr/lib64", "/lib", "/usr/lib"]


def check(appdir, arch, baseline_root, baseline_os):
    machine = MACHINES[arch]
    expected_id, _, expected_version = baseline_os.partition(":")
    release = read_os_release(baseline_root)
    if (release.get("ID"), release.get("VERSION_ID")) != (expected_id, expected_version):
        raise ABIError(
            f"baseline root {baseline_root} is {release.get('ID')} {release.get('VERSION_ID')}, "
            f"not {expected_id} {expected_version}: build the AppImage in build/linux/Dockerfile")
    appdir = Path(appdir)
    if not appdir.is_dir():
        raise ABIError(f"{appdir} is not a directory")
    bundled_files = {}
    for path in sorted(appdir.rglob("*")):
        if path.is_symlink() or not path.is_file():
            continue
        facts = read_dynamic(path, machine)
        if facts is not None:
            bundled_files[path] = facts
    if not bundled_files:
        raise ABIError(f"{appdir}: no dynamic ELF files found")
    bundled = {}
    for path, facts in bundled_files.items():
        for name in {path.name, facts.soname} - {None}:
            bundled.setdefault(name, facts)
    for path in appdir.rglob("*"):
        # Symlinked sonames (libfoo.so.1 -> libfoo.so.1.2.3) count as bundled too.
        if path.is_symlink() and path.name not in bundled:
            target = path.resolve()
            for candidate, facts in bundled_files.items():
                if candidate.resolve() == target:
                    bundled[path.name] = facts
                    break
    baseline = {}

    def system(soname):
        if soname not in baseline:
            baseline[soname] = None
            for directory in baseline_dirs(arch):
                path = resolve(baseline_root, f"{directory}/{soname}")
                if path and path.is_file():
                    baseline[soname] = (path, read_dynamic(path, machine))
                    break
        return baseline[soname]

    failures, unbundled = [], {}
    for path, facts in bundled_files.items():
        relative = path.relative_to(appdir)
        for soname in sorted(set(facts.needed) | set(facts.verneed)):
            versions = facts.verneed.get(soname, {})
            if soname in bundled:
                provider, where = bundled[soname], "bundled"
            else:
                found = system(soname)
                if found is None:
                    failures.append(f"{relative} needs {soname}, which is neither bundled "
                                    f"nor installed on {expected_id} {expected_version}")
                    continue
                provider, where = found[1], f"{expected_id} {expected_version}"
                unbundled.setdefault(soname, set()).update(versions)
            if provider is None:
                if any(not weak for weak in versions.values()):
                    failures.append(f"{relative} needs versioned symbols of {soname}, "
                                    f"which is not a dynamic library ({where})")
                continue
            for version, weak in sorted(versions.items()):
                if version not in provider.verdef and not weak:
                    failures.append(f"{relative} needs {version} from {soname}, "
                                    f"which {where} does not define")
            if not provider.verdef:
                continue  # an unversioned library satisfies by name at run time
            for file_name, version, symbol in facts.imports:
                if file_name == soname and (symbol, version) not in provider.exports:
                    failures.append(f"{relative} needs {symbol}@{version} from {soname}, "
                                    f"which {where} does not export")
    if failures:
        raise ABIError("AppImage ABI check failed:\n  " + "\n  ".join(sorted(set(failures))))
    return unbundled


def version_key(version):
    head, _, tail = version.rpartition("_")
    numbers = tuple(int(p) for p in tail.split(".") if p.isdigit())
    return head, numbers


def main(argv=None):
    parser = argparse.ArgumentParser(description="Fail an AppImage whose bundled code needs a newer system than it supports.")
    parser.add_argument("appdir", help="AppDir or extracted AppImage")
    parser.add_argument("--arch", choices=sorted(MACHINES), required=True)
    parser.add_argument("--baseline-root", default="/",
                        help="root of the oldest supported distribution (default /)")
    parser.add_argument("--baseline-os", default="ubuntu:24.04",
                        help="ID:VERSION_ID the baseline os-release must name")
    args = parser.parse_args(argv)
    try:
        unbundled = check(args.appdir, args.arch, args.baseline_root, args.baseline_os)
    except ABIError as err:
        print(err, file=sys.stderr)
        return 1
    print(f"AppImage ABI check passed against {args.baseline_os}; libraries left to the system:")
    for soname in sorted(unbundled):
        newest = {}
        for version in unbundled[soname]:
            head, numbers = version_key(version)
            if head not in newest or numbers > version_key(newest[head])[1]:
                newest[head] = version
        print(f"  {soname}" + (f" (needs up to {', '.join(sorted(newest.values()))})" if newest else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
