#!/usr/bin/env python3
"""Semantically validate an artifact's CycloneDX SBOM and notices (stdlib only).

Checks the exact layout emitted by scripts/build-inventory.py --artifact-only:
canonical CycloneDX 1.6 JSON without duplicate keys, a root component naming the
artifact and carrying its exact SHA-256, Go payloads linked to the matching Go
standard library and to library modules with full legal text, a closed
dependency graph, and notices that reproduce every payload, module and legal
text recorded in the SBOM. This proves internal consistency of the inventory,
not its authenticity; authenticity comes from the signed bundle manifest.

Usage:
  python3 scripts/validate-inventory.py --artifact DIR/Telegram-MCP.dmg \
      --profile macos-dmg --preamble THIRD_PARTY_NOTICES.md --project-license LICENSE

Reads ARTIFACT.cdx.json and ARTIFACT.THIRD-PARTY-NOTICES.txt beside the artifact
and never writes. Exits non-zero with a reason on the first violation.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import sys
from urllib.parse import quote

SCHEMA = 'http://cyclonedx.org/schema/bom-1.6.schema.json'
TOP_LEVEL = {'$schema', 'bomFormat', 'specVersion', 'version', 'metadata', 'components', 'dependencies'}
COMPONENT_TYPES = {'application', 'framework', 'library', 'container', 'platform',
                   'operating-system', 'device', 'device-driver', 'firmware', 'file',
                   'machine-learning-model', 'data', 'cryptographic-asset'}
SHA256 = re.compile(r'[0-9a-f]{64}')
GO_VERSION = re.compile(r'go[0-9]+\.[0-9]+(?:\.[0-9]+|(?:beta|rc)[0-9]+)?')
MODULE_VERSION = re.compile(r'v[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*')
STDLIB = 'Go standard library'
GENERATED = '\n\n# Generated linked Go module inventory\n\n'
APPLICATION = '\n\n# Application license\n\n'
# Linux AppImage/DEB inventories append a native payload section (see
# scripts/native_inventory.py): one component per payload file, Debian package
# and pinned input, all carrying telegram-mcp:native:kind.
NATIVE = '\n\n# Native payload inventory\n\n'
NATIVE_KIND = 'telegram-mcp:native:kind'
NATIVE_FILE_REF = re.compile(r'urn:telegram-mcp:payload:[^:]+:(?:[0-9a-f]{64}|embedded)')
NATIVE_PROFILES = {'linux-appimage': 'Telegram-MCP-', 'linux-deb': 'telegram-mcp_'}
# Final DMG payloads: build-macos.sh inventories both thin slices of each
# universal executable and hashes the universal files as packaged components.
PROFILES = {
    'macos-dmg': {
        'artifact': 'Telegram-MCP.dmg',
        'payloads': {f'{binary}-{slice}': ('darwin', goarch)
                     for binary in ('telegram-mcp', 'telegram-mcp-bridge')
                     for slice, goarch in (('arm64', 'arm64'), ('x86_64', 'amd64'))},
        'files': {'telegram-mcp', 'telegram-mcp-bridge'},
    },
}


class InventoryInvalid(Exception):
    """The inventory is malformed or semantically inconsistent."""


def require(condition, message):
    if not condition:
        raise InventoryInvalid(message)


def text(value, label):
    require(isinstance(value, str) and value.strip(), f'{label} must be a non-empty string')
    return value


def unique_pairs(pairs):
    keys = [key for key, _ in pairs]
    require(len(keys) == len(set(keys)), f'duplicate JSON keys: {sorted(k for k in set(keys) if keys.count(k) > 1)}')
    return dict(pairs)


def file_ref(name, sha256):
    return f"urn:telegram-mcp:file:{quote(name, safe='')}:{sha256}"


def purl(path, version):
    return f"pkg:golang/{quote(path, safe='/')}@{quote(version, safe='')}"


def properties(component, label):
    values = component.get('properties', [])
    require(isinstance(values, list), f'{label} properties must be a list')
    result = {}
    for item in values:
        require(isinstance(item, dict) and set(item) == {'name', 'value'}, f'{label} has a malformed property')
        name, value = text(item['name'], f'{label} property name'), item['value']
        require(isinstance(value, str), f'{label} property {name} must be a string')
        require(name not in result, f'{label} repeats property {name}')
        result[name] = value
    return result


def single_sha256(component, label):
    hashes = component.get('hashes')
    require(isinstance(hashes, list) and len(hashes) == 1 and isinstance(hashes[0], dict)
            and set(hashes[0]) == {'alg', 'content'} and hashes[0]['alg'] == 'SHA-256'
            and isinstance(hashes[0]['content'], str) and SHA256.fullmatch(hashes[0]['content']),
            f'{label} must carry exactly one lowercase SHA-256 hash')
    return hashes[0]['content']


def legal_texts(component, label, prefix='', exact=None):
    licenses = component.get('licenses')
    require(isinstance(licenses, list) and licenses, f'{label} has no license text')
    result = []
    for entry in licenses:
        require(isinstance(entry, dict) and set(entry) == {'license'}
                and isinstance(entry['license'], dict) and set(entry['license']) == {'name', 'text'},
                f'{label} has a malformed license entry')
        name = text(entry['license']['name'], f'{label} license name')
        body = entry['license']['text']
        require(isinstance(body, dict) and set(body) == {'contentType', 'content'}
                and body['contentType'] == 'text/plain', f'{label} license text must be text/plain')
        content = text(body['content'], f'{label} license content')
        if exact is not None:
            require(name == exact, f'{label} license must be named {exact!r}')
            filename = None
        else:
            require(name.startswith(prefix) and name[len(prefix):].strip(),
                    f'{label} license name must start with {prefix!r}')
            filename = name[len(prefix):]
        result.append((filename, content))
    return result


def load_bom(data):
    try:
        bom = json.loads(data.decode('utf-8'), object_pairs_hook=unique_pairs)
    except (UnicodeError, json.JSONDecodeError) as error:
        raise InventoryInvalid(f'SBOM is not valid UTF-8 JSON: {error}') from error
    require(isinstance(bom, dict), 'SBOM must be a JSON object')
    canonical = (json.dumps(bom, ensure_ascii=False, sort_keys=True, indent=2) + '\n').encode('utf-8')
    require(data == canonical, 'SBOM is not in the canonical generator serialization')
    return bom


def validate_sbom(data, artifact_name, artifact_sha256, profile=None, goos=None, min_modules=1):
    """Return the parsed inventory model or raise InventoryInvalid."""
    bom = load_bom(data)
    require(set(bom) == TOP_LEVEL, f'SBOM top-level keys must be exactly {sorted(TOP_LEVEL)}')
    require(bom['$schema'] == SCHEMA, 'SBOM must reference the CycloneDX 1.6 schema')
    require(bom['bomFormat'] == 'CycloneDX', 'SBOM bomFormat must be CycloneDX')
    require(bom['specVersion'] == '1.6', 'SBOM specVersion must be 1.6')
    require(type(bom['version']) is int and bom['version'] >= 1, 'SBOM version must be a positive integer')

    metadata = bom['metadata']
    require(isinstance(metadata, dict) and set(metadata) == {'component', 'properties'},
            'SBOM metadata must contain only component and properties')
    scope = properties(metadata, 'metadata').get('telegram-mcp:inventory:scope', '')
    require('Go toolchain' in scope and 'excludes' in scope, 'SBOM lacks the inventory scope statement')
    root = metadata['component']
    require(isinstance(root, dict), 'SBOM root component must be an object')
    require(root.get('type') == 'application', 'SBOM root component must be an application')
    require(root.get('name') == artifact_name, f'SBOM root names {root.get("name")!r}, not {artifact_name!r}')
    require(single_sha256(root, 'root component') == artifact_sha256,
            'SBOM root SHA-256 does not match the exact artifact bytes')
    require(root.get('bom-ref') == file_ref(artifact_name, artifact_sha256), 'SBOM root bom-ref is not artifact-bound')
    require(properties(root, 'root component').get('telegram-mcp:inventory:artifact-scope', '').strip(),
            'SBOM root lacks its artifact scope')

    components = bom['components']
    require(isinstance(components, list) and components, 'SBOM has no components')
    refs = {}
    for component in components:
        require(isinstance(component, dict), 'SBOM component must be an object')
        ref = text(component.get('bom-ref'), 'component bom-ref')
        text(component.get('name'), f'component {ref} name')
        require(component.get('type') in COMPONENT_TYPES, f'component {ref} has an invalid CycloneDX type')
        require(ref not in refs and ref != root['bom-ref'], f'duplicate bom-ref {ref}')
        refs[ref] = component
    require([c['bom-ref'] for c in components] == sorted(refs), 'SBOM components are not sorted by bom-ref')

    stdlib = [c for c in components if c['name'] == STDLIB]
    require(len(stdlib) == 1, 'SBOM must contain exactly one Go standard library component')
    stdlib = stdlib[0]
    version = stdlib.get('version')
    require(isinstance(version, str) and GO_VERSION.fullmatch(version), 'Go standard library has no valid Go version')
    require(stdlib['type'] == 'library'
            and stdlib['bom-ref'] == f"urn:telegram-mcp:go-toolchain:{quote(version, safe='')}",
            'Go standard library component is malformed')
    stdlib_text = legal_texts(stdlib, STDLIB, exact='Go LICENSE')
    # The toolchain BSD-style LICENSE is ~1.5 KB; reject placeholders and stubs.
    require(len(stdlib_text) == 1 and len(stdlib_text[0][1]) >= 500
            and all(word in stdlib_text[0][1] for word in ('Copyright', 'Redistribution')),
            'Go standard library LICENSE is not the full toolchain license text')

    modules, payloads, files, native = {}, {}, {}, {}
    for ref, component in refs.items():
        if component is stdlib:
            continue
        kind, name = component['type'], component['name']
        native_kind = properties(component, f'component {ref}').get(NATIVE_KIND)
        if native_kind is not None:
            native[ref] = validate_native_component(component, native_kind)
            continue
        if kind == 'library':
            module_version = component.get('version')
            require(isinstance(module_version, str) and MODULE_VERSION.fullmatch(module_version),
                    f'module {name} has no valid Go module version')
            require(component.get('purl') == ref == purl(name, module_version), f'module {name} purl/bom-ref mismatch')
            values = properties(component, f'module {name}')
            try:
                origins = json.loads(values.get('telegram-mcp:go:original-modules', ''))
            except json.JSONDecodeError:
                origins = None
            require(isinstance(origins, list) and origins and all(isinstance(o, str) and '@' in o for o in origins),
                    f'module {name} lacks original module identities')
            texts = legal_texts(component, f'module {name}', prefix='Upstream legal text: ')
            modules[ref] = {'key': (name, module_version), 'texts': texts, 'origins': origins}
        elif kind == 'application':
            sha256 = single_sha256(component, f'payload {name}')
            require(ref == file_ref(name, sha256), f'payload {name} bom-ref is not hash-bound')
            values = properties(component, f'payload {name}')
            require(values.get('telegram-mcp:go:version') == version,
                    f'payload {name} was not built by the inventoried Go standard library ({version})')
            text(values.get('telegram-mcp:go:package'), f'payload {name} Go package')
            payloads[ref] = {'name': name, 'sha256': sha256, 'values': values}
        elif kind == 'file':
            sha256 = single_sha256(component, f'file component {name}')
            require(ref == file_ref(name, sha256), f'file component {name} bom-ref is not hash-bound')
            files[ref] = name
        else:
            raise InventoryInvalid(f'unexpected component type {kind} for {name}')
    require(payloads, 'SBOM has no Go payload application components')
    require(len(modules) >= min_modules, f'SBOM lists {len(modules)} Go modules; expected at least {min_modules}')
    names = [p['name'] for p in payloads.values()] + list(files.values())
    require(len(names) == len(set(names)), 'payload and file component names must be unique')

    dependencies = bom['dependencies']
    require(isinstance(dependencies, list) and dependencies, 'SBOM has no dependency graph')
    graph = {}
    for entry in dependencies:
        require(isinstance(entry, dict) and set(entry) == {'ref', 'dependsOn'}, 'malformed dependency entry')
        ref, targets = entry['ref'], entry['dependsOn']
        require(isinstance(ref, str) and ref not in graph, f'duplicate or invalid dependency ref {ref!r}')
        require(isinstance(targets, list) and targets and all(isinstance(t, str) for t in targets)
                and targets == sorted(set(targets)), f'dependency {ref} must list sorted unique targets')
        graph[ref] = targets
    native_graph = {ref: targets for ref, targets in graph.items() if ref in native}
    require(set(graph) - set(native_graph) == {root['bom-ref'], *payloads},
            'dependency graph must describe exactly the artifact, each Go payload and native components')
    native_direct = sorted(ref for ref, item in native.items()
                           if item['kind'] == 'payload-file' or ref.startswith('urn:telegram-mcp:pinned:appimage-type2-runtime:')
                           and item['kind'] == 'pinned-input')
    require(graph[root['bom-ref']] == sorted([*payloads, *files, *native_direct]),
            'artifact must depend on exactly its Go payloads, packaged file components and native payload files')
    if native:
        validate_native_graph(native, native_graph, payloads)
    linked = set()
    for ref, payload in payloads.items():
        targets = set(graph[ref])
        require(stdlib['bom-ref'] in targets, f'payload {payload["name"]} does not link the Go standard library')
        require(targets - {stdlib['bom-ref']} <= set(modules), f'payload {payload["name"]} depends on unknown components')
        payload['modules'] = sorted(modules[t]['key'] for t in targets if t in modules)
        linked.update(targets)
    require(set(modules) <= linked, 'SBOM lists Go modules that no payload links')

    if goos is not None:
        for payload in payloads.values():
            require(payload['values'].get('telegram-mcp:go:setting:GOOS') == goos,
                    f'payload {payload["name"]} is not built for {goos}')
    if profile in NATIVE_PROFILES:
        require(artifact_name.startswith(NATIVE_PROFILES[profile]), f'{profile} inventory names {artifact_name}')
        require(native and 'telegram-mcp:inventory:native-scope' in properties(metadata, 'metadata'),
                f'{profile} SBOM lacks the native payload inventory')
    elif profile is not None:
        expected = PROFILES[profile]
        require(artifact_name == expected['artifact'], f'{profile} inventory must describe {expected["artifact"]}')
        found = {p['name']: p for p in payloads.values()}
        require(set(found) == set(expected['payloads']),
                f'{profile} payloads must be exactly {sorted(expected["payloads"])}, got {sorted(found)}')
        for name, (os_name, arch) in expected['payloads'].items():
            values = found[name]['values']
            require(values.get('telegram-mcp:go:setting:GOOS') == os_name
                    and values.get('telegram-mcp:go:setting:GOARCH') == arch,
                    f'{profile} payload {name} is not a {os_name}/{arch} slice')
        require(set(files.values()) == expected['files'],
                f'{profile} packaged files must be exactly {sorted(expected["files"])}')
    return {'stdlib': {'version': version, 'text': stdlib_text[0][1]}, 'scope': scope, 'native': native,
            'payloads': sorted(payloads.values(), key=lambda p: p['name']),
            'modules': [modules[ref] for ref in sorted(modules, key=lambda r: modules[r]['key'])]}


def validate_native_component(component, kind):
    ref, name = component['bom-ref'], component['name']
    values = properties(component, f'native component {name}')
    item = {'kind': kind, 'name': name, 'values': values}
    if kind == 'payload-file':
        require(component['type'] == 'file' and NATIVE_FILE_REF.fullmatch(ref), f'native file {name} is malformed')
        require(values.get('telegram-mcp:native:path') == name, f'native file {name} path property mismatch')
        cls = text(values.get('telegram-mcp:native:class'), f'native file {name} class')
        file_type = values.get('telegram-mcp:native:file-type')
        require(file_type in ('file', 'symlink'), f'native file {name} has an invalid file type')
        if file_type == 'file' and cls != 'native-inventory':
            sha256 = single_sha256(component, f'native file {name}')
            require(ref.endswith(':' + sha256), f'native file {name} bom-ref is not hash-bound')
        item['class'] = cls
    elif kind == 'debian-package':
        require(component['type'] == 'library' and component.get('purl') == ref and ref.startswith('pkg:deb/'),
                f'Debian package {name} purl/bom-ref mismatch')
        for key in ('architecture', 'source-package', 'source-version', 'source-retrieval', 'copyright-path'):
            text(values.get('telegram-mcp:native:' + key), f'Debian package {name} {key}')
        texts = legal_texts(component, f'Debian package {name}', prefix='')
        copyright_texts = [(entry_name[len('Debian copyright file: '):], body)
                           for entry_name, body in texts
                           if entry_name.startswith('Debian copyright file: ')]
        common_texts = [(entry_name[len('Debian common license: '):], body)
                        for entry_name, body in texts
                        if entry_name.startswith('Debian common license: ')]
        require(len(copyright_texts) == 1 and len(texts) == 1 + len(common_texts),
                f'Debian package {name} must carry exactly its copyright file and referenced common licenses')
        require(all(path.startswith('/usr/share/common-licenses/') for path, _ in common_texts),
                f'Debian package {name} has an invalid common-license path')
        require(hashlib.sha256(copyright_texts[0][1].encode('utf-8')).hexdigest()
                == values.get('telegram-mcp:native:copyright-sha256'), f'Debian package {name} copyright hash mismatch')
        item['copyright'] = copyright_texts[0][1]
        item['legal'] = [body for _, body in common_texts]
    elif kind in ('pinned-input', 'statically-linked'):
        require(component['type'] in ('application', 'library'), f'native input {name} has an invalid type')
        if kind == 'pinned-input':
            single_sha256(component, f'pinned input {name}')
        item['legal'] = [body for _, body in legal_texts(component, f'native input {name}', prefix='Pinned legal text: ')]
    else:
        raise InventoryInvalid(f'unknown native component kind {kind} for {name}')
    return item


def validate_native_graph(native, graph, payloads):
    for ref, targets in graph.items():
        require(set(targets) <= set(native) | set(payloads), f'native component {ref} depends on unknown components')
    for ref, item in native.items():
        if item['kind'] != 'payload-file':
            continue
        cls, targets = item['class'], graph.get(ref, [])
        expects = {'go-payload': payloads, 'symlink': native, 'debian-package': native, 'pinned-input': native,
                   'generated': native}.get(cls)
        if expects is None:
            require(not targets, f'native file {item["name"]} ({cls}) must not have dependencies')
        else:
            require(len(targets) == 1 and targets[0] in expects, f'native file {item["name"]} ({cls}) has no origin edge')
            if cls == 'debian-package':
                require(native[targets[0]]['kind'] == 'debian-package', f'native file {item["name"]} is not package-owned')


def native_texts(model):
    texts = []
    for item in model.get('native', {}).values():
        if 'copyright' in item:
            texts.append(item['copyright'])
        texts += item.get('legal', [])
    return texts


def generated_notice(model):
    """Rebuild build-inventory.py's generated notice section from the SBOM model."""
    chunks = [GENERATED, model['scope'], '\n\nBinaries:\n']
    chunks += [f"- {p['name']} (SHA-256: {p['sha256']})\n" for p in model['payloads']]
    stdlib = model['stdlib']
    chunks += [f"\n## {STDLIB} ({stdlib['version']})\n\n--- Go LICENSE ---\n",
               stdlib['text'], '' if stdlib['text'].endswith('\n') else '\n']
    for module in model['modules']:
        path, version = module['key']
        chunks.append(f'\n## {path}@{version}\n\n')
        chunks.append('Linked by: ' + ', '.join(p['name'] for p in model['payloads']
                                                if module['key'] in p['modules']) + '\n')
        originals = sorted(set(module['origins']) - {f'{path}@{version}'})
        if originals:
            chunks.append('Replaces: ' + ', '.join(originals) + '\n')
        for filename, body in module['texts']:
            chunks += [f'\n--- {filename} ---\n', body, '' if body.endswith('\n') else '\n']
    return ''.join(chunks)


