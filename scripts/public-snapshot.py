#!/usr/bin/env python3
"""Export a reviewed Git ref without history; never create or publish a repo."""
import argparse
from datetime import date
import fnmatch
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import unicodedata
import zlib

try:
    import yaml as YAML
except ImportError:  # The conservative fallback rejects complex runs-on values.
    YAML = None

ROOT = Path(__file__).resolve().parent.parent


def sha(data):
    return hashlib.sha256(data).hexdigest()


def run(args, **kwargs):
    env = dict(os.environ, GIT_NO_REPLACE_OBJECTS="1")
    return subprocess.run(args, check=True, capture_output=True, env=env, **kwargs)


def required_tool(env_name, command):
    value = os.environ.get(env_name) or shutil.which(command)
    if not value:
        raise ValueError(f"{command} is required")
    return str(Path(shutil.which(value) or value).resolve(strict=True))


def tool_version(binary):
    result = subprocess.run([binary, "--version"], capture_output=True, text=True)
    if result.returncode != 0:
        raise ValueError("Scanner version check failed")
    return (result.stdout + result.stderr).splitlines()[0].strip()


def normalized(value):
    return unicodedata.normalize("NFC", value).casefold()


def is_within(path, parent):
    return path == parent or parent in path.parents


def load_deny_file(repo, output):
    value = os.environ.get("PUBLIC_SNAPSHOT_DENY_FILE")
    if not value:
        raise ValueError("PUBLIC_SNAPSHOT_DENY_FILE must name a private external inventory")
    path = Path(value).resolve(strict=True)
    if is_within(path, repo) or is_within(path, output):
        raise ValueError("The private inventory must stay outside the checkout and output")
    raw = path.read_bytes()
    terms = [normalized(line.strip()) for line in raw.decode("utf-8").splitlines()
             if line.strip() and not line.lstrip().startswith("#")]
    if not terms:
        raise ValueError("The private inventory must contain at least one sensitive term")
    return sorted(set(terms)), sha(raw)


def archive(repo, commit, tree):
    index = run(["git", "-C", str(repo), "ls-tree", "-rz", "--full-tree", commit]).stdout
    expected = {}
    for record in index.split(b"\0"):
        if not record:
            continue
        metadata, name = record.split(b"\t", 1)
        mode, kind, oid = metadata.split()
        if kind != b"blob" or mode not in (b"100644", b"100755"):
            raise ValueError("Links, submodules and special members are not accepted")
        expected[name.decode("utf-8")] = (oid, mode == b"100755")
    ids = [value[0] for value in expected.values()]
    blobs = run(["git", "-C", str(repo), "cat-file", "--batch"], input=b"\n".join(ids) + b"\n").stdout
    cursor, original = 0, {}
    for name, (oid, executable) in expected.items():
        end = blobs.index(b"\n", cursor)
        actual_oid, kind, size = blobs[cursor:end].split()
        if actual_oid != oid or kind != b"blob":
            raise ValueError("Unexpected Git object response")
        start = end + 1
        contents = blobs[start:start + int(size)]
        cursor = start + int(size) + 1
        original[name] = (sha(contents), executable)
    data = run(["git", "-C", str(repo), "archive", "--format=tar", commit]).stdout
    with tarfile.open(fileobj=io.BytesIO(data)) as tar:
        for member in tar:
            parts = PurePosixPath(member.name).parts
            if (not parts or member.name.startswith("/") or ".." in parts or
                    ".git" in parts or "\\" in member.name):
                raise ValueError("Unsafe archive member")
            path = tree.joinpath(*parts)
            if member.isdir():
                path.mkdir(parents=True, exist_ok=True)
            elif member.isfile():
                path.parent.mkdir(parents=True, exist_ok=True)
                source = tar.extractfile(member)
                if source is None:
                    raise ValueError("Archive file contents are unavailable")
                with source:
                    path.write_bytes(source.read())
                path.chmod(0o755 if member.mode & 0o111 else 0o644)
            else:
                raise ValueError("Links, submodules and special archive members are not accepted")
    actual = {item["path"]: (item["sha256"], item["executable"]) for item in file_entries(tree)}
    if actual != original:
        raise ValueError("Archive differs from tracked blobs or modes; review export attributes")


