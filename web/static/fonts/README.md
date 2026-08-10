# Vendored fonts

**These are vendored binary artifacts. Do not edit them.** To change or update
them, re-run the curl commands below.

## Why they are here

The portal serves a Content-Security-Policy of `style-src 'self'` with no
external origins, and the project brief forbids third-party frontend assets and
CDNs. The webfont therefore has to be self-hosted out of this directory rather
than loaded from a font CDN at runtime.

## What this is

[Maple Mono](https://github.com/subframe7536/maple-font) by subframe7536, a
monospace typeface. The files here are the **latin** subset, **normal** (upright)
style, in three weights.

Packaged by [Fontsource](https://fontsource.org/fonts/maple-mono) as
`@fontsource/maple-mono@5.3.0`, which wraps upstream Maple Mono `v7.8`.

| File | Weight | Format | Size |
| --- | --- | --- | --- |
| `maple-mono-400.woff2` | 400 (regular) | WOFF2 | 74,088 bytes |
| `maple-mono-500.woff2` | 500 (medium) | WOFF2 | 76,708 bytes |
| `maple-mono-600.woff2` | 600 (semibold) | WOFF2 | 76,368 bytes |

The `@font-face` declarations that reference these files live in
`web/static/app.css`, not here.

## Where each file came from

Downloaded from the Fontsource CDN on jsDelivr:

- `maple-mono-400.woff2` <- <https://cdn.jsdelivr.net/fontsource/fonts/maple-mono@5.3.0/latin-400-normal.woff2>
- `maple-mono-500.woff2` <- <https://cdn.jsdelivr.net/fontsource/fonts/maple-mono@5.3.0/latin-500-normal.woff2>
- `maple-mono-600.woff2` <- <https://cdn.jsdelivr.net/fontsource/fonts/maple-mono@5.3.0/latin-600-normal.woff2>

## License

SIL Open Font License 1.1 (`OFL-1.1`). The full text is in
[`LICENSE-maple-mono.txt`](./LICENSE-maple-mono.txt), copied verbatim from
<https://cdn.jsdelivr.net/npm/@fontsource/maple-mono@5.3.0/LICENSE>.

Copyright (c) 2022, subframe7536, with Reserved Font Name Maple Mono.

The OFL permits redistributing these files bundled with this software, including
in a public repository, provided the license text travels with them — hence
`LICENSE-maple-mono.txt` sitting next to the fonts. Note the Reserved Font Name:
a modified version of the font may not be distributed under the name
"Maple Mono".

## Refreshing these files

Run from this directory (`web/static/fonts/`):

```sh
curl -fsSL -o maple-mono-400.woff2 https://cdn.jsdelivr.net/fontsource/fonts/maple-mono@5.3.0/latin-400-normal.woff2
curl -fsSL -o maple-mono-500.woff2 https://cdn.jsdelivr.net/fontsource/fonts/maple-mono@5.3.0/latin-500-normal.woff2
curl -fsSL -o maple-mono-600.woff2 https://cdn.jsdelivr.net/fontsource/fonts/maple-mono@5.3.0/latin-600-normal.woff2
curl -fsSL -o LICENSE-maple-mono.txt https://cdn.jsdelivr.net/npm/@fontsource/maple-mono@5.3.0/LICENSE
```

To bump the version, change `5.3.0` in every URL above and in this file. Other
weights (100-800) and an italic style are available from the same package using
the `latin-<weight>-<style>.woff2` naming.

Sanity-check a download before committing it — a WOFF2 file starts with the
magic bytes `wOF2`, and a CDN error page will not:

```sh
file maple-mono-*.woff2   # expect: Web Open Font Format (Version 2)
```
