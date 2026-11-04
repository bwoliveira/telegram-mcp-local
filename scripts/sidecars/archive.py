#!/usr/bin/env python3
"""Create a byte-reproducible sidecar archive from a flat payload directory."""
import argparse
import gzip
import io
import os
from pathlib import Path
import tarfile
import tempfile
import zipfile

EPOCH = 315532800  # ZIP's minimum timestamp: 1980-01-01 UTC.


def files(root):
    result = []
    for path in sorted(root.iterdir(), key=lambda item: item.name):
        if not path.is_file() or path.is_symlink() or path.name in {"", ".", ".."}:
            raise SystemExit(f"unsafe sidecar payload: {path}")
        result.append(path)
    if not result:
        raise SystemExit("empty sidecar payload")
    return result


def zip_archive(root, output):
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED,
                         compresslevel=9, strict_timestamps=True) as archive:
        for path in files(root):
            info = zipfile.ZipInfo(path.name, (1980, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = ((path.stat().st_mode & 0o777) | 0o100000) << 16
            archive.writestr(info, path.read_bytes(), compresslevel=9)


def tar_archive(root, output):
    with output.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as archive:
                for path in files(root):
                    data = path.read_bytes()
                    info = tarfile.TarInfo(path.name)
                    info.size = len(data)
                    info.mode = path.stat().st_mode & 0o777
                    info.mtime = EPOCH
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    archive.addfile(info, io.BytesIO(data))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--format", required=True, choices=("tar.gz", "zip"))
    parser.add_argument("--payload-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not args.payload_dir.is_dir():
        parser.error("payload directory must exist")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".sidecar-", dir=args.output.parent)
    os.close(fd)
    temporary = Path(temporary)
    try:
        if args.format == "zip":
            zip_archive(args.payload_dir, temporary)
        else:
            tar_archive(args.payload_dir, temporary)
        os.replace(temporary, args.output)
    finally:
        temporary.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