def file_entries(tree):
    return [{"path": p.relative_to(tree).as_posix(), "sha256": sha(p.read_bytes()),
             "size": p.stat().st_size, "executable": bool(p.stat().st_mode & 0o111)}
            for p in sorted(tree.rglob("*")) if p.is_file()]


def apply_exclusions(tree, entries):
    policy = tree / "release/public-exclude.txt"
    patterns = [line.strip() for line in policy.read_text().splitlines()
                if line.strip() and not line.lstrip().startswith("#")]
    excluded = []
    for item in entries:
        name = item["path"]
        matches = [p for p in patterns if
                   (name.startswith(p) if p.endswith("/") else fnmatch.fnmatchcase(name, p))]
        if matches:
            excluded.append(dict(item, rules=matches))
            (tree / name).unlink()
    for directory in sorted(tree.rglob("*"), reverse=True):
        if directory.is_dir() and not any(directory.iterdir()):
            directory.rmdir()
    return excluded, sha(policy.read_bytes())


def apply_overlays(tree):
    path = tree / "release/public-overlays.json"
    changes = []
    if not path.exists():
        return changes
    for rule in json.loads(path.read_text()):
        target = tree / rule["path"]
        if not is_within(target.resolve(), tree.resolve()) or not target.is_file():
            raise ValueError("Overlay target is missing or outside the tree")
        before = target.read_bytes()
        after, count = re.subn(rule["pattern"], rule["replacement"], before.decode("utf-8"))
        if count != rule["count"]:
            raise ValueError("Overlay match count changed; review the policy")
        target.write_text(after)
        changes.append({"path": rule["path"], "rule": rule["name"],
                        "before_sha256": sha(before), "after_sha256": sha(after.encode())})
    return changes


def personal_failures(relative, texts, terms, policy):
    patterns = [(name, re.compile(pattern, re.I)) for name, pattern in policy["patterns"].items()]
    email = re.compile(r"[a-zA-Z0-9._%+\-]+@(?![0-9]+x\.png\b)[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}")
    failures, seen = [], set()
    for text in texts:
        for number, line in enumerate(text.splitlines(), 1):
            categories = [name for name, pattern in patterns if pattern.search(line)]
            folded = normalized(line)
            if any((re.search(r"(?<!\d)" + re.escape(term) + r"(?!\d)", folded)
                    if term.isdecimal() else term in folded) for term in terms):
                categories.append("private-inventory")
            for address in email.findall(line):
                lower = address.casefold()
                if not (lower.endswith(".invalid") or lower in policy.get("public_emails", [])):
                    categories.append("unreviewed-email")
            approved = policy.get("approved_synthetic_lines", {}).get(relative, {}).get(sha(line.encode()), [])
            categories = [category for category in categories
                          if category == "private-inventory" or category not in approved]
            for category in categories:
                key = (relative, number, category)
                if key not in seen:
                    seen.add(key)
                    failures.append({"path": relative, "line": number, "category": category})
    return failures


def scan_personal(tree, terms, policy):
    failures = []
    for path in sorted(tree.rglob("*")):
        if not path.is_file():
            continue
        relative = path.relative_to(tree).as_posix()
        data = path.read_bytes()
        texts = [relative, data.decode("utf-8", errors="replace")]
        texts += [data.decode(enc, errors="replace") for enc in ("utf-16-le", "utf-16-be")]
        failures.extend(personal_failures(relative, texts, terms, policy))
    return failures


