#!/usr/bin/env python3
"""Pins a set of transcription archives in internal/sidecar/manifest.json.

    scripts/sidecars/manifest.py sidecars-N <dir with the five archives>

Writes the manifest with each archive's sha256 and size and its address in the
release sidecars-N of this repository, and copies it into the folder, to be
published with the archives.
"""
import hashlib
import io
import json
import os
import shutil
import sys
import tarfile
import zipfile

REPO = "bwoliveira/telegram-mcp-local"
ARCHIVES = {
    "transcription_darwin_universal.tar.gz": ("darwin/universal", "metal"),
    "transcription_linux_amd64.tar.gz": ("linux/amd64", "cpu"),
    "transcription_linux_arm64.tar.gz": ("linux/arm64", "cpu"),
    "transcription_windows_amd64.zip": ("windows/amd64", "cpu"),
    "transcription_windows_amd64_cuda.zip": ("windows/amd64/cuda", "cuda"),
}


def digest(body):
    return hashlib.sha256(body).hexdigest()


def payloads(name, body):
    extracted = {}
    if name.endswith(".zip"):
        with zipfile.ZipFile(io.BytesIO(body)) as archive:
            members = [(item.filename, archive.read(item)) for item in archive.infolist() if not item.is_dir()]
    else:
        with tarfile.open(fileobj=io.BytesIO(body), mode="r:gz") as archive:
            members = []
            for item in archive.getmembers():
                if item.isfile():
                    stream = archive.extractfile(item)
                    if stream is None:
                        raise SystemExit(f"cannot read archive member in {name}: {item.name}")
                    members.append((item.name, stream.read()))
    for member, content in members:
        normalized = member.removeprefix("./")
        if not normalized or "/" in normalized or "\\" in normalized or normalized in extracted:
            raise SystemExit(f"unsafe or duplicate member in {name}: {member}")
        extracted[normalized] = content
    return extracted


def validate_companions(name, archive_body, sbom_body, notice_body):
    try:
        sbom = json.loads(sbom_body)
        root = sbom["metadata"]["component"]
        if sbom["bomFormat"] != "CycloneDX" or sbom["specVersion"] != "1.6":
            raise ValueError("wrong CycloneDX format")
        if root["name"] != name or root["hashes"] != [{"alg": "SHA-256", "content": digest(archive_body)}]:
            raise ValueError("SBOM root does not bind the archive")
        actual = payloads(name, archive_body)
        described = {item["name"]: item["hashes"] for item in sbom["components"]}
        expected = {member: [{"alg": "SHA-256", "content": digest(content)}]
                    for member, content in actual.items()}
        if described != expected:
            raise ValueError("SBOM payload inventory is stale")
        notice = notice_body.decode("utf-8")
        for legal in ("whisper.cpp-LICENSE.txt", "FFmpeg-LGPL-2.1.txt", "FFmpeg-LGPL-3.txt"):
            text = actual[legal].decode("utf-8")
            if f"===== {legal} =====" not in notice or text not in notice:
                raise ValueError(f"notices omit {legal}")
        optional = "NVIDIA-CUDA-runtime-NOTICE.txt"
        if optional in actual and actual[optional].decode("utf-8") not in notice:
            raise ValueError(f"notices omit {optional}")
    except (KeyError, TypeError, ValueError, UnicodeError, tarfile.TarError, zipfile.BadZipFile) as error:
        raise SystemExit(f"invalid release companions for {name}: {error}") from error


tag, folder = sys.argv[1], sys.argv[2]
if tag != "sidecars-2":
    raise SystemExit("this release line requires the immutable sidecars-2 tag")
packages = {}
for name, (key, accel) in ARCHIVES.items():
    archive_path = os.path.join(folder, name)
    with open(archive_path, "rb") as f:
        body = f.read()
    metadata = {}
    companion_bodies = {}
    for suffix, field in ((".cdx.json", "sbom"), (".THIRD-PARTY-NOTICES.txt", "notices")):
        sidecar_name = name + suffix
        sidecar_path = os.path.join(folder, sidecar_name)
        if not os.path.isfile(sidecar_path):
            raise SystemExit(f"missing required release file: {sidecar_name}")
        with open(sidecar_path, "rb") as f:
            sidecar_body = f.read()
        companion_bodies[field] = sidecar_body
        metadata[field] = {
            "url": f"https://github.com/{REPO}/releases/download/{tag}/{sidecar_name}",
            "sha256": digest(sidecar_body),
            "size": len(sidecar_body),
        }
    validate_companions(name, body, companion_bodies["sbom"], companion_bodies["notices"])
    packages[key] = {
        "url": f"https://github.com/{REPO}/releases/download/{tag}/{name}",
        "sha256": hashlib.sha256(body).hexdigest(),
        "size": len(body),
        "accel": accel,
        **metadata,
    }
path = os.path.join(os.path.dirname(__file__), "..", "..", "internal", "sidecar", "manifest.json")
with open(path, "w") as f:
    json.dump({"version": tag, "packages": packages}, f, indent=2)
    f.write("\n")
shutil.copy(path, os.path.join(folder, "manifest.json"))
print(os.path.normpath(path))
