#!/usr/bin/env python3
"""Reject duplicate Pages selectors without mutating the generated policy."""
import pathlib
import sys

path = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'site/dist/_headers')
seen = set()
for line in path.read_text().splitlines():
    if not line.strip() or line.lstrip().startswith('#') or line[0].isspace():
        continue
    selector = line.strip()
    if selector in seen:
        raise SystemExit(f'duplicate Pages _headers selector: {selector}')
    seen.add(selector)
print(f'Pages _headers selectors unique: {len(seen)}')