IMAGE_SIGNATURES = (
    ("png", lambda d: d.startswith(b"\x89PNG\r\n\x1a\n")),
    ("jpeg", lambda d: d.startswith(b"\xff\xd8\xff")),
    ("gif", lambda d: d.startswith((b"GIF87a", b"GIF89a"))),
    ("webp", lambda d: len(d) >= 12 and d[:4] == b"RIFF" and d[8:12] == b"WEBP"),
    ("bmp", lambda d: d.startswith(b"BM")),
    ("tiff", lambda d: d.startswith((b"II*\x00", b"MM\x00*"))),
    ("netpbm", lambda d: bool(re.match(br"P[1-6][\t\n\r ]", d[:3]))),
)


def image_kind(data):
    return next((name for name, match in IMAGE_SIGNATURES if match(data)), None)


PNG_METADATA_CHUNKS = {b"tEXt", b"zTXt", b"iTXt", b"eXIf"}
JPEG_XMP_PREFIXES = (b"http://ns.adobe.com/xap/1.0/\x00",
                     b"http://ns.adobe.com/xmp/extension/\x00")


def strip_png_metadata(data):
    output = bytearray(data[:8])
    cursor, first, removed = 8, True, []
    while cursor < len(data):
        if len(data) - cursor < 12:
            raise ValueError("Malformed PNG container")
        length = struct.unpack(">I", data[cursor:cursor + 4])[0]
        end = cursor + 12 + length
        if end > len(data):
            raise ValueError("Malformed PNG container")
        kind = data[cursor + 4:cursor + 8]
        payload = data[cursor + 8:cursor + 8 + length]
        expected_crc = struct.unpack(">I", data[cursor + 8 + length:end])[0]
        if (not re.fullmatch(br"[A-Za-z]{4}", kind) or
                zlib.crc32(kind + payload) & 0xffffffff != expected_crc):
            raise ValueError("Malformed PNG container")
        if first and kind != b"IHDR":
            raise ValueError("Malformed PNG container")
        first = False
        if kind in PNG_METADATA_CHUNKS:
            removed.append(kind.decode("ascii"))
        else:
            output.extend(data[cursor:end])
        cursor = end
        if kind == b"IEND":
            if length or cursor != len(data):
                raise ValueError("Malformed PNG container")
            return bytes(output), removed
    raise ValueError("Malformed PNG container")


def strip_jpeg_metadata(data):
    if not data.startswith(b"\xff\xd8"):
        raise ValueError("Malformed JPEG container")
    output = bytearray(data[:2])
    cursor, in_scan, removed = 2, False, []
    while cursor < len(data):
        if in_scan:
            start = cursor
            while cursor < len(data):
                marker = data.find(b"\xff", cursor)
                if marker < 0 or marker + 1 >= len(data):
                    raise ValueError("Malformed JPEG container")
                code_at = marker + 1
                while code_at < len(data) and data[code_at] == 0xff:
                    code_at += 1
                if code_at >= len(data):
                    raise ValueError("Malformed JPEG container")
                code = data[code_at]
                if code == 0x00 or 0xd0 <= code <= 0xd7:
                    cursor = code_at + 1
                    continue
                output.extend(data[start:marker])
                cursor = marker
                in_scan = False
                break
            continue
        if data[cursor] != 0xff:
            raise ValueError("Malformed JPEG container")
        marker_at = cursor + 1
        while marker_at < len(data) and data[marker_at] == 0xff:
            marker_at += 1
        if marker_at >= len(data):
            raise ValueError("Malformed JPEG container")
        marker = data[marker_at]
        marker_end = marker_at + 1
        if marker == 0xd9:
            output.extend(data[cursor:marker_end])
            if marker_end != len(data):
                raise ValueError("Malformed JPEG container")
            return bytes(output), removed
        if marker in (0xd8, 0x00):
            raise ValueError("Malformed JPEG container")
        if marker == 0x01 or 0xd0 <= marker <= 0xd7:
            output.extend(data[cursor:marker_end])
            cursor = marker_end
            continue
        if marker_end + 2 > len(data):
            raise ValueError("Malformed JPEG container")
        length = struct.unpack(">H", data[marker_end:marker_end + 2])[0]
        end = marker_end + length
        if length < 2 or end > len(data):
            raise ValueError("Malformed JPEG container")
        payload = data[marker_end + 2:end]
        remove = marker == 0xfe or (marker == 0xe1 and
                                    (payload.startswith(b"Exif\x00\x00") or
                                     payload.startswith(JPEG_XMP_PREFIXES)))
        if remove:
            removed.append("COM" if marker == 0xfe else
                           ("EXIF" if payload.startswith(b"Exif\x00\x00") else "XMP"))
        else:
            output.extend(data[cursor:end])
        cursor = end
        in_scan = marker == 0xda
    raise ValueError("Malformed JPEG container")


