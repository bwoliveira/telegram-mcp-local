#!/usr/bin/env python3
"""Offline command-boundary regressions for private source/checksum preparation."""
import hashlib
import importlib.util
import json
import os
import shutil
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
HELPER = ROOT / 'scripts/release-common.sh'
VALIDATOR = ROOT / 'scripts/validate-inventory.py'
GENERATOR = ROOT / 'scripts/build-inventory.py'
# Production trust anchor pinned in the tracked install.sh and published in
# docs/security/release-signing.md (minisign key ID CF15D44BF3EA0B47).
PRODUCTION_PUBLIC_KEY = 'RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB'
PRODUCTION_RELEASE_VERSION = '1.0.0'

# Builds DMG inventories through the real build-inventory.py bom()/notices()
# serializers from synthetic Go payload metadata (no Mac, lipo or Go cache), and
# defines semantic corruptions that keep every file syntactically present.
FIXTURE_INVENTORY = r'''
import hashlib, importlib.util, json
from pathlib import Path

GO_LICENSE = """Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED.
"""
STDLIB_REF = 'urn:telegram-mcp:go-toolchain:go1.26.5'


def generator(path):
    spec = importlib.util.spec_from_file_location('build_inventory', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def build(artifact, generator_path, preamble, project_license):
    inv = generator(generator_path)
    toolchain = {'type': 'library', 'bom-ref': STDLIB_REF, 'name': 'Go standard library',
                 'version': 'go1.26.5', 'licenses': [{'license': {'name': 'Go LICENSE',
                 'text': {'contentType': 'text/plain', 'content': GO_LICENSE}}}]}
    modules = [{'key': (path, version), 'texts': [('LICENSE', f'Copyright {path} authors.\nPermission granted.\n')],
                'origins': {f'{path}@{version}'}, 'sums': {'h1:' + 'A' * 43 + '='}}
               for path, version in (('github.com/fixture/sqlite', 'v1.2.3'), ('golang.org/x/sys', 'v0.30.0'))]
    payloads = []
    for binary in ('telegram-mcp', 'telegram-mcp-bridge'):
        for slice, goarch in (('arm64', 'arm64'), ('x86_64', 'amd64')):
            name = f'{binary}-{slice}'
            root = inv.file_component(name, hashlib.sha256(name.encode()).hexdigest(), {
                'telegram-mcp:go:version': 'go1.26.5', 'telegram-mcp:go:package': 'example/' + binary,
                'telegram-mcp:go:setting:GOOS': 'darwin', 'telegram-mcp:go:setting:GOARCH': goarch})
            linked = modules if binary == 'telegram-mcp' else modules[1:]
            payloads.append({'name': name, 'root': root, 'modules': linked, 'go_version': 'go1.26.5'})
    payloads.sort(key=lambda payload: payload['name'])  # as generate() orders --binary inputs
    files = [inv.file_component(name, hashlib.sha256(b'universal ' + name.encode()).hexdigest(),
                                {'telegram-mcp:inventory:component-scope': 'Explicitly supplied packaged component'},
                                kind='file') for name in ('telegram-mcp', 'telegram-mcp-bridge')]
    artifact = Path(artifact)
    root = inv.file_component(artifact.name, hashlib.sha256(artifact.read_bytes()).hexdigest(),
                              {'telegram-mcp:inventory:artifact-scope': 'Inventory generated from payloads.'})
    sbom = inv.bom(root, inv.merge_modules(payloads), toolchain, payloads, files)
    return sbom, inv.notices(preamble, project_license, payloads, toolchain)


def canonical(bom):
    return (json.dumps(bom, ensure_ascii=False, sort_keys=True, indent=2) + '\n').encode()


def components(bom, keep):
    bom['components'] = [c for c in bom['components'] if keep(c)]
    refs = {c['bom-ref'] for c in bom['components']} | {bom['metadata']['component']['bom-ref']}
    bom['dependencies'] = [{'ref': d['ref'], 'dependsOn': [t for t in d['dependsOn'] if t in refs]}
                           for d in bom['dependencies'] if d['ref'] in refs]


def payload(bom, name):
    return next(c for c in bom['components'] if c['name'] == name)


def set_property(component, name, value):
    for item in component['properties']:
        if item['name'] == name:
            item['value'] = value


def rehash_root(bom, data):
    root = bom['metadata']['component']
    digest = hashlib.sha256(data).hexdigest()
    old, root['hashes'][0]['content'] = root['bom-ref'], digest
    root['bom-ref'] = f"urn:telegram-mcp:file:{root['name']}:{digest}"
    for entry in bom['dependencies']:
        if entry['ref'] == old:
            entry['ref'] = root['bom-ref']


def rename_root(bom, name):
    root = bom['metadata']['component']
    old, root['name'] = root['bom-ref'], name
    root['bom-ref'] = f"urn:telegram-mcp:file:{name}:{root['hashes'][0]['content']}"
    for entry in bom['dependencies']:
        if entry['ref'] == old:
            entry['ref'] = root['bom-ref']


def hash_only(bom):
    bom['components'], bom['dependencies'] = [], []


def drop_module_section(notice):
    start = notice.index('\n## github.com/fixture/sqlite@v1.2.3\n')
    end = notice.index('\n## golang.org/x/sys@v0.30.0\n')
    return notice[:start] + notice[end:]


# name -> (SBOM mutation, notices mutation, expected refusal substring)
MUTATIONS = {
    'hash-only': (hash_only, lambda n: 'Go standard library\nCopyright\n', 'no components'),
    'legacy-placeholder': (lambda b: (b.clear(), b.update(bomFormat='CycloneDX', artifact='Telegram-MCP.dmg')),
                           None, 'top-level keys'),
    'spec-1.4': (lambda b: b.update(specVersion='1.4'), None, 'specVersion'),
    'wrong-root-name': (lambda b: rename_root(b, 'Other.dmg'), None, 'not \'Telegram-MCP.dmg\''),
    'wrong-root-hash': (lambda b: rehash_root(b, b'other bytes'), None, 'exact artifact bytes'),
    'no-stdlib': (lambda b: components(b, lambda c: c['name'] != 'Go standard library'), None,
                  'exactly one Go standard library'),
    'stub-stdlib-license': (lambda b: next(c for c in b['components'] if c['name'] == 'Go standard library')
                            ['licenses'][0]['license']['text'].update(content='Copyright Go'),
                            None, 'full toolchain license'),
    'stdlib-only': (lambda b: components(b, lambda c: c['type'] != 'library' or c['name'] == 'Go standard library'),
                    None, 'expected at least 1'),
    'empty-module-license': (lambda b: b['components'][0]['licenses'][0]['license']['text'].update(content=' '),
                             None, 'license content'),
    'payload-without-stdlib': (lambda b: [d['dependsOn'].remove(STDLIB_REF) for d in b['dependencies']
                                          if STDLIB_REF in d['dependsOn']][:0], None,
                               'does not link the Go standard library'),
    'dangling-dependency': (lambda b: b['dependencies'][1].update(dependsOn=sorted(
                                b['dependencies'][1]['dependsOn'] + ['pkg:golang/absent@v1.0.0'])),
                            None, 'unknown components'),
    'orphan-module': (lambda b: [d['dependsOn'].remove('pkg:golang/github.com/fixture/sqlite@v1.2.3')
                                 for d in b['dependencies'] if 'pkg:golang/github.com/fixture/sqlite@v1.2.3'
                                 in d['dependsOn']][:0], None, 'no payload links'),
    'missing-slice': (lambda b: components(b, lambda c: c['name'] != 'telegram-mcp-bridge-x86_64'), None,
                      'payloads must be exactly'),
    'wrong-goarch': (lambda b: set_property(payload(b, 'telegram-mcp-x86_64'),
                                            'telegram-mcp:go:setting:GOARCH', 'arm64'), None, 'darwin/amd64 slice'),
    'foreign-toolchain-payload': (lambda b: set_property(payload(b, 'telegram-mcp-arm64'),
                                                         'telegram-mcp:go:version', 'go1.20.1'),
                                  None, 'not built by the inventoried Go standard library'),
    'notice-stub': (None, lambda n: 'Go standard library Copyright\n', 'notices do not reproduce'),
    'notice-missing-module-text': (None, drop_module_section, 'notices do not reproduce'),
    'notice-wrong-credits': (None, lambda n: n.replace('Fixture curated credits', 'Other credits', 1),
                             'selected source curated credits'),
}


def write(artifact, output, generator_path, preamble, project_license, mutation=''):
    sbom, notice = build(artifact, generator_path, preamble, project_license)
    if mutation:
        mutate_bom, mutate_notice, _ = MUTATIONS[mutation]
        if mutate_bom:
            bom = json.loads(sbom)
            mutate_bom(bom)
            sbom = canonical(bom)
        if mutate_notice:
            notice = mutate_notice(notice.decode()).encode()
    name = Path(artifact).name
    (Path(output) / (name + '.cdx.json')).write_bytes(sbom)
    (Path(output) / (name + '.THIRD-PARTY-NOTICES.txt')).write_bytes(notice)
'''
PREAMBLE = '# Fixture curated credits\n\nPreserve upstream authors.\n'
PROJECT_LICENSE = 'MIT License\n\nCopyright (c) 2026 Fixture\n'


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run(args, cwd, **kw):
    return subprocess.run(args, cwd=cwd, text=True, capture_output=True, **kw)


