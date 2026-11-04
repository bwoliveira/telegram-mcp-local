# Third-party notices — S1 engine (PoC)

No code from the comparison project is incorporated. The local adapter was
written for this fork; the S0 probe was reused as project code. The protocol,
SRP, manager, MTProto pagination and middleware come from dependencies and are
not our implementation. Bruno Orlandi's upstream rights remain intact.

## gotd/td v0.163.0 — MIT

Source: <https://github.com/gotd/td/tree/v0.163.0>.

```text
MIT License

Copyright (c) 2020 Aleksandr Razumov

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## gotd/contrib v0.25.0 — MIT

Source: <https://github.com/gotd/contrib/tree/v0.25.0>.

```text
MIT License

Copyright (c) 2020 tdakkota

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

## Other direct dependencies used by the PoC

- `golang.org/x/net` (SOCKS5), `x/sys` (terminal), `x/term` (terminal detection),
  `x/time` (rate limiting): BSD-3-Clause, Go Authors, licenses in the modules.
- `modernc.org/sqlite v1.60.1`, already inherited: BSD-3-Clause, Copyright (c) 2017
  The Sqlite Authors. Translated SQLite and other dependencies retain their notices.
- `github.com/skip2/go-qrcode`, already inherited: MIT, Copyright (c) 2014 Tom Harwood.

No GPL/AGPL code was copied. This is not a distribution inventory: before
packaging/distribution, generate notices for the transitive dependencies actually
linked and review their licenses. Stage 1 does not publish binaries or a release.
The coordinator must link this document to the root inventory on integration,
without removing existing notices; see `docs/contratos/PEDIDOS-S1.md`.
