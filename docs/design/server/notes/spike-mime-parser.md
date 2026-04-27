# Spike: MIME parser — `jhillyerd/enmime` vs. stdlib-only

- **Date**: 2026-04-24
- **Go**: toolchain 1.23.5 on the host; module floor 1.23.
- **Parsers under test**:
  - `github.com/jhillyerd/enmime v1.3.0` — high-level `ReadEnvelope` API that returns decoded `Text`, `HTML`, `Attachments`, `Inlines`, `OtherParts`.
  - Stdlib-only: `net/mail` + `mime/multipart` + `mime.ParseMediaType` + `mime.WordDecoder`, with a hand-written recursive walker that classifies parts into text / html / attachment.
- **Harness**: `internal/mailparse/spike/spike_test.go`, build-tag gated (`//go:build spike`). Each `.eml` is fed to both parsers behind a `recover()` so a panic does not abort the run.
  - `go get github.com/jhillyerd/enmime@latest && go mod tidy`
  - `go test -tags spike -v ./internal/mailparse/spike/...`

## Corpus (22 messages under `internal/mailparse/testdata/spike/`)

| # | Kind                                       |
|--:|--------------------------------------------|
| 01 | plain us-ascii, single part                |
| 02 | multipart/alternative (text + html)        |
| 03 | multipart/mixed with PDF attachment        |
| 04 | nested mixed -> alternative + attachment   |
| 05 | message/rfc822 forwarded inside DSN-like   |
| 06 | quoted-printable body                      |
| 07 | base64 body                                |
| 08 | RFC 2047 encoded-word Subject              |
| 09 | SMTPUTF8 mailbox, UTF-8 subject and body   |
| 10 | 8BITMIME Latin-1 body                      |
| 11 | boundary-lookalike lines inside body       |
| 12 | missing Content-Type header                |
| 13 | malformed Content-Type (unterminated quote)|
| 14 | duplicate headers                          |
| 15 | 1500-char header line (>998 RFC limit)     |
| 16 | zero-length body                           |
| 17 | binary NUL bytes in attachment             |
| 18 | broken base64 (illegal chars, truncated)   |
| 19 | charset label says utf-8 but bytes ISO-8859-1 |
| 20 | multipart/related with inline PNG          |
| 21 | missing closing boundary marker            |
| 22 | mixed LF / CR / CRLF line endings          |

## Results

Status = `pass` (parsed with no error) / `soft-fail` (error returned but result still usable) / `hard-fail` (no usable result) / `panic`.

| Message                         | enmime v1.3.0 | stdlib-only |
|---------------------------------|---------------|-------------|
| 01 plain ascii                  | pass | pass |
| 02 multipart/alternative        | pass | pass |
| 03 mixed + PDF                  | pass | pass |
| 04 nested multipart             | pass | pass |
| 05 message/rfc822               | pass | pass (inner walked; inner treated as text, not as attachment) |
| 06 quoted-printable             | pass | pass |
| 07 base64 body                  | pass | pass |
| 08 RFC 2047 subject             | pass | pass |
| 09 SMTPUTF8                     | pass | pass |
| 10 8BITMIME Latin-1             | pass | pass |
| 11 boundary-false-match         | pass | pass |
| 12 missing Content-Type         | pass | pass (both default to text/plain us-ascii per RFC 2045) |
| 13 malformed Content-Type       | pass | pass (both fall back to text/plain — no error surfaced) |
| 14 duplicate headers            | pass | pass |
| 15 very long header line        | pass | pass |
| 16 zero-length body             | **hard-fail** (Text="" -> zero parts recorded) | pass |
| 17 binary NUL attachment        | pass | pass |
| 18 broken base64                | pass (silently best-effort decoded) | pass (stdlib does not decode attachment bodies here either) |
| 19 wrong charset label          | pass (no validation of bytes vs. label) | pass (same) |
| 20 multipart/related + inline   | pass (text + html + inline attachment) | pass (html + 1 attachment; no text part in corpus, correct) |
| 21 missing end boundary         | pass (recovered) | **soft-fail** (`multipart: NextPart: EOF`, but 2 parts collected) |
| 22 mixed line endings           | pass | pass |

CSV dump from the test run is reproducible via `go test -tags spike -v ./internal/mailparse/spike/...` and begins with a `CSV,...` header line.

