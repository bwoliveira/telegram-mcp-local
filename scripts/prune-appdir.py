#!/usr/bin/env python3
"""Remove host-specific extras from a linuxdeploy AppDir before inventory (stdlib only).

  python3 scripts/prune-appdir.py APPDIR --arch amd64 --remove usr/lib/gtk-3.0/modules

Deletes each --remove path, then exactly the top-level usr/lib libraries that
were reachable through DT_NEEDED before the removal and are no longer reachable
from any remaining ELF file, plus the /usr/share/doc/<package> copies linuxdeploy
made for Debian packages that no longer contribute a library. Libraries that
were already unreachable before (deployed on purpose by a plugin) are kept.
With --schema-owner REGEX, GSettings schema files whose Debian owner package
does not match are removed and gschemas.compiled is rebuilt with the host
glib-compile-schemas (linuxdeploy-plugin-gtk copies every host schema).
Stanzas naming a removed module are dropped from the GTK immodules and
gdk-pixbuf loaders caches so the runtime never tries to open a missing file.
The native inventory then covers whatever remains, fail-closed.
"""
import argparse
import os
import re
import subprocess
from pathlib import Path
import shutil
import struct
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import native_inventory  # noqa: E402

PT_LOAD, PT_DYNAMIC, DT_NEEDED, DT_STRTAB = 1, 2, 1, 5
CACHES = ("usr/lib/gdk-pixbuf-2.0/2.10.0/loaders.cache", "usr/lib/gtk-3.0/3.0.0/immodules.cache")


def prune_cache(path, removed_names):
    """Drop cache stanzas whose first quoted path names a removed module."""
    if path.is_symlink() or not path.is_file():
        return []
    text = path.read_text(encoding="utf-8")
    stanzas = text.split("\n\n")
    kept, dropped = [], []
    for stanza in stanzas:
        first = next((line for line in stanza.splitlines() if line.startswith('"')), "")
        module = first.split('"')[1] if first.count('"') >= 2 else ""
        if module and Path(module).name in removed_names:
            dropped.append(module)
        else:
            kept.append(stanza)
    if dropped:
        path.write_text("\n\n".join(kept), encoding="utf-8")
    return dropped


def needed(path):
    """DT_NEEDED names of a little-endian ELF64 file, or None if not ELF."""
    data = Path(path).read_bytes()
    if data[:4] != b"\x7fELF":
        return None
    if data[4] != 2 or data[5] != 1:
        raise SystemExit(f"prune-appdir: unsupported ELF class/byte order: {path}")
    phoff = struct.unpack_from("<Q", data, 32)[0]
    phentsize, phnum = struct.unpack_from("<HH", data, 54)
    loads, dynamic = [], None
    for index in range(phnum):
        kind, _, offset, vaddr, _, filesz, _, _ = struct.unpack_from("<IIQQQQQQ", data, phoff + index * phentsize)
        if kind == PT_LOAD:
            loads.append((vaddr, offset, filesz))
        elif kind == PT_DYNAMIC:
            dynamic = (offset, filesz)
    if dynamic is None:
        return []
    entries, strtab = [], None
    for position in range(dynamic[0], dynamic[0] + dynamic[1], 16):
        tag, value = struct.unpack_from("<qQ", data, position)
        if tag == 0:
            break
        if tag == DT_NEEDED:
            entries.append(value)
        elif tag == DT_STRTAB:
            strtab = value
    if strtab is None:
        return []
    base = next((offset + strtab - vaddr for vaddr, offset, size in loads if vaddr <= strtab < vaddr + size), None)
    if base is None:
        raise SystemExit(f"prune-appdir: DT_STRTAB outside PT_LOAD in {path}")
    names = []
    for value in entries:
        end = data.index(b"\0", base + value)
        names.append(data[base + value:end].decode("utf-8"))
    return names


def reachable(appdir):
    libdir = appdir / "usr/lib"
    elves = {}
    for directory, _, files in os.walk(appdir):
        for name in files:
            path = Path(directory) / name
            if path.is_symlink() or not path.is_file():
                continue
            names = needed(path)
            if names is not None:
                elves[path] = names
    top = {path.name: path for path in elves if path.parent == libdir}
    seen, stack = set(), [path for path in elves if path.parent != libdir]
    while stack:
        current = stack.pop()
        for name in elves.get(current, []):
            library = top.get(name)
            if library is not None and library not in seen:
                seen.add(library)
                stack.append(library)
    return seen, top


