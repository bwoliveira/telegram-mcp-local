#!/usr/bin/env python3
"""Generate reproducible notices and CycloneDX 1.6 from built Go executables.

Inventories `Deps` from `go version -m -json` plus the exact Go toolchain license,
never `go list -m all`.
No network, package installation, SPDX guessing, timestamps or random IDs are used.

Example:
  python3 scripts/build-inventory.py --binary build/bin/app.exe \
      --binary build/bin/bridge.exe --artifact build/bin/Setup.exe \
      --component build/input/WebView2Setup.exe --output-dir build/bin

Outputs: <binary>.THIRD-PARTY-NOTICES.txt, THIRD-PARTY-NOTICES.txt,
<binary>.cdx.json and <artifact>.cdx.json. --check compares without writing.
Artifact SBOMs bind the supplied Go payloads and explicitly supplied native or
runtime components; they do not inspect an installer archive automatically,
except with --native-root: then every regular file and symlink of the payload
extracted from the final Linux AppImage/DEB is mapped by scripts/native_inventory.py
(Go payload, project asset, generated inventory, pinned input or originating
Debian package with its complete copyright file) and fails closed when any file
is unmapped, a legal text is missing or a GPL/AGPL marker is unreviewed.

  python3 scripts/build-inventory.py --native-embed --native-root APPDIR \
      --native-profile linux-appimage --native-arch amd64 --binary APPDIR/usr/bin/telegram-mcp ...
writes the native manifest/notices into APPDIR before packing; the final
--artifact-only run regenerates them from the extracted artifact and requires
byte equality with the embedded copies.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePath
import re
import subprocess
import sys
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[1]
NATIVE_MARKER = "\n\n# Native payload inventory\n\n"
LEGAL_NAME = re.compile(r"^(?:LICENSE|LICENCE|COPYING|NOTICE|COPYRIGHT|UNLICENSE)(?:$|[._-].+)", re.I)
SCOPE = ("Linked Go modules and the matching Go toolchain's standard-library license; "
         "excludes native libraries, installer tooling, downloaded sidecars and runtime assets. "
         "Module-level legal files are retained conservatively; license names are "
         "source filenames, not inferred SPDX classifications.")


class InventoryError(Exception):
    """An input cannot be inventoried safely."""


def run_go(*args):
    result = subprocess.run(["go", *args], capture_output=True, text=True, encoding="utf-8")
    if result.returncode:
        raise InventoryError(f"go {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest()


def read_text(path):
    return path.read_bytes().decode("utf-8")


def escape_module(value):
    # Go's module cache escaping is ASCII case escaping, not URL escaping.
    return "".join("!" + char.lower() if "A" <= char <= "Z" else char for char in value)


def module_identity(module):
    if not isinstance(module, dict):
        raise InventoryError("invalid Go module object")
    path, version = module.get("Path"), module.get("Version")
    if (not isinstance(path, str) or not path or path.startswith("/")
            or any(part in ("", ".", "..") for part in path.split("/"))
            or not re.fullmatch(r"[A-Za-z0-9~._/-]+", path)
            or not isinstance(version, str)
            or not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*", version)):
        raise InventoryError(f"module must have a cache-resolvable path and version: {path!r}@{version!r}; local replacements are not supported")
    return path, version


def legal_files(cache, path, version):
    folder = cache / (escape_module(path) + "@" + escape_module(version))
    if not folder.is_dir() or folder.is_symlink() or not folder.resolve().is_relative_to(cache.resolve()):
        raise InventoryError(f"module missing from GOMODCACHE or outside it: {path}@{version}")
    texts = []
    # Walk nested legal files too (e.g. SQLite's bundled component notices).
    # Never follow links out of the cache, even if the cache was modified locally.
    def walk_error(error):
        raise error
    for directory, dirs, files in os.walk(folder, followlinks=False, onerror=walk_error):
        for name in dirs:
            if (Path(directory) / name).is_symlink():
                raise InventoryError(f"symlink directory in module: {path}@{version}/{name}")
        for name in files:
            if not LEGAL_NAME.fullmatch(name) or Path(name).suffix.lower() == ".go":
                continue
            source = Path(directory) / name
            relative = source.relative_to(folder).as_posix()
            if source.is_symlink() or not source.is_file():
                raise InventoryError(f"unsafe legal file: {path}@{version}/{relative}")
            text = read_text(source)
            if text.strip():
                texts.append((relative, text))
    if not texts:
        raise InventoryError(f"no non-empty LICENSE/LICENCE/COPYING/NOTICE/COPYRIGHT/UNLICENSE text in GOMODCACHE for {path}@{version}")
    return sorted(texts)


def properties(values):
    return [{"name": name, "value": str(value)} for name, value in sorted(values.items())]


def load_toolchain(binaries):
    # Never borrow a license from a different installed Go version. GOROOT and
    # GOVERSION must come from the same go command used to inspect the payloads.
    try:
        info = json.loads(run_go("env", "-json", "GOROOT", "GOVERSION"))
    except json.JSONDecodeError as error:
        raise InventoryError(f"invalid Go toolchain environment metadata: {error}") from error
    if not isinstance(info, dict):
        raise InventoryError("invalid Go toolchain environment metadata")
    version, root = info.get("GOVERSION"), info.get("GOROOT")
    if (not isinstance(version, str)
            or not re.fullmatch(r"go[0-9]+\.[0-9]+(?:\.[0-9]+|(?:beta|rc)[0-9]+)?", version)
            or not isinstance(root, str) or not Path(root).is_absolute()):
        raise InventoryError("invalid Go toolchain GOVERSION or GOROOT")
    goroot = Path(root)

    def required_text(name):
        source = goroot / name
        if source.is_symlink() or not source.is_file():
            raise InventoryError(f"missing or unsafe Go toolchain {name}")
        try:
            text = read_text(source)
        except (OSError, UnicodeError) as error:
            raise InventoryError(f"cannot read Go toolchain {name}: {error}") from error
        if not text.strip():
            raise InventoryError(f"empty Go toolchain {name}")
        return text

    root_version = required_text("VERSION").splitlines()[0]
    if root_version != version:
        raise InventoryError(f"Go toolchain version mismatch: GOVERSION {version!r}, GOROOT VERSION {root_version!r}")
    for binary in binaries:
        if binary["go_version"] != version:
            raise InventoryError(f"Go toolchain version mismatch: {binary['name']} uses {binary['go_version']!r}, "
                                 f"local Go toolchain is {version!r}")
    text = required_text("LICENSE")
    return {"type": "library", "bom-ref": f"urn:telegram-mcp:go-toolchain:{quote(version, safe='')}",
            "name": "Go standard library", "version": version,
            "licenses": [{"license": {"name": "Go LICENSE",
                                       "text": {"contentType": "text/plain", "content": text}}}]}


def purl(path, version):
    return f"pkg:golang/{quote(path, safe='/')}@{quote(version, safe='')}"


def module_component(path, version, texts, origins, sums):
    ref = purl(path, version)
    values = {"telegram-mcp:go:original-modules": json.dumps(sorted(origins), separators=(",", ":"))}
    if sums:
        # h1 is Go's module-directory checksum, NOT a SHA-256 of a file.
        values["telegram-mcp:go:module-sums"] = json.dumps(sorted(sums), separators=(",", ":"))
    return {"type": "library", "bom-ref": ref, "name": path, "version": version,
            "purl": ref, "properties": properties(values),
            "licenses": [{"license": {"name": f"Upstream legal text: {name}",
                                       "text": {"contentType": "text/plain", "content": text}}}
                         for name, text in texts]}


def load_binary(binary, cache):
    if not binary.is_file():
        raise InventoryError(f"binary is not a regular file: {binary}")
    before = digest(binary)
    try:
        info = json.loads(run_go("version", "-m", "-json", str(binary.resolve())))
    except json.JSONDecodeError as error:
        raise InventoryError(f"invalid or absent Go build metadata for {binary.name}: {error}") from error
    if (not isinstance(info, dict) or not isinstance(info.get("GoVersion"), str)
            or not info["GoVersion"] or not isinstance(info.get("Path"), str) or not info["Path"]):
        raise InventoryError(f"incomplete Go build metadata for {binary.name}")
    deps = info.get("Deps", [])
    if not isinstance(deps, list):
        raise InventoryError(f"invalid Deps in Go metadata for {binary.name}")
    records = {}
    for dep in deps:
        original = module_identity(dep)
        actual = dep.get("Replace") if dep.get("Replace") is not None else dep
        path, version = module_identity(actual)
        if actual.get("Replace"):
            raise InventoryError("nested Go module replacements are unsupported")
        key = (path, version)
        record = records.setdefault(key, {"origins": set(), "sums": set()})
        record["origins"].add("@".join(original))
        checksum = actual.get("Sum")
        if checksum:
            if not isinstance(checksum, str):
                raise InventoryError(f"invalid module checksum for {path}@{version}")
            record["sums"].add(checksum)
        if len(record["sums"]) > 1:
            raise InventoryError(f"conflicting checksums for {path}@{version}")
    modules = []
    for (path, version), record in sorted(records.items()):
        texts = legal_files(cache, path, version)
        modules.append({"key": (path, version), "texts": texts, **record})
    if digest(binary) != before:
        raise InventoryError(f"binary changed during inventory: {binary.name}")
    main = info.get("Main", {})
    settings = info.get("Settings", [])
    if not isinstance(main, dict) or not isinstance(settings, list):
        raise InventoryError(f"invalid Go build metadata for {binary.name}")
    values = {"telegram-mcp:go:version": info["GoVersion"], "telegram-mcp:go:package": info["Path"]}
    for key in ("Path", "Version"):
        if main.get(key):
            if not isinstance(main[key], str):
                raise InventoryError(f"invalid main module {key} for {binary.name}")
            values["telegram-mcp:go:main-" + key.lower()] = main[key]
    # Avoid exporting build-host paths or ldflags (which may contain secrets).
    for setting in settings:
        if not isinstance(setting, dict):
            raise InventoryError(f"invalid Go build setting for {binary.name}")
        if setting.get("Key") in ("GOOS", "GOARCH", "GOAMD64", "GOARM", "CGO_ENABLED", "vcs.revision", "vcs.modified"):
            if not isinstance(setting.get("Value"), str):
                raise InventoryError(f"invalid Go build setting value for {binary.name}")
            values["telegram-mcp:go:setting:" + setting["Key"]] = setting["Value"]
    root = file_component(binary.name, before, values)
    return {"name": binary.name, "root": root, "modules": modules, "go_version": info["GoVersion"]}


def file_component(name, sha256, values=None, kind="application"):
    return {"type": kind, "bom-ref": f"urn:telegram-mcp:file:{quote(name, safe='')}:{sha256}",
            "name": name, "hashes": [{"alg": "SHA-256", "content": sha256}],
            "properties": properties(values or {})}


def merge_modules(binaries):
    merged = {}
    for binary in binaries:
        for module in binary["modules"]:
            key = module["key"]
            if key not in merged:
                merged[key] = {**module, "origins": set(module["origins"]), "sums": set(module["sums"])}
            else:
                if merged[key]["texts"] != module["texts"]:
                    raise InventoryError(f"module legal files changed during inventory: {'@'.join(key)}")
                merged[key]["origins"].update(module["origins"])
                merged[key]["sums"].update(module["sums"])
                if len(merged[key]["sums"]) > 1:
                    raise InventoryError(f"conflicting checksums for {'@'.join(key)}")
    return [merged[key] for key in sorted(merged)]


def library_components(modules):
    return [module_component(module["key"][0], module["key"][1], module["texts"],
                             module["origins"], module["sums"]) for module in modules]


def notices(preamble, project_license, binaries, toolchain):
    modules = merge_modules(binaries)
    chunks = [preamble.rstrip(), "\n\n# Application license\n\n", project_license.rstrip(),
              "\n\n# Generated linked Go module inventory\n\n", SCOPE, "\n\nBinaries:\n"]
    for binary in binaries:
        chunks.append(f"- {binary['name']} (SHA-256: {binary['root']['hashes'][0]['content']})\n")
    text = toolchain["licenses"][0]["license"]["text"]["content"]
    chunks.extend([f"\n## Go standard library ({toolchain['version']})\n\n--- Go LICENSE ---\n",
                   text, "" if text.endswith("\n") else "\n"])
    for module in modules:
        path, version = module["key"]
        chunks.append(f"\n## {path}@{version}\n\n")
        linked = [binary["name"] for binary in binaries
                  if any(other["key"] == module["key"] for other in binary["modules"])]
        chunks.append("Linked by: " + ", ".join(linked) + "\n")
        originals = sorted(module["origins"] - {f"{path}@{version}"})
        if originals:
            chunks.append("Replaces: " + ", ".join(originals) + "\n")
        for filename, text in module["texts"]:
            chunks.extend([f"\n--- {filename} ---\n", text, "" if text.endswith("\n") else "\n"])
    return "".join(chunks).encode("utf-8")


def bom(root, modules, toolchain, payloads=(), additional=(), native=None):
    components = library_components(modules) + [toolchain]
    scope = {"telegram-mcp:inventory:scope": SCOPE}
    if payloads:
        components += [binary["root"] for binary in payloads]
        components += list(additional)
        direct = [binary["root"]["bom-ref"] for binary in payloads]
        direct += [component["bom-ref"] for component in additional]
        native_edges = {}
        if native is not None:
            native_components, native_edges, native_direct = native
            components += native_components
            direct += native_direct
            scope["telegram-mcp:inventory:native-scope"] = native_module().SCOPE
        dependencies = [{"ref": root["bom-ref"], "dependsOn": sorted(direct)}]
        dependencies += [{"ref": binary["root"]["bom-ref"],
                          "dependsOn": sorted([purl(*module["key"]) for module in binary["modules"]]
                                              + [toolchain["bom-ref"]])}
                         for binary in payloads]
        dependencies += [{"ref": ref, "dependsOn": sorted(set(targets))}
                         for ref, targets in sorted(native_edges.items()) if targets]
    else:
        dependencies = [{"ref": root["bom-ref"], "dependsOn": sorted(c["bom-ref"] for c in components)}]
    # The build info lists a flat set, not module-to-module dependency edges.
    result = {"$schema": "http://cyclonedx.org/schema/bom-1.6.schema.json", "bomFormat": "CycloneDX",
              "specVersion": "1.6", "version": 1,
              "metadata": {"component": root, "properties": properties(scope)},
              "components": sorted(components, key=lambda c: c["bom-ref"]), "dependencies": dependencies}
    return (json.dumps(result, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")


def generate(args):
    cache = Path(args.gomodcache or run_go("env", "GOMODCACHE").strip())
    if not cache.is_absolute() or not cache.is_dir():
        raise InventoryError("GOMODCACHE must be an existing absolute directory")
    preamble, project_license = read_text(args.preamble), read_text(args.project_license)
    if not preamble.strip() or not project_license.strip():
        raise InventoryError("curated preamble and project license must not be empty")
    inputs = [Path(path) for path in args.binary + args.artifact + args.component]
    names = [path.name for path in inputs]
    if len(names) != len(set(names)):
        raise InventoryError("binary, artifact and component basenames must be unique")
    binaries = [load_binary(path, cache) for path in sorted(map(Path, args.binary), key=lambda p: p.name)]
    toolchain = load_toolchain(binaries)
    modules = merge_modules(binaries)
    additional = []
    for component in sorted(map(Path, args.component), key=lambda p: p.name):
        if not component.is_file():
            raise InventoryError(f"component is not a regular file: {component}")
        additional.append(file_component(component.name, digest(component), {
            "telegram-mcp:inventory:component-scope": "Explicitly supplied packaged component"
        }, kind="file"))
    inventory_notice = notices(preamble, project_license, binaries, toolchain)
    native = load_native(args, binaries, inventory_notice) if args.native_root else None
    if args.native_embed:
        write_native_embed(args, native)
        return
    outputs = {} if args.artifact_only else {"THIRD-PARTY-NOTICES.txt": inventory_notice}
    if not args.artifact_only:
        for binary in binaries:
            outputs[binary["name"] + ".THIRD-PARTY-NOTICES.txt"] = notices(preamble, project_license, [binary], toolchain)
            outputs[binary["name"] + ".cdx.json"] = bom(binary["root"], binary["modules"], toolchain)
    for artifact in sorted(map(Path, args.artifact), key=lambda p: p.name):
        if not artifact.is_file():
            raise InventoryError(f"artifact is not a regular file: {artifact}")
        root = file_component(artifact.name, digest(artifact), {"telegram-mcp:inventory:artifact-scope": "Inventory generated from payloads extracted from the final artifact plus explicitly supplied packaged components."})
        native_parts, artifact_notice = None, inventory_notice
        if native is not None:
            native_inventory = native_module()
            runtime = None
            if args.native_profile == "linux-appimage":
                runtime = native_inventory.appimage_runtime(artifact, args.native_arch, native["pins"])
            go_refs = {path: binary["root"]["bom-ref"] for path, binary in native["go"].items()}
            native_parts = native_inventory.sbom_parts(native["model"], go_refs, runtime)
            artifact_notice = inventory_notice + NATIVE_MARKER.encode() + native_inventory.notices_bytes(native["model"])
        outputs[artifact.name + ".cdx.json"] = bom(root, modules, toolchain, binaries, additional, native_parts)
        outputs[artifact.name + ".THIRD-PARTY-NOTICES.txt"] = artifact_notice
    directory = Path(args.output_dir)
    targets = [directory / name for name in outputs]
    input_paths = {path.resolve() for path in inputs + [Path(args.preamble), Path(args.project_license)]}
    if any(target.resolve() in input_paths or target.is_symlink() for target in targets):
        raise InventoryError("output would overwrite an input or a symlink")
    if args.check:
        stale = [name for name, content in sorted(outputs.items())
                 if not (directory / name).is_file() or (directory / name).read_bytes() != content]
        if stale:
            raise InventoryError("missing or stale generated inventory: " + ", ".join(stale))
        return
    directory.mkdir(parents=True, exist_ok=True)
    # Compute and validate every output before touching an existing inventory.
    for name, content in sorted(outputs.items()):
        (directory / name).write_bytes(content)


def native_module():
    """Load the stdlib-only sibling module on demand (macOS/Windows flows never need it)."""
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    try:
        import native_inventory
    finally:
        sys.path.pop(0)
    return native_inventory


def load_native(args, binaries, inventory_notice):
    """Classify the extracted payload; Go payload paths must be the supplied binaries."""
    native_inventory = native_module()
    profile = native_inventory.PROFILES.get(args.native_profile)
    if profile is None or args.native_arch not in native_inventory.TRIPLETS:
        raise InventoryError("--native-root requires a valid --native-profile and --native-arch")
    root = Path(args.native_root)
    if args.native_embed and (args.artifact or args.check or not profile["embedded"]):
        raise InventoryError("--native-embed writes the embedded copy only (no --artifact/--check, embedded profile)")
    if not args.native_embed and len(args.artifact) != 1:
        raise InventoryError("--native-root inventories exactly one --artifact")
    by_name = {binary["name"]: binary for binary in binaries}
    go, go_hashes = {}, {}
    for path in sorted(profile["go"]):
        name = PurePath(path).name
        binary = by_name.get(name)
        actual = root / path
        if binary is None or actual.is_symlink() or not actual.is_file():
            raise InventoryError(f"Go payload {path} must be supplied with --binary from the native root")
        sha = digest(actual)
        if sha != binary["root"]["hashes"][0]["content"]:
            raise InventoryError(f"--binary {name} is not the payload file {path}")
        go[path], go_hashes[path] = binary, sha
    if set(by_name) != {PurePath(p).name for p in profile["go"]}:
        raise InventoryError(f"{args.native_profile} requires exactly the Go payloads {sorted(profile['go'])}")
    data_dir = Path(args.native_data_dir) if args.native_data_dir else native_inventory.DATA
    model = native_inventory.classify(root, args.native_profile, args.native_arch, go_hashes,
                                      host_root=args.native_host_root, data_dir=data_dir,
                                      embedded=not args.native_embed, patchelf=args.native_patchelf)
    # Generated Go inventory files inside the payload must be the exact Go notices.
    for record, entry in model["files"]:
        if record["class"] == "generated-inventory" and Path(entry["absolute"]).read_bytes() != inventory_notice:
            raise InventoryError(f"packaged inventory {record['path']} is not the Go notices of its payloads")
    if not args.native_embed and profile["embedded"]:
        native_inventory.verify_embedded(root, model)
    pins, _ = native_inventory.load_pins(data_dir)
    return {"model": model, "go": go, "pins": pins}


def write_native_embed(args, native):
    native_inventory = native_module()
    directory = Path(args.native_root) / native_inventory.EMBED_DIR
    for parent in [directory, *directory.parents]:
        if parent == Path(args.native_root):
            break
        if parent.is_symlink():
            raise InventoryError(f"refusing to write through a symlink: {parent}")
    directory.mkdir(parents=True, exist_ok=True)
    for name, content in sorted(native_inventory.embedded_outputs(native["model"]).items()):
        target = directory / name
        if target.is_symlink():
            raise InventoryError(f"refusing to overwrite a symlink: {target}")
        target.write_bytes(content)
        target.chmod(0o644)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", action="append", required=True, help="built Go executable (repeatable)")
    parser.add_argument("--artifact", action="append", default=[], help="final distribution artifact containing the extracted supplied binaries (repeatable)")
    parser.add_argument("--artifact-only", action="store_true", help="emit only per-artifact SBOMs and notices; requires --artifact")
    parser.add_argument("--component", action="append", default=[], help="non-Go file extracted from the artifact, such as WebView2 (repeatable)")
    parser.add_argument("--output-dir", help="destination for notices and .cdx.json files (required unless --native-embed)")
    parser.add_argument("--gomodcache", help="module cache (default: go env GOMODCACHE)")
    parser.add_argument("--preamble", type=Path, default=ROOT / "THIRD_PARTY_NOTICES.md")
    parser.add_argument("--project-license", type=Path, default=ROOT / "LICENSE")
    parser.add_argument("--check", action="store_true", help="fail on missing or changed output without writing")
    parser.add_argument("--native-root", help="extracted final Linux payload (AppImage squashfs-root or DEB data)")
    parser.add_argument("--native-profile", choices=["linux-appimage", "linux-deb"])
    parser.add_argument("--native-arch", choices=["amd64", "arm64"])
    parser.add_argument("--native-embed", action="store_true",
                        help="write the native manifest/notices into --native-root (pre-pack AppDir) and exit")
    parser.add_argument("--native-host-root", default="/", help=argparse.SUPPRESS)
    parser.add_argument("--native-data-dir", help=argparse.SUPPRESS)
    parser.add_argument("--native-patchelf", help="pinned patchelf extracted from linuxdeploy (required for modified AppImage ELF files)")
    args = parser.parse_args()
    if args.artifact_only and not args.artifact:
        parser.error("--artifact-only requires at least one --artifact")
    if (args.native_profile or args.native_arch or args.native_embed) and not args.native_root:
        parser.error("--native-* options require --native-root")
    if not args.output_dir and not args.native_embed:
        parser.error("--output-dir is required")
    try:
        generate(args)
    except Exception as error:  # noqa: BLE001 - narrowed just below
        native_error = getattr(sys.modules.get("native_inventory"), "NativeInventoryError", None)
        known = (InventoryError, OSError, UnicodeError) + ((native_error,) if native_error else ())
        if not isinstance(error, known):
            raise
        print(f"build-inventory: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