## Feature gaps vs. what we need

- **SMTPUTF8 (REQ-PROTO-05)** — enmime preserves UTF-8 in addresses and headers; stdlib `net/mail.ParseAddress` is stricter and rejects some RFC 6531 non-ASCII local parts unless pre-normalized. If we go stdlib-only we need our own address parser for SMTPUTF8.
- **8BITMIME non-UTF-8 bodies (REQ-PROTO, RFC 6152)** — enmime runs charset detection via `gogs/chardet` and transcodes Latin-1 / Windows-1252 to UTF-8 on `env.Text`. Stdlib does not transcode; we must wrap `golang.org/x/text/encoding` ourselves. Important for FTS.
- **`message/rfc822` walk (REQ-STORE-60 FTS)** — enmime surfaces the inner message as `OtherParts` (treated as attachment in the harness); the stdlib walker recurses into it and exposes inner body text. Neither is outright wrong, but the semantics for FTS indexing differ: we need to decide whether inner message body is attachment-text or top-level-text for search. This is a policy question, not a parser gap.
- **RFC 2047 encoded-word edge cases** — both parsers handle well-formed `=?UTF-8?Q?...?=` correctly. Neither was stressed with malformed encoded-words or mixed charsets inside a single header; worth a follow-up fuzz target.
- **Charset mislabel (#19)** — both parsers trust the label. A correctness layer has to detect `utf-8` + invalid UTF-8 and fall back; this is ours to build regardless of parser choice.
- **Missing closing boundary (#21)** — stdlib returns `EOF` as an error from `NextPart`; enmime treats it as recoverable. For inbound SMTP DATA we want enmime's posture (accept and log), so our own wrapper would need to replicate it over the stdlib.
- **Nested structure depth** — enmime has no configurable depth cap; the harness capped the stdlib walker at 16. For abuse-resistance we need an explicit limit wherever we land.

## Error-handling posture

enmime is on the **tolerant end of the spectrum**, sometimes too tolerant. It silently best-effort-decodes broken base64 (#18) without surfacing the corruption, trusts mislabeled charsets (#19) without validation, and on the zero-length-body case (#16) returns an envelope with no text at all — the harness scored this `hard-fail` because zero recorded parts looks the same as a failed parse. For a mail server where "accept and log" is operationally correct, tolerance is a feature, but we need to add a validation pass on top (at minimum: UTF-8 well-formedness check against the declared charset, base64 structural check, boundary integrity check) so broken mail gets flagged for the admin surface rather than silently indexed wrong. Stdlib is less tolerant (surfaces the #21 EOF) but also does less work — the things it is strict about are not the things we need strictness on, and the things we need (charset validation, base64 integrity) it does not provide.

## Recommendation

**thin-stdlib-wrapper-and-extend** is tempting for dependency-budget reasons but underestimates how much glue we would write: charset detection + transcoding, attachment extraction, recursive walk with depth cap, tolerant boundary recovery, RFC 2047 across all the places it matters. That is several weeks of work that enmime already ships, and the bugs we would hit are the bugs enmime has already fixed.

**vendor-enmime-into-internal/third_party-from-day-one** is overkill at this stage; we have no evidence yet that upstream is hostile or unresponsive. Premature forking is a tax we pay forever.

**Chosen: start-with-enmime-and-fork-later.** Use `github.com/jhillyerd/enmime v1.3.0` behind a narrow `internal/mailparse` facade (`ParseMessage(ctx, io.Reader) (*Message, error)`) that returns our own types, not enmime's. Add a post-parse validation layer that enforces the strictness we care about (charset well-formedness, base64 structural check, depth cap, size cap, attachment-count cap). If correctness issues mount or upstream stalls, vendor into `internal/third_party/enmime/` per STANDARDS.md §3. The facade keeps the blast radius of a switch small: roughly one package's worth of changes, not the whole tree.

One direct dependency added (`jhillyerd/enmime`), which pulls six transitive deps (`gogs/chardet`, `cention-sany/utf7`, `jaytaylor/html2text`, `ssor/bom`, `pkg/errors`, `olekukonko/tablewriter`, `mattn/go-runewidth`, `rivo/uniseg`, `golang.org/x/net`). All MIT/BSD-compatible. Transitive count bears watching against the 50-direct-dep budget.