def prune_schemas(appdir, owner, arch):
    schemas = appdir / "usr/share/glib-2.0/schemas"
    if schemas.is_symlink() or not schemas.is_dir():
        return []
    dpkg = native_inventory.Dpkg()
    removed = []
    for path in sorted(schemas.iterdir()):
        if path.name == "gschemas.compiled" or path.is_symlink() or not path.is_file():
            continue
        relative = path.relative_to(appdir).as_posix()
        packages = {native_inventory.owner_of(dpkg, real, arch)
                    for real in native_inventory.host_origins(dpkg, relative, arch)}
        names = {p.split(":", 1)[0] for p in packages if p}
        if len(names) != 1 or not owner.fullmatch(next(iter(names))):
            removed.append(path)
            path.unlink()
            print(f"prune-appdir: removed schema {relative} (owner {', '.join(sorted(names)) or 'none'})")
    if removed:
        subprocess.run(["glib-compile-schemas", str(schemas)], check=True)
    return removed


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("appdir", type=Path)
    parser.add_argument("--arch", required=True, choices=sorted(native_inventory.TRIPLETS))
    parser.add_argument("--remove", action="append", default=[], help="AppDir-relative path to delete (repeatable)")
    parser.add_argument("--schema-owner", help="keep only GSettings schemas owned by packages matching this regex")
    args = parser.parse_args()
    appdir = args.appdir.resolve()
    before, _ = reachable(appdir)
    removed = []
    if args.schema_owner:
        removed += prune_schemas(appdir, re.compile(args.schema_owner), args.arch)
    for relative in args.remove:
        target = appdir / relative
        if ".." in Path(relative).parts or Path(relative).is_absolute() or \
                not Path(os.path.abspath(target)).is_relative_to(appdir):
            raise SystemExit(f"prune-appdir: refusing path outside the AppDir: {relative}")
        if target.is_dir() and not target.is_symlink():
            removed += [p for p in target.rglob("*") if p.is_file() and not p.is_symlink()]
            shutil.rmtree(target)
        elif os.path.lexists(target):
            removed.append(target)
            target.unlink()
    removed_names = {path.name for path in removed}
    for cache in CACHES:
        for module in prune_cache(appdir / cache, removed_names):
            print(f"prune-appdir: dropped {module} from {cache}")
    after, _ = reachable(appdir)
    orphans = sorted(before - after)
    for library in orphans:
        library.unlink()
    for directory, _, files in os.walk(appdir):
        for name in files:
            path = Path(directory) / name
            if path.is_symlink() and not path.exists():
                path.unlink()
                print(f"prune-appdir: removed dangling symlink {path.relative_to(appdir)}")
    # Copyright copies linuxdeploy made for packages that no longer contribute a library.
    dpkg = native_inventory.Dpkg()
    gone = set()
    for path in [*removed, *orphans]:
        relative = path.relative_to(appdir).as_posix()
        for real in native_inventory.host_origins(dpkg, relative, args.arch):
            package = native_inventory.owner_of(dpkg, real, args.arch)
            if package:
                gone.add(package.split(":", 1)[0])
    remaining = set()
    for directory, _, files in os.walk(appdir / "usr/lib"):
        for name in files:
            relative = (Path(directory) / name).relative_to(appdir).as_posix()
            for real in native_inventory.host_origins(dpkg, relative, args.arch):
                package = native_inventory.owner_of(dpkg, real, args.arch)
                if package:
                    remaining.add(package.split(":", 1)[0])
    for package in sorted(gone - remaining):
        doc = appdir / "usr/share/doc" / package
        if doc.is_dir() and not doc.is_symlink():
            shutil.rmtree(doc)
            print(f"prune-appdir: removed usr/share/doc/{package}")
    for path in removed:
        print(f"prune-appdir: removed {path.relative_to(appdir)}")
    for library in orphans:
        print(f"prune-appdir: removed now-unreferenced {library.relative_to(appdir)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