def validate_notices(data, model, preamble=None, project_license=None):
    try:
        notice = data.decode('utf-8')
    except UnicodeError as error:
        raise InventoryInvalid(f'notices are not UTF-8: {error}') from error
    native = model.get('native')
    if native:
        require(notice.count(NATIVE) == 1, 'notices lack the native payload inventory section')
        notice, native_notice = notice.split(NATIVE, 1)
        for body in native_texts(model):
            require(body in native_notice, 'native notices do not reproduce every SBOM copyright/legal text')
        for item in native.values():
            if item['kind'] == 'payload-file':
                require(f"- {item['name']} [" in native_notice, f'native notices omit payload file {item["name"]}')
        require('Written offer' in native_notice or not any(i['kind'] == 'debian-package' for i in native.values()),
                'native notices lack the corresponding-source written offer')
    else:
        require(NATIVE not in notice, 'notices carry a native section the SBOM does not describe')
    tail = generated_notice(model)
    require(notice.endswith(tail),
            'notices do not reproduce the SBOM payloads, Go standard library and module legal texts')
    head = notice[:-len(tail)]
    if preamble is not None and project_license is not None:
        require(preamble.strip() and project_license.strip(), 'selected curated credits or project license is empty')
        require(head == preamble.rstrip() + APPLICATION + project_license.rstrip(),
                'notices do not carry the selected source curated credits and project license')
        return
    require(APPLICATION in head, 'notices lack the application license section')
    curated, license_text = head.split(APPLICATION, 1)
    require(curated.strip() and license_text.strip(), 'notices lack curated credits or the application license')
    if preamble is not None:
        require(curated == preamble.rstrip(), 'notices do not carry the selected source curated credits')
    if project_license is not None:
        require(license_text == project_license.rstrip(), 'notices do not carry the selected source project license')


