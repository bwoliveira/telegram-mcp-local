#!/usr/bin/env python3
"""Inspect actual release artifacts without launching the GUI or Telegram.

Requires file, dpkg-deb, 7z and unsquashfs; no foreign binaries are executed.
Usage: python3 scripts/test-artifacts.py OUTPUT_DIRECTORY
"""
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import zipfile

_spec = importlib.util.spec_from_file_location(
    'validate_inventory', Path(__file__).with_name('validate-inventory.py'))
assert _spec and _spec.loader
validate_inventory = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(validate_inventory)


def command(*args, cwd=None):
    return subprocess.run(args, cwd=cwd, check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout


def binary(path, architecture):
    description = command('file', '-b', str(path)).strip()
    assert architecture in description, (path, description, architecture)
    return description


def notices(path):
    text = path.read_text()
    for credit in ('Bruno Orlandi', 'Wails', 'modernc.org/sqlite', 'whisper.cpp', 'FFmpeg'):
        assert credit in text, (path, credit)


def main(root):
    root = root.resolve()
    provenance = json.loads((root / 'build-source.json').read_text())
    version = provenance['version']
    expected = {'Telegram-MCP-Setup.exe'}
    expected.update(f'Telegram-MCP-{arch}.AppImage' for arch in ('x86_64', 'aarch64'))
    expected.update(f'telegram-mcp_{version}_{arch}.deb' for arch in ('amd64', 'arm64'))
    targets = [('darwin', 'arm64', 'arm64'), ('darwin', 'amd64', 'x86_64'),
               ('linux', 'arm64', 'ARM aarch64'), ('linux', 'amd64', 'x86-64'),
               ('windows', 'amd64', 'x86-64')]
    for os, arch, _ in targets:
        expected.add(f'telegram-mcp_{os}_{arch}.' + ('zip' if os == 'windows' else 'tar.gz'))
    present = {p.name for p in root.iterdir()}
    assert expected <= present, expected - present
    inventories = dict.fromkeys(expected)
    # Linux packages must carry the file-by-file native payload inventory.
    for name in expected:
        if name.endswith('.AppImage'):
            inventories[name] = 'linux-appimage'
        elif name.endswith('.deb'):
            inventories[name] = 'linux-deb'
    if 'Telegram-MCP.dmg' in present:
        inventories['Telegram-MCP.dmg'] = 'macos-dmg'
    for artifact, profile in sorted(inventories.items()):
        required = {artifact + '.cdx.json', artifact + '.THIRD-PARTY-NOTICES.txt'}
        assert required <= present, (artifact, required - present)
        goos = ('darwin' if artifact.endswith('.dmg') or '_darwin_' in artifact else
                'windows' if artifact.endswith(('.exe', '.zip')) else 'linux')
        # Same semantic gate release.sh applies to the imported DMG: canonical
        # CycloneDX 1.6, exact artifact SHA-256, Go stdlib/module graph, notices.
        try:
            validate_inventory.validate_artifact(root / artifact, profile=profile, goos=goos)
        except validate_inventory.InventoryInvalid as error:
            raise AssertionError(f'{artifact}: {error}') from error
    sums = {}
    for line in (root / 'checksums.txt').read_text().splitlines():
        digest, name = line.split('  ')
        assert name not in sums and name != 'checksums.txt'
        assert Path(name).name == name
        with (root / name).open('rb') as stream:
            assert hashlib.file_digest(stream, 'sha256').hexdigest() == digest, name
        sums[name] = digest
    assert set(sums) == {p.name for p in root.iterdir() if p.is_file() and p.name != 'checksums.txt'}
    reports = []
    with tempfile.TemporaryDirectory(prefix='telegram-artifact-test-') as tmp:
        tmp = Path(tmp)
        for os, arch, machine in targets:
            name = f'telegram-mcp_{os}_{arch}.' + ('zip' if os == 'windows' else 'tar.gz')
            stage = tmp / f'{os}-{arch}'
            stage.mkdir()
            if os == 'windows':
                with zipfile.ZipFile(root / name) as archive:
                    assert set(archive.namelist()) == {'telegram-mcp.exe', 'THIRD-PARTY-NOTICES.txt'}
                    archive.extractall(stage)
            else:
                with tarfile.open(root / name) as archive:
                    assert set(archive.getnames()) == {'telegram-mcp', 'THIRD-PARTY-NOTICES.txt'}
                    archive.extractall(stage, filter='data')
            exe = stage / ('telegram-mcp.exe' if os == 'windows' else 'telegram-mcp')
            reports.append({'artifact': name, 'binary': binary(exe, machine)})
            notices(stage / 'THIRD-PARTY-NOTICES.txt')
        for arch, machine in [('amd64', 'x86-64'), ('arm64', 'ARM aarch64')]:
            name = f'telegram-mcp_{version}_{arch}.deb'
            assert command('dpkg-deb', '-f', str(root / name), 'Architecture').strip() == arch
            assert command('dpkg-deb', '-f', str(root / name), 'Version').strip() == version
            stage = tmp / f'deb-{arch}'
            command('dpkg-deb', '-x', str(root / name), str(stage))
            notices(stage / 'usr/share/doc/telegram-mcp/THIRD-PARTY-NOTICES.txt')
            for exe in ('telegram-mcp', 'telegram-mcp-bridge'):
                reports.append({'artifact': name, 'binary': binary(stage / 'opt/telegram-mcp' / exe, machine)})
        for arch, machine in [('x86_64', 'x86-64'), ('aarch64', 'ARM aarch64')]:
            name = f'Telegram-MCP-{arch}.AppImage'
            stage = tmp / f'appimage-{arch}'
            stage.mkdir()
            command(sys.executable, str(Path(__file__).with_name('extract-appimage.py')),
                    str(root / name), str(stage / 'squashfs-root'))
            appdir = stage / 'squashfs-root'
            notices(appdir / 'usr/bin/THIRD-PARTY-NOTICES.txt')
            # The embedded native inventory and written offer travel with the AppImage.
            embedded = appdir / 'usr/share/doc/telegram-mcp'
            manifest = json.loads((embedded / 'native-payload.json').read_text())
            assert manifest['profile'] == 'linux-appimage' and manifest['files'], name
            assert 'Written offer' in (embedded / 'NATIVE-THIRD-PARTY-NOTICES.txt').read_text(), name
            # The GTK plugin installs a shell launcher around AppImageKit's ELF.
            wrapped = appdir / 'AppRun.wrapped'
            if wrapped.exists():
                assert 'AppRun.wrapped' in (appdir / 'AppRun').read_text()
                binary(wrapped, machine)
            else:
                binary(appdir / 'AppRun', machine)
            for exe in ('telegram-mcp', 'telegram-mcp-bridge'):
                reports.append({'artifact': name, 'binary': binary(appdir / 'usr/bin' / exe, machine)})
            for helper in ('WebKitWebProcess', 'WebKitNetworkProcess'):
                paths = list(appdir.rglob(helper))
                assert paths, (name, helper)
                binary(paths[0], machine)
        stage = tmp / 'windows-installer'
        stage.mkdir()
        command('7z', 'x', '-y', f'-o{stage}', str(root / 'Telegram-MCP-Setup.exe'))
        notices(stage / 'THIRD-PARTY-NOTICES.txt')
        for exe in ('telegram-mcp.exe', 'telegram-mcp-bridge.exe'):
            reports.append({'artifact': 'Telegram-MCP-Setup.exe', 'binary': binary(stage / exe, 'x86-64')})
    print(json.dumps({'source': provenance, 'artifacts': [
        {'name': name, 'bytes': (root / name).stat().st_size, 'sha256': sums[name]} for name in sorted(expected)
    ], 'binary_checks': reports, 'checksums_verified': len(sums)}, indent=2))


if __name__ == '__main__':
    main(Path(sys.argv[1]))
