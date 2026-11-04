#!/usr/bin/env python3
"""Extract a type-2 ELF64 AppImage without executing a foreign-architecture runtime.

binfmt/QEMU's standard ELF magic does not match AppImage's AI bytes at offset 8.
The SquashFS follows the ELF section table; validate its magic before extraction.
"""
from pathlib import Path
import struct
import subprocess
import sys


def extract(image, destination):
    image = Path(image).resolve()
    destination = Path(destination).resolve()
    if destination.exists():
        raise ValueError(f'Refusing to overwrite extraction directory: {destination}')
    with image.open('rb') as stream:
        header = stream.read(64)
        if len(header) != 64 or header[:6] != b'\x7fELF\x02\x01' or header[8:11] != b'AI\x02':
            raise ValueError(f'Not a little-endian ELF64 type-2 AppImage: {image}')
        section_start = struct.unpack_from('<Q', header, 40)[0]
        section_size, section_count = struct.unpack_from('<HH', header, 58)
        offset = section_start + section_size * section_count
        if section_size != 64 or section_count == 0 or offset < 64:
            raise ValueError('Unsupported AppImage ELF section table')
        stream.seek(offset)
        if stream.read(4) != b'hsqs':
            raise ValueError('SquashFS magic missing after AppImage ELF section table')
    subprocess.run(['unsquashfs', '-processors', '2', '-no-progress', '-o', str(offset),
                    '-d', str(destination), str(image)], check=True, stdout=subprocess.DEVNULL)


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('Usage: extract-appimage.py IMAGE DESTINATION')
    extract(sys.argv[1], sys.argv[2])