def webp_chunk(kind, payload):
    return kind + struct.pack("<I", len(payload)) + payload + (b"\x00" if len(payload) % 2 else b"")


def strip_webp_metadata(data):
    if (len(data) < 12 or data[:4] != b"RIFF" or data[8:12] != b"WEBP" or
            struct.unpack("<I", data[4:8])[0] != len(data) - 8):
        raise ValueError("Malformed WebP container")
    cursor, chunks, removed = 12, [], []
    while cursor < len(data):
        if len(data) - cursor < 8:
            raise ValueError("Malformed WebP container")
        kind = data[cursor:cursor + 4]
        length = struct.unpack("<I", data[cursor + 4:cursor + 8])[0]
        end = cursor + 8 + length
        padded_end = end + (length % 2)
        if padded_end > len(data):
            raise ValueError("Malformed WebP container")
        payload = data[cursor + 8:end]
        if kind in (b"EXIF", b"XMP "):
            removed.append(kind.decode("ascii").strip())
        else:
            if kind == b"VP8X":
                if len(payload) != 10:
                    raise ValueError("Malformed WebP container")
                payload = bytes([payload[0] & ~0x0c]) + payload[1:]
            chunks.append(webp_chunk(kind, payload))
        cursor = padded_end
    body = b"WEBP" + b"".join(chunks)
    return b"RIFF" + struct.pack("<I", len(body)) + body, removed


def sanitize_image_metadata(tree):
    sanitizers = {"png": strip_png_metadata, "jpeg": strip_jpeg_metadata,
                  "webp": strip_webp_metadata}
    changes = []
    for path in sorted(tree.rglob("*")):
        if not path.is_file():
            continue
        before = path.read_bytes()
        kind = image_kind(before)
        sanitizer = sanitizers.get(kind or "")
        if sanitizer is None:
            continue
        after, removed = sanitizer(before)
        if after != before:
            path.write_bytes(after)
            changes.append({"path": path.relative_to(tree).as_posix(),
                            "removed": removed, "before_sha256": sha(before),
                            "after_sha256": sha(after)})
    return changes


