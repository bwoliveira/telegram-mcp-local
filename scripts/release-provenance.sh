#!/usr/bin/env bash
# Local Mac builder attestation: not a detached cryptographic publisher signature.
# Linux verifies binding/integrity; Apple trust checks run on the Mac builder.
release_mac_provenance() {
  python3 - "$@" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import re
import sys

MANIFEST = 'mac-build.json'
EVIDENCE = ('mac-build.log', 'mac-image-verify.log', 'mac-signature.log',
            'mac-stapler.log', 'mac-gatekeeper.log', 'notarization.json')
NOTARY_ID = r'[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}'
action, directory, argument = sys.argv[1:]
root = Path(directory)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read(name):
    path = root / name
    require(path.is_file() and not path.is_symlink(), f'missing/unsafe bundle file: {name}')
    return path.read_bytes()


def image(data):
    # UDIF images have a 512-byte koly trailer. Full hdiutil verification is a
    # Mac-side gate; this sanity check rejects renamed plain text on Linux.
    require(len(data) >= 512 and data[-512:-508] == b'koly', 'not a UDIF disk image')


def signer(data):
    text = data.decode('utf-8')
    authority = re.search(r'^Authority=(Developer ID Application: .+)$', text, re.M)
    team = re.search(r'^TeamIdentifier=([A-Z0-9]{10})$', text, re.M)
    require(authority and team, 'missing Developer ID authority/team verification')
    return {'verified': True, 'authority': authority[1], 'team_id': team[1]}


try:
    expected = {key.lower(): os.environ[key] for key in
                ('VERSION', 'SOURCE_REF', 'SOURCE_COMMIT', 'SOURCE_TREE', 'BUILD_MODE')}
    if action == 'create':
        unsigned = argument == 'unsigned'
        require(argument in ('signed', 'unsigned'), 'invalid signature mode')
        name = 'Telegram-MCP-unsigned.dmg' if unsigned else 'Telegram-MCP.dmg'
        data = read(name)
        image(data)
        inventory_names = (name + '.cdx.json', name + '.THIRD-PARTY-NOTICES.txt')
        inventories = {item: read(item) for item in inventory_names}
        require(all(inventories.values()), 'empty DMG SBOM or notices')
        manifest = dict(schema_version=1, **expected,
                        artifact={'name': name, 'sha256': digest(data)},
                        inventories={item: digest(content) for item, content in inventories.items()},
                        signature={'verified': False},
                        notarization={'status': 'NotSubmitted', 'stapled': False}, evidence={})
        if not unsigned:
            evidence = {name: read(name) for name in EVIDENCE}
            require(all(evidence.values()), 'empty verification evidence')
            notary = json.loads(evidence['notarization.json'])
            require(notary['status'] == 'Accepted', 'notarization was not Accepted')
            require(re.fullmatch(NOTARY_ID, notary['id']), 'invalid notarization ID')
            manifest['signature'] = signer(evidence['mac-signature.log'])
            manifest['notarization'] = {'status': 'Accepted', 'id': notary['id'], 'stapled': True}
            manifest['evidence'] = {name: digest(data) for name, data in evidence.items()}
        (root / MANIFEST).write_text(json.dumps(manifest, indent=2) + '\n')
    elif action == 'import':
        # Read once, validate every byte/field, then write those same bytes. The
        # caller never copies a mutable external DMG after checking its hash.
        manifest_data = read(MANIFEST)
        manifest = json.loads(manifest_data)
        require(type(manifest['schema_version']) is int and manifest['schema_version'] == 1,
                'unsupported Mac manifest schema')
        for key, value in expected.items():
            require(manifest[key] == value, f'Mac bundle {key} does not match selected source')
        require(manifest['artifact']['name'] == 'Telegram-MCP.dmg', 'unsigned/wrong artifact')
        data = read('Telegram-MCP.dmg')
        image(data)
        require(manifest['artifact']['sha256'] == digest(data), 'Mac artifact SHA-256 mismatch')
        inventory_names = ('Telegram-MCP.dmg.cdx.json', 'Telegram-MCP.dmg.THIRD-PARTY-NOTICES.txt')
        inventories = {name: read(name) for name in inventory_names}
        require(set(manifest['inventories']) == set(inventory_names), 'missing/unknown Mac inventory')
        for name, content in inventories.items():
            require(content and manifest['inventories'][name] == digest(content),
                    f'Mac inventory SHA-256 mismatch: {name}')
        require(manifest['signature']['verified'] is True, 'Mac signature is not verified')
        notary = manifest['notarization']
        require(notary['status'] == 'Accepted' and notary['stapled'] is True,
                'Mac notarization/stapling is not verified')
        require(set(manifest['evidence']) == set(EVIDENCE), 'missing/unknown Mac verification evidence')
        evidence = {name: read(name) for name in EVIDENCE}
        for name, content in evidence.items():
            require(content and manifest['evidence'][name] == digest(content),
                    f'Mac evidence SHA-256 mismatch: {name}')
        require(manifest['signature'] == signer(evidence['mac-signature.log']),
                'signature evidence does not match manifest')
        submission = json.loads(evidence['notarization.json'])
        require(submission['status'] == 'Accepted' and submission['id'] == notary['id']
                and re.fullmatch(NOTARY_ID, notary['id']),
                'notarization evidence does not match manifest')
        require(b'source=Notarized Developer ID' in evidence['mac-gatekeeper.log'],
                'Gatekeeper did not verify a notarized Developer ID')
        destination = Path(argument)
        for name, content in {MANIFEST: manifest_data, 'Telegram-MCP.dmg': data,
                              **inventories, **evidence}.items():
            with (destination / name).open('xb') as stream:
                stream.write(content)
    else:
        raise ValueError('unknown Mac provenance operation')
except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
    sys.exit(f'Mac provenance refused: {error}')
PY
}
