# Font sources

Both families are licensed under the SIL Open Font License 1.1 with no Reserved Font Name.
The license texts ship next to the fonts (`OFL-AtkinsonHyperlegibleNext.txt`,
`OFL-AtkinsonHyperlegibleMono.txt`) and are copied, fingerprinted, into `dist/assets/fonts/`.

- Copyright 2020-2024 The Atkinson Hyperlegible Next Project Authors
  (https://github.com/googlefonts/atkinson-hyperlegible-next)
- Copyright 2020-2024 The Atkinson Hyperlegible Mono Project Authors
  (https://github.com/googlefonts/atkinson-hyperlegible-next-mono)

## Download (02/11/2026)

From `google/fonts` at commit `6c1dbe27dcf7d817d4b62cec033ba77fa8e2a5ec`, into a new, empty
scratch directory:

| File in `google/fonts` | SHA-256 |
|---|---|
| `ofl/atkinsonhyperlegiblenext/AtkinsonHyperlegibleNext[wght].ttf` | `5a455d1cfa099b601ab70751bb9673e8fe1854dc4500c80e1a220d0d75e31745` |
| `ofl/atkinsonhyperlegiblenext/OFL.txt` | `aca6a428580965d2297d1b718042dd427c2a9443ece3b0d02d758e161e0c4030` |
| `ofl/atkinsonhyperlegiblemono/AtkinsonHyperlegibleMono[wght].ttf` | `5ce8b1698d1ded7dff2178c1a3ad159470085a58ea239e8b2cb88f4fb4a6f646` |
| `ofl/atkinsonhyperlegiblemono/OFL.txt` | `1ebb31cf7393164f20d10c1d48406cddb5314feff8465531cf1e4ba37e9dd740` |

`METADATA.pb` of both families lists `license: "OFL"`; neither `OFL.txt` names a Reserved Font
Name. Only the upright variable fonts (weight axis 200–800) are used; italics are not shipped.

## Subset

`fontTools` 4.60.1 with `brotli` 1.1.0, in a throwaway `uv` virtual environment outside the
repository (not a project dependency):

```sh
pyftsubset <ttf> --flavor=woff2 --layout-features='*' --name-IDs='*' \
  --unicodes=U+0000-00FF,U+0131,U+0152-0153,U+02C6,U+02DA,U+02DC,U+2000-206F,U+20AC,U+2122 \
  --output-file=<woff2>
```

`--name-IDs='*'` keeps the copyright (name ID 0) and license (name IDs 13/14) fields; they were
read back from the output files with `fontTools.ttLib`.

| Output | Bytes | SHA-256 |
|---|---:|---|
| `atkinson-hyperlegible-next.woff2` | 32,740 | `39821cc64b5e43745f058f95dd95d9733afb8c5de005cdd190fdf67ea4976742` |
| `atkinson-hyperlegible-mono.woff2` | 17,028 | `4246d448c7720620693e64dfaff5a335c2599ebde0d5cae23d44b4be0915ee79` |

Total 49,768 bytes, inside the 150 KB budget of `site/DESIGN.md` §2.3.
