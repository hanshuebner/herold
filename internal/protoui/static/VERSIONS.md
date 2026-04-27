# Vendored UI library versions

These libraries are checked in verbatim so the Herold binary self-contains
the entire UI surface. No CDN. No build pipeline. Per docs/design/server/notes/
open-questions.md R35 the JS budget is < 30 KB; the actual measured load
is documented in the surrounding ticket.

## htmx.min.js

- Library: HTMX
- Pinned version: 1.9.12
- Source: https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js
- Minified size: ~16 KB

## alpine.min.js

- Library: Alpine.js
- Pinned version: 3.13.10
- Source: https://unpkg.com/alpinejs@3.13.10/dist/cdn.min.js
- Minified size: ~14 KB
- Used only where a small client-side reactive bit (e.g. show/hide a
  confirm panel) is awkward in pure HTMX.

## ui.css

- Hand-rolled, ~3 KB. Sticks to a single colour palette so the
  rendered HTML stays printable in operator postmortems.

## Refreshing

To bump versions:

1. Download the new minified file at the URLs above.
2. Replace the file in this directory.
3. Update the version number in this file.
4. Run `go test ./internal/protoui/...` — the integration tests verify
   the asset is served at the expected path, not its byte-for-byte
   contents.