def load_reviewed(path, fields):
    entries = {}
    for number, raw in enumerate(path.read_text().splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        parts = raw.split("\t")
        if len(parts) != fields or not re.fullmatch(r"[0-9a-f]{64}", parts[1]) or any(not value.strip() for value in parts[2:]):
            raise ValueError(f"Invalid reviewed policy entry at line {number}")
        if fields == 4:
            try:
                reviewed_on = date.fromisoformat(parts[2])
            except ValueError as error:
                raise ValueError(f"Invalid review date at line {number}") from error
            if reviewed_on > date.today():
                raise ValueError(f"Future review date at line {number}")
        entries[(parts[0], parts[1])] = parts[2:]
    return entries


def is_text(data):
    try:
        text = data.decode("utf-8-sig")
    except UnicodeDecodeError:
        return False
    return all(character in "\t\n\r\f\b" or ord(character) >= 32 for character in text)


def scan_formats(tree):
    allowed = load_reviewed(tree / "release/reviewed-opaque.txt", 3)
    failures, opaque = [], 0
    for path in sorted(tree.rglob("*")):
        if not path.is_file():
            continue
        relative, data = path.relative_to(tree).as_posix(), path.read_bytes()
        if image_kind(data) or is_text(data):
            continue
        opaque += 1
        if (relative, sha(data)) not in allowed:
            failures.append({"path": relative, "category": "unreviewed-opaque-format"})
    return {"status": "failed" if failures else "passed", "scanned": opaque,
            "allowlisted": opaque - len(failures), "failures": failures}


QR_MATRIX = """000000000000000000000000000000000
000000000000000000000000000000000
000000000000000000000000000000000
000000000000000000000000000000000
000011111110011100001011111110000
000010000010111001010010000010000
000010111010010110000010111010000
000010111010110001101010111010000
000010111010000100001010111010000
000010000010101000011010000010000
000011111110101010101011111110000
000000000000000000111000000000000
000011111011111011100101010100000
000000110100110100100001010000000
000010111010010011010010111110000
000001010001011000011111100110000
000001011011110001011110111110000
000011010001111101001101001100000
000010011010110000111011110110000
000010100100001000101010000010000
000010111111010111101111111010000
000000000000100110111000111100000
000011111110111011001010100110000
000010000010000100101000100000000
000010111010111011111111111100000
000010111010111100010111110110000
000010111010110010111101001010000
000010000010101000111011010010000
000011111110100111100101111110000
000000000000000000000000000000000
000000000000000000000000000000000
000000000000000000000000000000000
000000000000000000000000000000000"""


def write_zbar_self_test(target):
    rows, scale = QR_MATRIX.splitlines(), 6
    pixels = bytearray()
    for row in rows:
        expanded = [bit == "1" for bit in row for _ in range(scale)]
        packed = bytes(sum((1 << (7 - (i % 8))) for i, bit in enumerate(expanded[j:j + 8], j) if bit)
                       for j in range(0, len(expanded), 8))
        for _ in range(scale):
            pixels.extend(packed)
    target.write_bytes(b"P4\n# PUBLIC_SNAPSHOT_ZBAR_SELF_TEST\n198 198\n" + pixels)


def verify_zbar(binary):
    with tempfile.TemporaryDirectory(prefix="public-snapshot-zbar-") as directory:
        target = Path(directory) / "selftest.pbm"
        write_zbar_self_test(target)
        result = subprocess.run([binary, "--quiet", "--raw", str(target)], capture_output=True)
        if result.returncode != 0 or result.stdout.strip() != b"public-snapshot-zbar-self-test":
            raise ValueError("zbarimg self-test failed")


def scan_qr(tree, binary):
    scanned, failures = 0, []
    for path in sorted(tree.rglob("*")):
        if not path.is_file() or not image_kind(path.read_bytes()):
            continue
        scanned += 1
        result = subprocess.run([binary, "--quiet", "--raw", str(path)], capture_output=True)
        relative = path.relative_to(tree).as_posix()
        if result.returncode == 0 and result.stdout.strip():
            failures.append({"path": relative, "category": "decodable-barcode"})
        elif result.returncode not in (0, 4):
            failures.append({"path": relative, "category": "barcode-scan-error"})
    return {"status": "failed" if failures else "passed", "scanned": scanned, "failures": failures}


def scan_ocr(tree, binary, terms, policy):
    scanned, failures, errors = 0, [], []
    for path in sorted(tree.rglob("*")):
        if not path.is_file() or not image_kind(path.read_bytes()):
            continue
        scanned += 1
        result = subprocess.run([binary, str(path), "stdout"], capture_output=True, text=True)
        relative = path.relative_to(tree).as_posix()
        if result.returncode != 0:
            errors.append({"path": relative, "category": "ocr-scan-error"})
        else:
            failures.extend(personal_failures(relative, [result.stdout], terms, policy))
    failures.extend(errors)
    return {"status": "failed" if failures else "passed", "scanned": scanned, "failures": failures}


def scan_image_reviews(tree):
    reviewed = load_reviewed(tree / "release/reviewed-images.txt", 4)
    images = []
    for path in sorted(tree.rglob("*")):
        if not path.is_file():
            continue
        data = path.read_bytes()
        if image_kind(data) or data.startswith((b"icns", b"\x00\x00\x01\x00")):
            images.append((path.relative_to(tree).as_posix(), sha(data)))
    failures = [{"path": path, "category": "image-not-reviewed"}
                for path, digest in images if (path, digest) not in reviewed]
    return {"status": "failed" if failures else "passed", "scanned": len(images), "failures": failures}


def strip_yaml_comments(text):
    output = []
    for line in text.splitlines():
        quote, escaped, kept = None, False, []
        for character in line:
            if escaped:
                kept.append(character)
                escaped = False
                continue
            if character == "\\" and quote == '"':
                kept.append(character)
                escaped = True
                continue
            if character in ("'", '"'):
                if quote is None:
                    quote = character
                elif quote == character:
                    quote = None
                kept.append(character)
                continue
            if character == "#" and quote is None:
                break
            kept.append(character)
        output.append("".join(kept))
    return "\n".join(output)


FORBIDDEN_RUNNER_LABELS = ("self-hosted", "homelab-ci")


if YAML is not None:
    class UniqueSafeLoader(YAML.SafeLoader):
        pass


    def construct_unique_mapping(loader, node, deep=False):
        loader.flatten_mapping(node)
        mapping = {}
        for key_node, value_node in node.value:
            key = loader.construct_object(key_node, deep=deep)
            try:
                duplicate = key in mapping
            except TypeError as error:
                raise YAML.constructor.ConstructorError(
                    "while constructing a mapping", node.start_mark,
                    "found an unhashable key", key_node.start_mark) from error
            if duplicate:
                raise YAML.constructor.ConstructorError(
                    "while constructing a mapping", node.start_mark,
                    "found a duplicate key", key_node.start_mark)
            mapping[key] = loader.construct_object(value_node, deep=deep)
        return mapping


    UniqueSafeLoader.add_constructor(
        YAML.resolver.BaseResolver.DEFAULT_MAPPING_TAG, construct_unique_mapping)
else:
    UniqueSafeLoader = None


def runner_value_is_unsafe(value):
    if isinstance(value, str):
        folded = value.casefold()
        return (any(label in folded for label in FORBIDDEN_RUNNER_LABELS) or
                "${{" in value or "}}" in value)
    if isinstance(value, list):
        return not value or any(not isinstance(item, str) or runner_value_is_unsafe(item)
                                for item in value)
    return True


def find_runs_on_values(value, seen=None):
    if seen is None:
        seen = set()
    if isinstance(value, (dict, list)):
        identity = id(value)
        if identity in seen:
            raise ValueError("Recursive YAML aliases are not accepted")
        seen.add(identity)
    if isinstance(value, dict):
        for key, child in value.items():
            if key == "runs-on":
                yield child
            yield from find_runs_on_values(child, seen)
    elif isinstance(value, list):
        for child in value:
            yield from find_runs_on_values(child, seen)
    if isinstance(value, (dict, list)):
        seen.remove(id(value))


def fallback_workflow_is_unsafe(text):
    stripped = strip_yaml_comments(text)
    lines = stripped.splitlines()
    if "\\" in stripped:
        return True
    literal_keys = len(re.findall(
        r"(?<![A-Za-z0-9_-])runs-on(?![A-Za-z0-9_-])", stripped))
    recognized_keys = sum(bool(re.match(
        r"^\s*(?:runs-on|'runs-on'|\"runs-on\")\s*:", line)) for line in lines)
    if literal_keys != recognized_keys:
        return True
    for index, line in enumerate(lines):
        match = re.match(r"^(\s*)(?:runs-on|'runs-on'|\"runs-on\")\s*:\s*(.*)$", line)
        if not match:
            continue
        indent, inline = len(match.group(1)), match.group(2)
        values = [inline]
        if not inline.strip():
            for continuation in lines[index + 1:]:
                if not continuation.strip():
                    continue
                continuation_indent = len(continuation) - len(continuation.lstrip())
                if continuation_indent <= indent:
                    break
                values.append(continuation)
        raw = "\n".join(values)
        if (re.search(r"[\\&*!]", raw) or "${{" in raw or "}}" in raw or
                any(label in raw.casefold() for label in FORBIDDEN_RUNNER_LABELS)):
            return True
        if not re.fullmatch(r"[A-Za-z0-9_. /\[\],'\"#\n-]+", raw):
            return True
    return False


def workflow_is_unsafe(text):
    if YAML is None:
        return fallback_workflow_is_unsafe(text)
    try:
        document = YAML.load(text, Loader=UniqueSafeLoader)
        return any(runner_value_is_unsafe(value)
                   for value in find_runs_on_values(document))
    except (YAML.YAMLError, TypeError, ValueError, RecursionError):
        return True


def scan_workflows(tree):
    failures = []
    root = tree / ".github/workflows"
    paths = [path for path in sorted(root.rglob("*")) if path.is_file()] if root.exists() else []
    for path in paths:
        if workflow_is_unsafe(path.read_text(errors="replace")):
            failures.append(path.relative_to(tree).as_posix())
    return {"status": "failed" if failures else "passed", "scanned": len(paths),
            "failures": failures}


def export(args):
    repo = Path(args.repo).resolve(strict=True)
    output = Path(args.output).resolve()
    top = Path(run(["git", "-C", str(repo), "rev-parse", "--show-toplevel"], text=True).stdout.strip()).resolve()
    if repo != top:
        raise ValueError("--repo must identify the checkout root")
    if is_within(output, repo) or is_within(repo, output):
        raise ValueError("Output must not overlap the checkout")
    if output.exists():
        raise ValueError("Output must be a new directory (existing output is never overwritten)")
    terms, inventory_hash = load_deny_file(repo, output)
    scanner = os.environ.get("GITLEAKS_BIN") or shutil.which("gitleaks")
    if not scanner:
        raise ValueError("gitleaks is required; use the pinned installer")
    scanner = str(Path(shutil.which(scanner) or scanner).resolve(strict=True))
    version = run([scanner, "version"], text=True).stdout.strip()
    commit = run(["git", "-C", str(repo), "rev-parse", "--verify", "--end-of-options", args.ref + "^{commit}"], text=True).stdout.strip()
    tree_id = run(["git", "-C", str(repo), "rev-parse", commit + "^{tree}"], text=True).stdout.strip()
    output.mkdir(parents=True, exist_ok=False, mode=0o700)
    tree, reports = output / "tree", output / "reports"
    tree.mkdir()
    reports.mkdir()
    manifest = {"schema_version": 1, "status": "failed", "source_commit": commit, "source_tree": tree_id,
                "private_inventory_sha256": inventory_hash, "gitleaks_version": version,
                "gitleaks_binary_sha256": sha(Path(scanner).read_bytes())}
    try:
        archive(repo, commit, tree)
        scanner_policy = json.loads((tree / "release/public-scanners.json").read_text())
        if version != scanner_policy["gitleaks"]["version"] or sha(Path(scanner).read_bytes()) != scanner_policy["gitleaks"]["sha256"]:
            raise ValueError("Gitleaks version or binary hash differs from the selected policy")
        zbar = required_tool("PUBLIC_SNAPSHOT_ZBARIMG", "zbarimg")
        tesseract = required_tool("PUBLIC_SNAPSHOT_TESSERACT", "tesseract")
        zbar_version = tool_version(zbar)
        tesseract_version = tool_version(tesseract).removeprefix("tesseract ")
        if zbar_version != scanner_policy["zbarimg"]["version"]:
            raise ValueError("zbarimg version differs from the selected policy")
        if tesseract_version != scanner_policy["tesseract"]["version"]:
            raise ValueError("Tesseract version differs from the selected policy")
        verify_zbar(zbar)
        manifest["scanners"] = {
            "gitleaks": {"version": version, "binary_sha256": sha(Path(scanner).read_bytes())},
            "zbarimg": {"version": zbar_version, "self_test": "passed"},
            "tesseract": {"version": tesseract_version},
        }
        original = file_entries(tree)
        manifest["excluded"], manifest["exclusion_policy_sha256"] = apply_exclusions(tree, original)
        manifest["overlays"] = apply_overlays(tree)
        manifest["sanitized_images"] = sanitize_image_metadata(tree)
        policy_path = tree / "release/public-patterns.json"
        manifest["pattern_policy_sha256"] = sha(policy_path.read_bytes())
        policy = json.loads(policy_path.read_text())
        failures = scan_personal(tree, terms, policy)
        manifest["personal_data"] = {"status": "failed" if failures else "passed", "scanned": len(file_entries(tree)),
                                     "failures": failures}
        manifest["formats"] = scan_formats(tree)
        manifest["barcodes"] = scan_qr(tree, zbar)
        manifest["ocr"] = scan_ocr(tree, tesseract, terms, policy)
        manifest["image_review"] = scan_image_reviews(tree)
        manifest["workflows"] = scan_workflows(tree)
        report = reports / "gitleaks.json"
        result = subprocess.run([scanner, "dir", ".", "--no-banner", "--redact=100",
                                 "--config", ".gitleaks.toml", "--report-format", "json",
                                 "--report-path", str(report)], cwd=tree, capture_output=True)
        # Do not echo scanner output: even diagnostics can contain private values.
        try:
            findings = json.loads(report.read_text())
            report_valid = isinstance(findings, list)
        except (OSError, ValueError):
            findings, report_valid = None, False
        leaks_passed = result.returncode == 0 and report_valid and findings == []
        manifest["gitleaks"] = {"status": "passed" if leaks_passed else "failed",
                                "exit_code": result.returncode, "report_valid": report_valid}
        manifest["included"] = file_entries(tree)
        manifest["counts"] = {"included": len(manifest["included"]), "excluded": len(manifest["excluded"]),
                              "overlays": len(manifest["overlays"]),
                              "sanitized_images": len(manifest["sanitized_images"]),
                              "personal_data": manifest["personal_data"]["scanned"],
                              "opaque_formats": manifest["formats"]["scanned"],
                              "barcodes": manifest["barcodes"]["scanned"],
                              "ocr": manifest["ocr"]["scanned"],
                              "reviewed_images": manifest["image_review"]["scanned"],
                              "workflows": manifest["workflows"]["scanned"],
                              "gitleaks": 1}
        checks = ("personal_data", "formats", "barcodes", "ocr", "image_review", "workflows", "gitleaks")
        if all(manifest[name]["status"] == "passed" for name in checks):
            manifest["status"] = "passed"
    except Exception:
        manifest["error"] = "Export failed; inspect source and policy privately"
        raise
    finally:
        manifest_path = output / "manifest.json"
        manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        print(json.dumps({"status": manifest["status"], "counts": manifest.get("counts"),
                          "manifest": str(manifest_path)}, sort_keys=True))
    return 0 if manifest["status"] == "passed" else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=str(ROOT))
    parser.add_argument("--ref", default="main")
    parser.add_argument("--output", required=True, help="new private staging directory")
    args = parser.parse_args()
    try:
        return export(args)
    except (ValueError, OSError, subprocess.CalledProcessError, tarfile.TarError, KeyError) as error:
        # Intentionally do not stringify exceptions containing inventory or archive contents.
        print("Snapshot blocked (" + type(error).__name__ + "): check prerequisites, ref and policy privately", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
