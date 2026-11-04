#!/usr/bin/env python3
"""Disposable loopback SOCKS5 test proxy; Ctrl-C drops every PoC connection.

No authentication, logging, metrics, or external listener. Use only for the
explicitly authorized real-phase network interruption check, never as a service.
"""
import asyncio
import ipaddress
import struct


async def pipe(reader, writer):
    while chunk := await reader.read(65536):
        writer.write(chunk)
        await writer.drain()


async def client(reader, writer):
    upstream = None
    try:
        version, count = await reader.readexactly(2)
        methods = await reader.readexactly(count)
        if version != 5 or 0 not in methods:
            return
        writer.write(b"\x05\x00")
        await writer.drain()
        version, command, reserved, kind = await reader.readexactly(4)
        if version != 5 or command != 1 or reserved != 0:
            return
        if kind == 1:
            host = str(ipaddress.ip_address(await reader.readexactly(4)))
        elif kind == 4:
            host = str(ipaddress.ip_address(await reader.readexactly(16)))
        elif kind == 3:
            size = (await reader.readexactly(1))[0]
            host = (await reader.readexactly(size)).decode("ascii")
        else:
            return
        port = struct.unpack("!H", await reader.readexactly(2))[0]
        remote, upstream = await asyncio.open_connection(host, port)
        writer.write(b"\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00")
        await writer.drain()
        tasks = [asyncio.create_task(pipe(reader, upstream)),
                 asyncio.create_task(pipe(remote, writer))]
        try:
            await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
        finally:
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
    except (OSError, UnicodeError, asyncio.IncompleteReadError):
        pass
    finally:
        writer.close()
        if upstream:
            upstream.close()


async def main():
    async with await asyncio.start_server(client, "127.0.0.1", 47899) as server:
        await server.serve_forever()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass
