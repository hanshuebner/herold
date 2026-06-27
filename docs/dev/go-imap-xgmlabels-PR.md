# Upstream PR: add X-GM-LABELS (X-GM-EXT-1) FETCH support to imapclient

**Target:** `github.com/emersion/go-imap` (v2 / `imapclient`)
**Patch:** `go-imap-xgmlabels-upstream.patch` (git am-able; generated from the
original `github.com/emersion/go-imap/v2` module path, before the herold fork
rename).

## Title

imapclient: add X-GM-LABELS (X-GM-EXT-1) FETCH support

## Description

Gmail's IMAP server advertises the `X-GM-EXT-1` capability and exposes each
message's set of labels through the `X-GM-LABELS` FETCH data item
(<https://developers.google.com/gmail/imap/imap-extensions#access_to_gmail_labels_x-gm-labels>).
Today the client cannot fetch it: `handleFetch` reaches its `default:` branch and
returns `unsupported msg-att name: "X-GM-LABELS"`, which aborts the whole FETCH
response — so the item is unusable even via the unilateral/raw paths.

This change adds first-class, opt-in support, mirroring how `MODSEQ` (another
extension-gated item) is wired:

- **Request:** `imap.FetchOptions.GmailLabels bool` (documented "requires the
  X-GM-EXT-1 extension"). `writeFetchItems` emits the `X-GM-LABELS` atom when set.
- **Parse:** a `case "X-GM-LABELS":` in `handleFetch` reads the parenthesised
  label list. System labels are flag-shaped (a backslash followed by an atom,
  e.g. `\Inbox`, `\Sent`, `\Important`, `\Starred`, `\Trash`, `\Spam`, `\Draft`)
  and are preserved with their leading backslash; user labels are astrings
  (quoted or atom, modified-UTF-7 when non-ASCII) and are returned verbatim. An
  empty list `()` is valid. Result is exposed as `FetchItemDataGmailLabels{Labels
  []string}`.
- **Collect:** `FetchMessageBuffer.GmailLabels []string`, populated by
  `populateItemData`.

The item is only emitted when the caller opts in via `FetchOptions.GmailLabels`,
so non-Gmail servers are unaffected. The server side is untouched.

## Tests

`imapclient/fetch_gmail_test.go` drives the client against a scripted raw IMAP
server (the in-tree `imapmemserver` does not implement the Gmail extension) and
asserts a round-trip over a mixed label set: two system labels plus two quoted
user labels, one containing a space and one containing a slash. `go test ./...`
and `go vet ./...` pass (the pre-existing unkeyed-struct-literal vet notices in
`imapserver` are unrelated to this change).

## Notes

- Scope is intentionally limited to `X-GM-LABELS`, the load-bearing item.
  `X-GM-MSGID` / `X-GM-THRID` could be added the same way if desired, but are
  left out to keep the surface minimal.
- Wire format reference: RFC 3501 fetch syntax plus Gmail's extension doc; the
  label-list reader tolerates a stray leading space inside the list, matching the
  existing leniency in `internal.ExpectFlagList`.