def validate_artifact(artifact, profile=None, goos=None, preamble=None, project_license=None, min_modules=1):
    """Validate ARTIFACT plus its sibling .cdx.json and notices; returns the model."""
    artifact = Path(artifact)

    def read(path):
        require(path.is_file() and not path.is_symlink(), f'missing or unsafe inventory input: {path.name}')
        return path.read_bytes()

    sha256 = hashlib.sha256(read(artifact)).hexdigest()
    model = validate_sbom(read(artifact.with_name(artifact.name + '.cdx.json')), artifact.name, sha256,
                          profile=profile, goos=goos, min_modules=min_modules)
    validate_notices(read(artifact.with_name(artifact.name + '.THIRD-PARTY-NOTICES.txt')), model,
                     preamble=preamble, project_license=project_license)
    return model


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--artifact', required=True, type=Path)
    parser.add_argument('--profile', choices=sorted([*PROFILES, *NATIVE_PROFILES]))
    parser.add_argument('--goos', help='require every Go payload to target this GOOS')
    parser.add_argument('--preamble', type=Path, help='curated credits the notices must carry exactly')
    parser.add_argument('--project-license', type=Path, help='project license the notices must carry exactly')
    args = parser.parse_args(argv)
    try:
        preamble = args.preamble.read_bytes().decode('utf-8') if args.preamble else None
        project_license = args.project_license.read_bytes().decode('utf-8') if args.project_license else None
        model = validate_artifact(args.artifact, profile=args.profile, goos=args.goos,
                                  preamble=preamble, project_license=project_license)
    except (InventoryInvalid, OSError, UnicodeError) as error:
        print(f'Inventory refused for {args.artifact.name}: {error}', file=sys.stderr)
        return 1
    print(f"Inventory verified: {args.artifact.name} ({len(model['payloads'])} Go payloads, "
          f"{len(model['modules'])} modules, {STDLIB} {model['stdlib']['version']})")
    return 0


if __name__ == '__main__':
    sys.exit(main())
