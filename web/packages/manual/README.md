# @herold/manual

Herold's in-app and standalone manual viewer. Bundles the Markdoc
`.mdoc` source into JSON and renders it via Svelte 5 components.

The manual *content* -- the `.mdoc` chapter files and `manifest.toml`
-- lives in `docs/manual/`, not in this package. This package is the
tooling and the renderer.

## Editing the manual

Run the standalone dev server and edit the `.mdoc` files under
`docs/manual/`:

```bash
make manual                          # from the repo root
# or:
pnpm --filter @herold/manual dev
```

This builds the manual to standalone SSR HTML, serves it at
**http://localhost:8000/**, and watches `docs/manual/` -- every save
to a `.mdoc` file (or `manifest.toml`) rebuilds and live-reloads any
open browser tab. It is pure Node: no Vite, no SPA build, no herold
binary. The only prerequisite is a one-time `pnpm -C web install` so
the Markdoc dependency is present.

- `MANUAL_PORT=9000 make manual` changes the port.
- Build output goes to `dist-dev/` (gitignored).
- A broken `.mdoc` fails the rebuild non-fatally: the error prints to
  the terminal and the last good build keeps serving until the next
  successful save.

## Scripts

| Command | What it does |
|---------|--------------|
| `pnpm dev` | Standalone dev server with live reload (see above). |
| `pnpm validate` | Parse every `.mdoc` against the Markdoc schema; non-zero exit on any error. Run by CI. Needs `--content-root ../../../docs/manual`. |
| `pnpm bundle` | Bundle the `.mdoc` source into `{user,admin}.json` and, with `--ssr`, per-chapter static HTML. The production build step. |
| `pnpm test` | Vitest unit tests for the renderer components. |

`validate` and `bundle` take `--manifest` / `--content-root` flags;
see the headers of `scripts/validate.mjs` and `scripts/bundle.mjs`.

## How it ships

`scripts/build-web.sh` (invoked by `make build-web`) runs the bundler
twice: once to emit the JSON consumed by the in-SPA manual viewer,
and once in `--ssr` mode to emit the per-chapter static HTML installed
into `internal/webspa/dist/manual/`. A running herold then serves the
standalone manual at `/manual/` on the public listener and
`/admin/manual/` on the admin listener. The `make manual` dev server
above renders the same SSR output, just without the SPA build or the
herold binary in the loop.
