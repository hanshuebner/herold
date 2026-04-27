package webspa

// Phase 1 of the merge plan
// (docs/design/server/notes/plan-tabard-merge-and-admin-rewrite.md)
// only ships the suite handler in this package; the admin Svelte
// app does not exist yet and the working operator UI remains
// internal/protoui at /ui/. Phase 2 lands the Svelte admin SPA at
// web/apps/admin/, fills in dist/admin/ via scripts/build-web.sh,
// and exposes a NewAdmin(...) constructor here that mounts the SPA
// at /admin/ on the admin listener.
//
// The Phase-1 placeholder index.html at dist/admin/index.html is
// what the Phase-2 mount will replace; until then the admin
// listener does not mount this package.
//
// adminEmbeddedFS / adminEmbeddedFS-stub already exist in
// embed_default.go / embed_stub.go so the build-tag split surface
// is in place when Phase 2 wires up NewAdmin. Keeping the function
// reachable but unused at this stage costs nothing (Go's compiler
// does not complain about unused package-level functions) and lets
// Phase 2 land a single small constructor without re-introducing
// the build-tag scaffolding.