class ReleaseFixture(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture_tools = None
        candidates = [os.environ.get('MINISIGN', ''), shutil.which('minisign')]
        scratch = Path(os.environ.get('TMPDIR', '/root/.hermes/cache/scratch'))
        candidates.extend(str(p) for p in scratch.glob('**/bin/minisign'))
        cls.minisign = next((str(Path(p).resolve()) for p in candidates
                             if p and Path(p).is_file() and os.access(p, os.X_OK)), None)
        if not cls.minisign:
            cls.fixture_tools = tempfile.TemporaryDirectory()
            cls.minisign = str(Path(cls.fixture_tools.name) / 'minisign')
            subprocess.run(['go', 'build', '-o', cls.minisign,
                            './scripts/testdata/minisign-fixture'],
                           cwd=ROOT, check=True, capture_output=True)

    @classmethod
    def tearDownClass(cls):
        if cls.fixture_tools:
            cls.fixture_tools.cleanup()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        for args in (['init', '-q'], ['config', 'user.name', 'Fixture'],
                     ['config', 'user.email', 'fixture@example.invalid'],
                     ['remote', 'add', 'origin', 'https://github.com/bwoliveira/telegram-mcp-local.git']):
            run(['git', *args], self.repo, check=True)
        (self.repo / 'tracked').write_text('committed\n')
        (self.repo / '.gitignore').write_text('ignored\n')
        run(['git', 'add', '.'], self.repo, check=True)
        run(['git', 'commit', '-qm', 'fixture'], self.repo, check=True)
        self.ref = run(['git', 'rev-parse', 'HEAD'], self.repo, check=True).stdout.strip()
        self.tools = self.root / 'tools'
        self.tools.mkdir()
        (self.tools / 'minisign').symlink_to(self.minisign)
        self.secret = self.root / 'test-only.key'
        self.public = self.root / 'test-only.pub'
        run([self.minisign, '-G', '-W', '-s', str(self.secret), '-p', str(self.public)],
            self.root, check=True)
        self.public_key = self.public.read_text().splitlines()[1]
        (self.repo / 'install.sh').write_text(
            f'public_key={self.public_key}\nrelease_version=1.2.3\n')
        run(['git', 'add', 'install.sh'], self.repo, check=True)
        run(['git', 'commit', '--amend', '--no-edit', '-q'], self.repo, check=True)
        self.ref = run(['git', 'rev-parse', 'HEAD'], self.repo, check=True).stdout.strip()
        gh = self.tools / 'gh'
        gh.write_text('#!/bin/sh\nprintf "%s\\n" "${FIXTURE_PRIVATE-true}"\n')
        gh.chmod(0o755)
        self.out = self.root / 'output'
        self.env = dict(os.environ, PATH=f'{self.tools}:{os.environ["PATH"]}',
                        TELEGRAM_MCP_MINISIGN_SECRET_KEY=str(self.secret),
                        TELEGRAM_MCP_MINISIGN_PUBLIC_KEY=self.public_key,
                        TMPDIR=str(self.root), PRO_DIR='', TAGS='')


class ReleaseTests(ReleaseFixture):
    def prepare(self, *args, after=':', **env):
        return run(['bash', '-euc', f'source "$1"; shift; release_prepare "$@"; {after}',
                    'fixture', str(HELPER), *args], self.repo, env=dict(self.env, **env))

    def test_invalid_versions_and_test_ref_contract(self):
        for version in ('', 'v1.2.3', '01.2.3', '1.2', '1.2.3-rc1', '1.2.3;true'):
            with self.subTest(version=version):
                self.assertNotEqual(self.prepare(version).returncode, 0)
                self.assertFalse(self.out.exists())
        for args in [('0.0.0',), ('1.2.3', '--ref', self.ref, '--output', str(self.out)),
                     ('0.0.0', '--ref', self.ref[:8], '--output', str(self.out)),
                     ('0.0.0', '--ref', self.ref)]:
            self.assertNotEqual(self.prepare(*args).returncode, 0)

    def test_clean_snapshot_excludes_dirty_ignored_and_untracked_inputs(self):
        (self.repo / 'tracked').write_text('dirty\n')
        (self.repo / 'ignored').write_text('private\n')
        (self.repo / 'untracked').write_text('private\n')
        result = self.prepare('0.0.0', '--ref', self.ref, '--output', str(self.out),
                              after='cp "$src/tracked" "$OUT/tracked"; '
                              '! test -e "$src/ignored"; ! test -e "$src/untracked"; '
                              'test -z "$(git -C "$src" status --porcelain)"')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.out / 'tracked').read_text(), 'committed\n')
        info = json.loads((self.out / 'build-source.json').read_text())
        self.assertEqual(info['source_commit'], self.ref)
        self.assertEqual(info['build_mode'], 'private-test')
        self.assertFalse(list(self.root.glob('tmp.*')))

    def test_tag_uses_exact_commit_not_working_head(self):
        run(['git', 'tag', 'v1.2.3'], self.repo, check=True)
        (self.repo / 'tracked').write_text('later\n')
        run(['git', 'commit', '-qam', 'later'], self.repo, check=True)
        result = self.prepare('1.2.3', '--output', str(self.out),
                              after='cp "$src/tracked" "$OUT/tracked"')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.out / 'tracked').read_text(), 'committed\n')
        self.assertEqual(json.loads((self.out / 'build-source.json').read_text())['build_mode'], 'tag')

    def test_origin_and_visibility_fail_before_output_creation(self):
        args = ('0.0.0', '--ref', self.ref, '--output', str(self.out))
        self.assertNotEqual(self.prepare(*args, FIXTURE_PRIVATE='false').returncode, 0)
        self.assertFalse(self.out.exists())
        self.assertNotEqual(self.prepare(*args, FIXTURE_PRIVATE='').returncode, 0)
        self.assertFalse(self.out.exists())
        run(['git', 'remote', 'set-url', 'origin', 'https://github.com/other/repo.git'], self.repo, check=True)
        self.assertNotEqual(self.prepare(*args).returncode, 0)
        self.assertFalse(self.out.exists())

    def test_output_is_never_overwritten(self):
        self.out.mkdir()
        (self.out / 'keep').write_text('keep')
        result = self.prepare('0.0.0', '--ref', self.ref, '--output', str(self.out))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.out / 'keep').read_text(), 'keep')

    def test_build_environment_cannot_inject_untracked_go_inputs(self):
        result = self.prepare('0.0.0', '--ref', self.ref, '--output', str(self.out),
                              GOFLAGS='-overlay=/untracked -modfile=/untracked',
                              GOWORK='/untracked/go.work', GOENV='/untracked/go.env',
                              after='test -z "$GOFLAGS"; test "$GOWORK" = off; test "$GOENV" = off')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_signing_rejects_missing_and_placeholder_keys_before_output(self):
        args = ('0.0.0', '--ref', self.ref, '--output', str(self.out))
        for key in ('', 'REPLACE_WITH_RELEASE_KEY', 'RW' + 'A' * 54):
            result = self.prepare(*args, TELEGRAM_MCP_MINISIGN_PUBLIC_KEY=key,
                                  TELEGRAM_MCP_MINISIGN_SECRET_KEY='')
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse(self.out.exists())

    def test_secret_key_must_be_external_and_explicit(self):
        args = ('0.0.0', '--ref', self.ref, '--output', str(self.out))
        inside = self.repo / 'secret.key'
        shutil.copy2(self.secret, inside)
        link = self.root / 'key-link'
        link.symlink_to(inside)
        for secret in ('', 'relative.key', str(self.root / 'missing.key'), str(inside), str(link)):
            result = self.prepare(*args, TELEGRAM_MCP_MINISIGN_SECRET_KEY=secret)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            self.assertFalse(self.out.exists())

    def test_public_key_embeds_despite_explicit_ldflags_and_empty_goflags(self):
        (self.repo / 'go.mod').write_text('module github.com/bwoliveira/telegram-mcp-local\n\ngo 1.23\n')
        package = self.repo / 'internal/updater'
        package.mkdir(parents=True)
        (package / 'key.go').write_text('package updater\nvar ReleasePublicKey string\n')
        (self.repo / 'main.go').write_text('''package main
import("fmt"; "github.com/bwoliveira/telegram-mcp-local/internal/updater")
var version string
func main(){ fmt.Printf("%s:%s",version,updater.ReleasePublicKey) }
''')
        command = ('source "$1"; release_work="$2"; release_embed_public_key; '
                   'export GOFLAGS= GOWORK=off; go version; '
                   'go build -ldflags "-s -w -X main.version=0.0.0" -o "$2/probe" .; "$2/probe"')
        result = run(['bash', '-euc', command, 'fixture', str(HELPER), str(self.root)],
                     self.repo, env=self.env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(result.stdout.endswith(f'0.0.0:{self.public_key}'), result.stdout)

    def test_production_installer_must_pin_selected_key(self):
        source = self.root / 'source'
        source.mkdir()
        installer = source / 'install.sh'
        command = ('source "$1"; BUILD_MODE=tag; VERSION=1.2.3; src="$2"; '
                   'release_validate_installer_key')
        installer.write_text('public_key=REPLACE_WITH_PRODUCTION_MINISIGN_PUBLIC_KEY\n'
                             'release_version=REPLACE_WITH_PRODUCTION_RELEASE_VERSION\n')
        result = run(['bash', '-euc', command, 'fixture', str(HELPER), str(source)],
                     self.repo, env=self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('placeholder', result.stderr)
        installer.write_text(f'public_key={self.public_key}\nrelease_version=1.2.3\n')
        result = run(['bash', '-euc', command, 'fixture', str(HELPER), str(source)],
                     self.repo, env=self.env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        installer.write_text(f'public_key=wrong-key\n# public_key={self.public_key}\n')
        result = run(['bash', '-euc', command, 'fixture', str(HELPER), str(source)],
                     self.repo, env=self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('exactly once', result.stderr)

    def test_tracked_installer_accepts_only_production_key_and_version(self):
        # Validate the real tracked install.sh, not a synthetic copy: a tag build
        # signed with any other key, or for another version, must be refused.
        source = self.root / 'tracked-source'
        source.mkdir()
        shutil.copyfile(ROOT / 'install.sh', source / 'install.sh')
        command = ('source "$1"; BUILD_MODE=tag; VERSION="$3"; src="$2"; '
                   'TELEGRAM_MCP_MINISIGN_PUBLIC_KEY="$4"; release_validate_installer_key')

        def validate(key, version):
            return run(['bash', '-euc', command, 'fixture', str(HELPER), str(source),
                        version, key], self.repo, env=self.env)

        result = validate(PRODUCTION_PUBLIC_KEY, PRODUCTION_RELEASE_VERSION)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotEqual(self.public_key, PRODUCTION_PUBLIC_KEY)
        result = validate(self.public_key, PRODUCTION_RELEASE_VERSION)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('pinned exactly once to the selected release key', result.stderr)
        result = validate(PRODUCTION_PUBLIC_KEY, '1.0.1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('pinned exactly once to the selected release version', result.stderr)

    def test_imported_bundle_must_use_selected_release_key(self):
        bundle = self.root / 'mac-bundle'
        bundle.mkdir()
        (bundle / 'build-source.json').write_text(json.dumps(
            {'version': '0.0.0', 'source_commit': self.ref}))
        (bundle / 'Telegram-MCP.dmg').write_bytes(b'fixture dmg')
        sign = run(['bash', '-euc', 'source "$1"; release_checksums "$2"',
                    'fixture', str(HELPER), str(bundle)], self.repo, env=self.env)
        self.assertEqual(sign.returncode, 0, sign.stderr)
        verify = run(['bash', '-euc', 'source "$1"; release_verify_signed_bundle "$2" "$3"',
                      'fixture', str(HELPER), str(bundle), str(self.root / 'verified')], self.repo, env=self.env)
        self.assertEqual(verify.returncode, 0, verify.stderr)
        other_secret, other_public = self.root / 'other.key', self.root / 'other.pub'
        run([self.minisign, '-G', '-W', '-s', str(other_secret), '-p', str(other_public)],
            self.root, check=True)
        wrong = run(['bash', '-euc', 'source "$1"; release_verify_signed_bundle "$2" "$3"',
                     'fixture', str(HELPER), str(bundle), str(self.root / 'wrong-verified')], self.repo,
                    env=dict(self.env, TELEGRAM_MCP_MINISIGN_PUBLIC_KEY=other_public.read_text().splitlines()[1]))
        self.assertNotEqual(wrong.returncode, 0)
        self.assertIn('different or invalid', wrong.stderr)
        (bundle / 'Telegram-MCP.dmg').write_bytes(b'tampered dmg')
        self.assertEqual((self.root / 'verified' / 'Telegram-MCP.dmg').read_bytes(), b'fixture dmg')
        tampered = run(['bash', '-euc', 'source "$1"; release_verify_signed_bundle "$2" "$3"',
                        'fixture', str(HELPER), str(bundle), str(self.root / 'tampered-verified')], self.repo, env=self.env)
        self.assertNotEqual(tampered.returncode, 0)
        self.assertIn('hash does not match', tampered.stderr)

    def test_mismatched_signing_key_removes_signature(self):
        self.out.mkdir()
        (self.out / 'build-source.json').write_text(json.dumps(
            {'version': '0.0.0', 'source_commit': self.ref}))
        (self.out / 'app.bin').write_bytes(b'fixture')
        other_secret, other_public = self.root / 'other.key', self.root / 'other.pub'
        run([self.minisign, '-G', '-W', '-s', str(other_secret), '-p', str(other_public)],
            self.root, check=True)
        (self.out / 'checksums.txt.minisig').write_text('stale signature')
        result = run(['bash', '-euc', 'source "$1"; release_checksums "$2"',
                      'fixture', str(HELPER), str(self.out)], self.repo,
                     env=dict(self.env, TELEGRAM_MCP_MINISIGN_PUBLIC_KEY=other_public.read_text().splitlines()[1]))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('signing or public-key verification failed', result.stderr)
        self.assertFalse((self.out / 'checksums.txt.minisig').exists())

    def test_checksums_include_mac_and_are_repeatable_without_self(self):
        self.out.mkdir()
        (self.out / 'build-source.json').write_text(json.dumps(
            {'version': '0.0.0', 'source_commit': self.ref}))
        for name, data in {'Telegram-MCP.dmg': b'mac fixture', 'linux.tar.gz': b'linux fixture'}.items():
            (self.out / name).write_bytes(data)
        for _ in range(2):
            result = run(['bash', '-euc', 'source "$1"; release_checksums "$2"',
                          'fixture', str(HELPER), str(self.out)], self.repo, env=self.env)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            lines = (self.out / 'checksums.txt').read_text().splitlines()
            self.assertEqual(lines[:2], ['# version: 0.0.0', f'# source_commit: {self.ref}'])
            self.assertRegex(lines[2], r'^# date: \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$')
            self.assertEqual(len(lines), 6)
            for line in lines[3:]:
                digest, name = line.split('  ')
                self.assertNotIn(name, ('checksums.txt', 'checksums.txt.minisig'))
                self.assertEqual(digest, hashlib.sha256((self.out / name).read_bytes()).hexdigest())
            verified = run([self.minisign, '-V', '-P', self.public_key, '-m',
                            str(self.out / 'checksums.txt')], self.repo)
            self.assertEqual(verified.returncode, 0, verified.stderr)
            import base64
            packet = base64.b64decode((self.out / 'checksums.txt.minisig').read_text().splitlines()[1])
            self.assertEqual(packet[:2], b'Ed', 'must sign raw bytes, not prehashed ED format')
        # Real Go standard-library verifier, independent of minisign verification.
        probe = self.root / 'verify.go'
        probe.write_text('''package main
import("crypto/ed25519"; "encoding/base64"; "os"; "strings")
func main() {
 p,_:=base64.StdEncoding.DecodeString(os.Args[1])
 s,_:=os.ReadFile(os.Args[2]); lines:=strings.Split(string(s), "\\n")
 sig,_:=base64.StdEncoding.DecodeString(lines[1]); m,_:=os.ReadFile(os.Args[3])
 if len(p)!=42 || len(sig)!=74 || !ed25519.Verify(p[10:], m, sig[10:]) { os.Exit(1) }
}
''')
        verified = run(['go', 'run', str(probe), self.public_key,
                        str(self.out / 'checksums.txt.minisig'), str(self.out / 'checksums.txt')],
                       self.repo, env=dict(self.env, GOWORK='off', GOFLAGS=''))
        self.assertEqual(verified.returncode, 0, verified.stderr)
        with (self.out / 'checksums.txt').open('a') as manifest:
            manifest.write('# tampered\\n')
        rejected = run([self.minisign, '-V', '-P', self.public_key, '-m',
                        str(self.out / 'checksums.txt')], self.repo)
        self.assertNotEqual(rejected.returncode, 0)

    def test_appimage_extractor_rejects_invalid_elf_without_writing(self):
        image = self.root / 'invalid.AppImage'
        for content in (b'', b'not an executable', b'\x7fELF\x02\x01' + bytes(58)):
            image.write_bytes(content)
            result = run([os.environ.get('PYTHON', 'python3'),
                          str(ROOT / 'scripts/extract-appimage.py'), str(image), str(self.out)], self.repo)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(self.out.exists())


class InventoryValidatorTests(unittest.TestCase):
    """Semantic DMG SBOM/notices gate, independent of signatures and digests."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'fixture_inventory.py').write_text(FIXTURE_INVENTORY)
        self.fixture = load_module('fixture_inventory', self.root / 'fixture_inventory.py')
        self.validator = load_module('validate_inventory', VALIDATOR)
        self.dmg = self.root / 'Telegram-MCP.dmg'
        self.dmg.write_bytes(b'fixture UDIF payload' + b'koly' + bytes(508))
        (self.root / 'credits.md').write_text(PREAMBLE)
        (self.root / 'LICENSE').write_text(PROJECT_LICENSE)

    def write(self, mutation=''):
        self.fixture.write(self.dmg, self.root, GENERATOR, PREAMBLE, PROJECT_LICENSE, mutation)

    def validate(self):
        return self.validator.validate_artifact(self.dmg, profile='macos-dmg', preamble=PREAMBLE,
                                                project_license=PROJECT_LICENSE)

    def cli(self):
        return run([sys.executable, '-I', str(VALIDATOR), '--artifact', str(self.dmg), '--profile', 'macos-dmg',
                    '--preamble', str(self.root / 'credits.md'),
                    '--project-license', str(self.root / 'LICENSE')], self.root)

    def test_generator_shaped_dmg_inventory_is_accepted(self):
        self.write()
        model = self.validate()
        self.assertEqual([p['name'] for p in model['payloads']], [
            'telegram-mcp-arm64', 'telegram-mcp-bridge-arm64', 'telegram-mcp-bridge-x86_64', 'telegram-mcp-x86_64'])
        self.assertEqual(len(model['modules']), 2)
        self.assertEqual(model['stdlib']['version'], 'go1.26.5')
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('4 Go payloads, 2 modules, Go standard library go1.26.5', result.stdout)

    def test_syntactically_present_but_semantically_invalid_inventories_are_refused(self):
        for mutation, (_, _, reason) in self.fixture.MUTATIONS.items():
            with self.subTest(mutation=mutation):
                self.write(mutation)
                # Files exist, are non-empty, and SBOMs remain parseable JSON.
                for suffix in ('.cdx.json', '.THIRD-PARTY-NOTICES.txt'):
                    self.assertTrue((self.root / (self.dmg.name + suffix)).read_bytes().strip())
                json.loads((self.root / (self.dmg.name + '.cdx.json')).read_text())
                with self.assertRaises(self.validator.InventoryInvalid) as refused:
                    self.validate()
                self.assertIn(reason, str(refused.exception))
                result = self.cli()
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn('Inventory refused for Telegram-MCP.dmg', result.stderr)
                self.assertIn(reason, result.stderr)

    def test_hash_only_inventory_that_passed_former_checks_is_refused(self):
        # The former import/inspection gate only checked non-empty bytes, root
        # name/hash and 'Go standard library'/'Copyright' notice substrings.
        self.write('hash-only')
        bom = json.loads((self.root / 'Telegram-MCP.dmg.cdx.json').read_text())
        notice = (self.root / 'Telegram-MCP.dmg.THIRD-PARTY-NOTICES.txt').read_text()
        self.assertEqual(bom['metadata']['component']['name'], 'Telegram-MCP.dmg')
        self.assertEqual(bom['metadata']['component']['hashes'], [
            {'alg': 'SHA-256', 'content': hashlib.sha256(self.dmg.read_bytes()).hexdigest()}])
        self.assertTrue('Go standard library' in notice and 'Copyright' in notice)
        with self.assertRaisesRegex(self.validator.InventoryInvalid, 'no components'):
            self.validate()

    def test_serialization_encoding_and_artifact_binding(self):
        self.write()
        sbom = self.root / 'Telegram-MCP.dmg.cdx.json'
        good = sbom.read_bytes()
        cases = {
            'noncanonical': json.dumps(json.loads(good)).encode(),
            'duplicate-key': good.replace(b'"bomFormat": "CycloneDX",',
                                          b'"bomFormat": "CycloneDX",\n  "bomFormat": "CycloneDX",', 1),
            'not-utf8': b'\xff' + good,
            'not-object': b'[]\n',
        }
        for name, data in cases.items():
            with self.subTest(case=name):
                sbom.write_bytes(data)
                with self.assertRaises(self.validator.InventoryInvalid):
                    self.validate()
        sbom.write_bytes(good)
        self.validate()
        self.dmg.write_bytes(self.dmg.read_bytes() + b'appended after inventory')
        with self.assertRaisesRegex(self.validator.InventoryInvalid, 'exact artifact bytes'):
            self.validate()

    def test_missing_or_symlinked_inventory_files_are_refused(self):
        self.write()
        notices = self.root / 'Telegram-MCP.dmg.THIRD-PARTY-NOTICES.txt'
        target = self.root / 'elsewhere.txt'
        notices.rename(target)
        with self.assertRaisesRegex(self.validator.InventoryInvalid, 'missing or unsafe'):
            self.validate()
        notices.symlink_to(target)
        with self.assertRaisesRegex(self.validator.InventoryInvalid, 'missing or unsafe'):
            self.validate()


class MacImportTests(ReleaseFixture):
    """Exercise actual release entry points with explicit compiler/OS doubles."""

    def setUp(self):
        super().setUp()
        scripts = self.repo / 'scripts'
        scripts.mkdir()
        for path in (ROOT / 'scripts').glob('release*.sh'):
            shutil.copy2(path, scripts / path.name)
        shutil.copy2(VALIDATOR, scripts / VALIDATOR.name)
        (self.repo / 'THIRD_PARTY_NOTICES.md').write_text(PREAMBLE)
        (self.repo / 'LICENSE').write_text(PROJECT_LICENSE)
        (self.root / 'fixture_inventory.py').write_text(FIXTURE_INVENTORY)
        self.env.update(FIXTURE_INVENTORY=str(self.root / 'fixture_inventory.py'),
                        FIXTURE_GENERATOR=str(GENERATOR))
        (scripts / 'build-windows.sh').write_text(
            '#!/bin/sh\nmkdir -p build/bin\nprintf windows > build/bin/Telegram-MCP-Setup.exe\n')
        (scripts / 'notices.sh').write_text('#!/bin/sh\nprintf notices\n')
        (scripts / 'build-inventory.py').write_text(f'#!{sys.executable}\n' + '''
import argparse, json
from pathlib import Path
parser = argparse.ArgumentParser()
parser.add_argument('--binary', action='append', required=True)
parser.add_argument('--artifact', action='append', required=True)
parser.add_argument('--artifact-only', action='store_true')
parser.add_argument('--output-dir', required=True)
parser.add_argument('--check', action='store_true')
args = parser.parse_args()
root = Path(args.output_dir)
for value in args.artifact:
    artifact = Path(value)
    outputs = {
        root / (artifact.name + '.cdx.json'): json.dumps({'bomFormat': 'CycloneDX', 'artifact': artifact.name}) + '\\n',
        root / (artifact.name + '.THIRD-PARTY-NOTICES.txt'): 'fixture Go standard library notice\\n',
    }
    for path, content in outputs.items():
        if args.check:
            if not path.is_file() or path.read_text() != content:
                raise SystemExit('stale fixture inventory')
        else:
            path.write_text(content)
''')
        (scripts / 'build-macos.sh').write_text(f'#!{sys.executable}\n' + '''
import importlib.util, json, os, pathlib, sys
root = pathlib.Path('build/bin')
root.mkdir(parents=True, exist_ok=True)
name = 'Telegram-MCP-unsigned.dmg' if '--unsigned' in sys.argv else 'Telegram-MCP.dmg'
(root / name).write_bytes(b'fixture UDIF payload' + b'koly' + bytes(508))
# Generator-shaped inventory of the snapshot's credits; optionally corrupted.
spec = importlib.util.spec_from_file_location('fixture_inventory', os.environ['FIXTURE_INVENTORY'])
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)
inventory.write(root / name, root, os.environ['FIXTURE_GENERATOR'],
                pathlib.Path('THIRD_PARTY_NOTICES.md').read_text(), pathlib.Path('LICENSE').read_text(),
                os.environ.get('FIXTURE_INVENTORY_MUTATION', ''))
(root / 'notarization.json').write_text(json.dumps({
    'status': 'Accepted', 'id': '11111111-2222-3333-4444-555555555555'}))
print('Fixture build only; no native Mac compilation')
''')
        self.tool('uname', '#!/bin/sh\nprintf "Darwin\\n"\n')
        for name, text in {
                'hdiutil': 'verified CRC32',
                'codesign': 'Authority=Developer ID Application: Fixture (ABCDEFGHIJ)\\nTeamIdentifier=ABCDEFGHIJ',
                'xcrun': 'The validate action worked!',
                'spctl': 'accepted\\nsource=Notarized Developer ID'}.items():
            self.tool(name, f'#!/bin/sh\n[ "${{FIXTURE_FAIL_GATE:-}}" != {name} ] || exit 45\n'
                      f'printf "{text}\\n"\n')
        for path in scripts.iterdir():
            path.chmod(0o755)
        self.tool('docker', '#!/bin/sh\nprintf invoked > "$BUILD_PROBE"\nmkdir -p build/bin\n'
                  'touch build/bin/Telegram-MCP-fixture.AppImage build/bin/telegram-mcp_${VERSION}_fixture.deb\n')
        self.tool('go', f'#!{sys.executable}\nimport pathlib, sys\n'
                  'pathlib.Path(sys.argv[sys.argv.index("-o") + 1]).write_text("fixture executable")\n')
        run(['git', 'add', '.'], self.repo, check=True)
        run(['git', 'commit', '-qm', 'release fixture'], self.repo, check=True)
        self.ref = run(['git', 'rev-parse', 'HEAD'], self.repo, check=True).stdout.strip()
        run(['git', 'tag', 'v1.2.3'], self.repo, check=True)
        self.probe = self.root / 'build-invoked'
        self.env['BUILD_PROBE'] = str(self.probe)

    def tool(self, name, content):
        path = self.tools / name
        path.write_text(content)
        path.chmod(0o755)

    def release(self, artifact, output='combined'):
        return run(['bash', 'scripts/release.sh', '1.2.3', '--output',
                    str(self.root / output), '--mac-artifact', str(artifact)],
                   self.repo, env=self.env)

    def mac_release(self, output='mac', private=False, unsigned=False, **env):
        args = ['0.0.0', '--ref', self.ref] if private else ['1.2.3']
        return run(['bash', 'scripts/release-mac.sh', *args, '--output',
                    str(self.root / output), *(['--unsigned'] if unsigned else [])],
                   self.repo, env=dict(self.env, **env))

    def test_mac_bundle_manifest_and_combined_checksums(self):
        result = self.mac_release()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        bundle = self.root / 'mac'
        manifest = json.loads((bundle / 'mac-build.json').read_text())
        self.assertEqual(manifest['version'], '1.2.3')
        self.assertEqual(manifest['source_ref'], 'v1.2.3')
        self.assertEqual(manifest['source_commit'], self.ref)
        self.assertEqual(manifest['source_tree'], run(
            ['git', 'rev-parse', 'HEAD^{tree}'], self.repo, check=True).stdout.strip())
        self.assertIs(manifest['signature']['verified'], True)
        self.assertEqual(manifest['signature']['team_id'], 'ABCDEFGHIJ')
        self.assertEqual(manifest['notarization']['status'], 'Accepted')
        self.assertIs(manifest['notarization']['stapled'], True)
        self.assertEqual(manifest['artifact']['sha256'], hashlib.sha256(
            (bundle / 'Telegram-MCP.dmg').read_bytes()).hexdigest())
        result = self.release(bundle)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        combined = self.root / 'combined'
        for name in ('mac-build.json', 'notarization.json', 'Telegram-MCP.dmg',
                     *manifest['inventories'], *manifest['evidence']):
            self.assertEqual((combined / name).read_bytes(), (bundle / name).read_bytes())
        checksums = (combined / 'checksums.txt').read_text()
        for name in ('mac-build.json', 'notarization.json', 'Telegram-MCP.dmg',
                     *manifest['inventories'], *manifest['evidence']):
            digest = hashlib.sha256((combined / name).read_bytes()).hexdigest()
            self.assertIn(f'{digest}  {name}\n', checksums)
        self.assertNotIn('  checksums.txt\n', checksums)

    def test_reject_mismatched_incomplete_tampered_or_unverified_bundle_before_build(self):
        result = self.mac_release()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        original = self.root / 'mac'
        manifest = json.loads((original / 'mac-build.json').read_text())
        cases = {
            'version': lambda m: m.update(version='9.9.9'),
            'ref': lambda m: m.update(source_ref='v9.9.9'),
            'commit': lambda m: m.update(source_commit='f' * 40),
            'tree': lambda m: m.update(source_tree='f' * 40),
            'mode': lambda m: m.update(build_mode='private-test'),
            'digest': lambda m: m['artifact'].update(sha256='0' * 64),
            'unsigned': lambda m: m['signature'].update(verified=False),
            'string-verified': lambda m: m['signature'].update(verified='true'),
            'adhoc': lambda m: m['signature'].update(authority='adhoc'),
            'notary': lambda m: m['notarization'].update(status='Rejected'),
            'unstapled': lambda m: m['notarization'].update(stapled=False),
            'schema': lambda m: m.update(schema_version=999),
            'missing-field': lambda m: m.pop('source_ref'),
        }
        for name, mutate in cases.items():
            with self.subTest(case=name):
                bundle = self.root / name
                shutil.copytree(original, bundle)
                modified = json.loads(json.dumps(manifest))
                mutate(modified)
                (bundle / 'mac-build.json').write_text(json.dumps(modified))
                result = self.release(bundle, output=f'out-{name}')
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse(self.probe.exists(), 'rejected only after platform builds')
                self.assertFalse((self.root / f'out-{name}/Telegram-MCP.dmg').exists())
        for name in ('mac-build.json', *manifest['evidence'], 'Telegram-MCP.dmg'):
            with self.subTest(missing=name):
                bundle = self.root / f'missing-{name}'
                shutil.copytree(original, bundle)
                (bundle / name).unlink()
                result = self.release(bundle, output=f'out-missing-{name}')
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.probe.exists())
        for name in (*manifest['evidence'], 'Telegram-MCP.dmg'):
            with self.subTest(tampered=name):
                bundle = self.root / f'tampered-{name}'
                shutil.copytree(original, bundle)
                with (bundle / name).open('ab') as stream:
                    stream.write(b'tampered')
                result = self.release(bundle, output=f'out-tampered-{name}')
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.probe.exists())
        bundle = self.root / 'plain-text-bundle'
        shutil.copytree(original, bundle)
        (bundle / 'Telegram-MCP.dmg').write_bytes(b'plain text')
        manifest['artifact']['sha256'] = hashlib.sha256(b'plain text').hexdigest()
        (bundle / 'mac-build.json').write_text(json.dumps(manifest))
        self.assertNotEqual(self.release(bundle).returncode, 0)
        self.assertFalse(self.probe.exists())

    def test_combined_release_validates_imported_dmg_inventory_semantics(self):
        result = self.mac_release()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        result = self.release(self.root / 'mac')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Inventory verified: Telegram-MCP.dmg (4 Go payloads, 2 modules', result.stdout)
        # The gate runs right after import and before any platform build.
        self.assertLess(result.stdout.index('Inventory verified'), result.stdout.index('==> Windows'))

    def test_signed_bundle_with_semantically_invalid_inventory_is_refused_before_build(self):
        fixture = load_module('fixture_inventory', self.root / 'fixture_inventory.py')
        for mutation in ('hash-only', 'legacy-placeholder', 'wrong-root-hash', 'no-stdlib',
                         'stdlib-only', 'missing-slice', 'notice-stub', 'notice-missing-module-text',
                         'notice-wrong-credits'):
            with self.subTest(mutation=mutation):
                bundle = self.root / f'mac-{mutation}'
                result = self.mac_release(output=bundle.name, FIXTURE_INVENTORY_MUTATION=mutation)
                # Non-empty, digest-bound and signed: provenance alone accepts it.
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                manifest = json.loads((bundle / 'mac-build.json').read_text())
                for name, digest in manifest['inventories'].items():
                    self.assertEqual(hashlib.sha256((bundle / name).read_bytes()).hexdigest(), digest)
                out = self.root / f'out-{mutation}'
                result = self.release(bundle, output=out.name)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn('Inventory refused for Telegram-MCP.dmg', result.stderr)
                self.assertIn(fixture.MUTATIONS[mutation][2], result.stderr)
                self.assertIn('removed imported Mac files', result.stderr)
                self.assertFalse(self.probe.exists(), 'rejected only after platform builds')
                self.assertEqual(sorted(p.name for p in out.iterdir()), ['build-source.json'])

    def test_failed_native_verification_never_generates_manifest(self):
        for gate in ('hdiutil', 'codesign', 'xcrun', 'spctl'):
            with self.subTest(gate=gate):
                result = self.mac_release(output=gate, FIXTURE_FAIL_GATE=gate)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.root / gate / 'mac-build.json').exists())
                self.assertFalse((self.root / gate / 'Telegram-MCP.dmg').exists())

    def test_unsigned_private_bundle_is_explicit_and_not_importable(self):
        result = self.mac_release(private=True, unsigned=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        bundle = self.root / 'mac'
        manifest = json.loads((bundle / 'mac-build.json').read_text())
        self.assertEqual(manifest['version'], '0.0.0')
        self.assertEqual(manifest['source_ref'], self.ref)
        self.assertEqual(manifest['artifact']['name'], 'Telegram-MCP-unsigned.dmg')
        self.assertIs(manifest['signature']['verified'], False)
        result = run(['bash', 'scripts/release.sh', '0.0.0', '--ref', self.ref,
                      '--output', str(self.root / 'combined'), '--mac-artifact', str(bundle)],
                     self.repo, env=self.env)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.probe.exists())

    def test_plain_text_dmg_is_not_release_provenance(self):
        artifact = self.root / 'Telegram-MCP.dmg'
        artifact.write_text('not a disk image, signed package, or selected source\n')
        result = self.release(artifact)
        self.assertNotEqual(result.returncode, 0,
                            f'unqualified plain text import succeeded:\n{result.stdout}')
        self.assertFalse((self.root / 'combined/Telegram-MCP.dmg').exists())


if __name__ == '__main__':
    unittest.main(verbosity=2)
