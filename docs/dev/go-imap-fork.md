# go-imap in-tree fork

herold vendors `github.com/emersion/go-imap/v2` as a permanent in-tree fork
under `third_party/go-imap`, mapped via a `replace` directive in `go.mod`.

## Fork base

The fork branches from upstream tag **v2.0.0-beta.8**
(commit `7ac47a9cfd9a06ff5f3bdee52d7420137f023a1e`), recorded in
`third_party/go-imap/UPSTREAM`.

## What herold adds

The fork adds X-GM-LABELS (X-GM-EXT-1) client FETCH support
(`imapclient/fetch.go`, `fetch.go`) so `internal/imapimport` can retrieve
Gmail labels during import (REQ-IMAP-IMP-53). Upstream declined this change
as out-of-scope for proprietary Gmail extensions.

The divergence from the base is captured as a git-am-able patch in
`docs/dev/go-imap-xgmlabels-upstream.patch`.

## Runtime vs. test-fixture scope

- **Runtime (client-only):** `imapclient/` and the top-level parser files are
  used by `internal/imapimport`. herold's own IMAP server (`internal/protoimap`)
  does not use the go-imap server packages.
- **Test fixture:** `imapserver/` and `imapserver/imapmemserver/` provide the
  fake-server harness consumed by `_test.go` files in `internal/imapimport`.
  These packages are kept in the fork so those tests compile without a separate
  module.

The `cmd/` directory (an upstream standalone binary) was removed from the fork;
nothing in herold imports it.

## Harvesting upstream fixes

Run `make imap-upstream-diff` once or twice a year to review upstream commits
since the base and identify parser robustness or correctness fixes worth
cherry-picking. The target clones (or reuses a local cache of) the upstream
repository and prints both a one-line log and a restricted diff excluding
`cmd/`. Cherry-pick relevant hunks manually; re-run `go build ./...` and
`make vet` after each pick.

The harvest target is read-only and is not wired into `ci-local` or `all`.
