#!/usr/bin/env python3
"""Create a deterministic CycloneDX 1.6 SBOM for a sidecar archive."""
import argparse
import hashlib
import json
from pathlib import Path


def sha256(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def component(path, kind="file"):
    digest = sha256(path)
    return {
        "type": kind,
        "bom-ref": f"urn:telegram-mcp:sha256:{digest}",
        "name": path.name,
        "hashes": [{"alg": "SHA-256", "content": digest}],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifact", type=Path, required=True)
    parser.add_argument("--payload-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not args.artifact.is_file() or not args.payload_dir.is_dir():
        parser.error("artifact and payload directory must exist")
    payloads = [component(path) for path in sorted(args.payload_dir.iterdir()) if path.is_file()]
    root = component(args.artifact, "application")
    root["properties"] = [{"name": "telegram-mcp:inventory:scope", "value": "Exact packaged sidecar payload files"}]
    bom = {
        "$schema": "http://cyclonedx.org/schema/bom-1.6.schema.json",
        "bomFormat": "CycloneDX",
        "specVersion": "1.6",
        "version": 1,
        "metadata": {"component": root},
        "components": payloads,
        "dependencies": [{"ref": root["bom-ref"], "dependsOn": sorted(item["bom-ref"] for item in payloads)}],
    }
    args.output.write_text(json.dumps(bom, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
