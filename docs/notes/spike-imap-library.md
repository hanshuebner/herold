# Spike: IMAP library — fork `emersion/go-imap/v2` vs. write from scratch vs. middle path

- **Date**: 2026-04-24
- **Upstream**: `github.com/emersion/go-imap/v2`
- **Current version**: `v2.0.0-beta.8`, published 2025-12-16 (8th beta; v2 line open since 2023-03-17; still pre-1.0; API carries a beta warning on pkg.go.dev).
- **Branch inspected**: `master` (HEAD not exposed through WebFetch — GitHub commit history page came back empty; version cadence is documented via pkg.go.dev release list).
- **License**: MIT (compatible with our MIT project license, `docs/implementation/01-tech-stack.md §License`).
- **Scope**: both `imapclient` and `imapserver` sub-packages; `imapmemserver` in-memory reference backend.
- **Required workload** (from requirements + phasing):
  - 2,000 concurrent IDLE, goroutine-per-session (`REQ-PROTO-31`).
  - Mailboxes up to 1 TB (`docs/implementation/02-phasing.md` Phase 2.5).
  - `CONDSTORE`/`QRESYNC` are load-bearing for Apple Mail (`REQ-PROTO-32`).
  - Shared mailboxes + `ACL` in Phase 2 (`REQ-PROTO-33`).

## Coverage matrix

Evidence rows refer to `emersion/go-imap/v2/imapserver` unless noted. "const only" = capability constant defined in the top-level `v2` types package but no server-side wiring found (no `Session...` interface, no dedicated handler file). Unknown = not confirmed within the WebFetch budget.

| Capability (REQ / Phase) | RFC | emersion server-side | Evidence |
|---|---|---|---|
| IMAP4rev2 / rev1 baseline | 9051 / 3501 | yes | `server.go` advertises `IMAP4rev2`; Session interface shaped to rev2. |
| STARTTLS | 3207 | yes | `starttls.go`, `Options.TLSConfig`. |
| AUTH= (SASL PLAIN/LOGIN/SCRAM/OAUTHBEARER) | 4954 / 5802 / 7628 | partial | `authenticate.go` + `SessionSASL`; mechanisms provided by consumer via `sasl.Server` (we bring SCRAM/OAUTHBEARER ourselves). |
| LOGIN | 3501 | yes | `login.go`, `Session.Login`. |
| LIST / LSUB | 9051 / 3501 | yes | `list.go`, `Session.List`, `Subscribe/Unsubscribe`. |
| LIST-EXTENDED | 5258 | yes | capability advertised in `capability.go`; flags on `imap.ListOptions`. |
| LIST-STATUS | 5819 | yes | capability advertised in `capability.go`. |
| SPECIAL-USE / CREATE-SPECIAL-USE | 6154 | yes | capability constants present; create options. |
| SELECT / EXAMINE / UNSELECT | 9051 / 3691 | yes | `select.go`, `Session.Select/Unselect`. |
| FETCH | 9051 | yes | `fetch.go`, `Session.Fetch`. |
| STORE | 9051 | yes | `store.go`, `Session.Store`. |
| APPEND | 9051 | yes | `append.go`, `Session.Append`. |
| EXPUNGE | 9051 | yes | `expunge.go`, `Session.Expunge`. |
| SEARCH (basics) | 9051 | yes | `search.go`, `Session.Search`. |
| ESEARCH / SEARCHRES | 4731 / 5182 | yes | listed in `capability.go`. |
| SORT | 5256 | const only | no `sort.go`; no `SessionSort`. |
| THREAD | 5256 | const only | `ThreadAlgorithm` type exists in `v2`; no server handler file found. |
| IDLE | 2177 | yes | `idle.go`; `Session.Idle(w, stop)` on base interface. |
| UIDPLUS | 4315 | yes | `UID-PLUS` advertised; `AppendData` carries `UIDValidity`+`UID`. |
| MOVE | 6851 | yes | `move.go`; `SessionMove` interface. |
| NAMESPACE | 2342 | yes | `namespace.go`; `SessionNamespace` interface. |
| ENABLE | 5161 | yes | `enable.go`. |
| UTF8=ACCEPT | 6855 | yes | advertised in `capability.go`. |
| LITERAL+ / LITERAL- | 7888 | yes | advertised in `capability.go`. |
| ID | 2971 | unknown | not seen in `capability.go` file list. |
| BINARY | 3516 | partial | advertised in `capability.go`; handler support in `append`/`fetch` not verified end-to-end. |
| MULTIAPPEND | 3502 | const only | not listed in `capability.go`; no dedicated file. |
| CATENATE | 4469 | const only | not listed in `capability.go`; no dedicated file. |
| COMPRESS=DEFLATE | 4978 | missing | not in `capability.go`, no `compress.go`. |
| CONDSTORE | 7162 | **missing** | **no `condstore.go`, no `SessionCondStore`, no `MODSEQ` on writer/session API, not in `capability.go`.** Const exists in top-level `v2` only. |
| QRESYNC | 7162 | **missing** | **no QRESYNC handler, no `VANISHED` writer, no `SelectOptions.QResync`.** Const exists in top-level `v2` only. |
| NOTIFY | 5465 | const only | we skip anyway per `docs/architecture/03-protocol-architecture.md` "What we don't build". |
| ACL | 4314 | missing | no `acl.go`, no `SessionACL`; Phase 2 per `REQ-PROTO-33`. |
| QUOTA / QUOTA=RES-STORAGE | 9208 | const only | no `quota.go`; bring our own. |
| METADATA / METADATA-SERVER | 5464 | const only | no `metadata.go`. |
| OBJECTID | 8474 | const only | no `objectid.go`. |
| STATUS=SIZE | 8438 | yes | advertised in `capability.go`. |
| APPENDLIMIT | 7889 | yes | `SessionAppendLimit`. |
| UNAUTHENTICATE | 5161 | yes | `SessionUnauthenticate`. |

Rough scorecard for the caps we must ship by Phase 2 (`docs/implementation/02-phasing.md`): 16 yes / 3 partial / 9 missing (CONDSTORE, QRESYNC, MULTIAPPEND, CATENATE, COMPRESS=DEFLATE, ACL, QUOTA, METADATA, OBJECTID) / 1 const-only (SORT) / 1 unknown (ID).

## CONDSTORE / QRESYNC — dedicated section

`REQ-PROTO-32` is our single hardest IMAP correctness requirement, and the answer from the library is unambiguous:

- **No server-side implementation in `emersion/go-imap/v2/imapserver`.** The `Session` interface has no `MODSEQ`-aware variants. `FetchWriter`/`SelectData`/`StoreOptions` expose no MODSEQ field in the public surface visible via pkg.go.dev. No `SessionCondStore`, no `SessionQResync`, no `VANISHED` writer, no `SelectOptions.QResync` block. No `condstore.go` / `qresync.go` file in the server tree.
- The top-level `v2` package does define `CapCondStore`, `CapQResync`, and presumably wire-level tokens (`MODSEQ`, `VANISHED`, `CHANGEDSINCE`, `UNCHANGEDSINCE`). This covers parser/formatter primitives but **not** server orchestration.
- Consequence: adopting the library as-is forces us to add:
  1. `MODSEQ` into every FETCH writer + STORE path (per-message, per-response token).
  2. `HIGHESTMODSEQ` into `SelectData`/`StatusData`.
  3. `(CHANGEDSINCE / UNCHANGEDSINCE)` handling on FETCH/STORE/SEARCH.
  4. `SELECT ... (QRESYNC (uidvalidity modseq known-uids seq-match-data))` parsing and the corresponding `VANISHED (EARLIER)` emission.
  5. ModSeq ownership (`docs/architecture/05-sync-and-state.md` is clear: MODSEQ lives on our `messages` table; `highest_modseq` on `mailboxes`; expunge-retention feeds QRESYNC diffs from `state_changes`).

Every one of these touches the session API, not just the backend. "Ownership of MODSEQ" is ours regardless — the library does not take a position — but the session interface shape also has to change, which means our fork of the interface diverges permanently from upstream.

## API-integration fit

Strengths for a fresh-start consumer:

- Goroutine-per-session model (`NewSession func(*Conn) (Session, *GreetingData, error)`) matches `docs/architecture/03-protocol-architecture.md §Concurrency model`.
- `Session.Idle(w *UpdateWriter, stop <-chan struct{}) error` aligns naturally with our broadcaster + bounded per-session channel from `docs/architecture/05-sync-and-state.md §Notification broadcasting`. We register a subscription in `Idle`, fan events into `w`, return on `stop`.
- Handler interfaces are narrow; we wire our `store` handle into a `heroldSession` struct and implement them.
- Writer types (`FetchWriter`, `ExpungeWriter`, `UpdateWriter`) encapsulate untagged-response formatting — real value.

Friction:

- `SelectData` / `StatusData` are library-shaped; extending for `HIGHESTMODSEQ` means either a library patch or a side-channel response writer.
- No hook for emitting arbitrary untagged responses mid-FETCH (`* N FETCH (... MODSEQ (x))` interleaved) without diving into writer internals.
- Session state (selected mailbox, enabled caps, CONDSTORE-enabled flag) lives in our struct, not the library — good — but re-entrancy across IDLE + broadcaster writes needs our own mutex discipline; the library does not give us one.
- `Poll(w, allowExpunge)` exists and is a nice primitive for our periodic broadcaster drain.

## Options: cost + risk

### Option A — Fork + clean up

| | |
|---|---|
| Calendar | ~6–8 weeks to add CONDSTORE + QRESYNC + ACL + QUOTA + COMPRESS + METADATA + MULTIAPPEND + CATENATE + wire tests. Another 2–3 weeks of rework when upstream reshapes the `Session` interface between betas (we have already seen 8 betas over ~22 months; the API is moving). |
| Risks | Upstream drift: every `v2.0.0-beta.N` bump is a merge conflict magnet on the exact files we changed (fetch, store, select, capability). Permanent maintenance surface. Beta status means upstream can rename `Session` methods without a deprecation cycle. |
| Benefits | Keep the wire parser/formatter and writer types, which are correct and tested. Savings on parser correctness are real (IMAP lexing is notoriously edge-casey). |

### Option B — Fresh write

| | |
|---|---|
| Calendar | 8–10 weeks for Phase 1 IMAP slice (LOGIN, LIST, LSUB, SELECT/EXAMINE, FETCH, STORE, APPEND, EXPUNGE, IDLE, UIDPLUS, ESEARCH, SEARCH, UTF8=ACCEPT, LITERAL+). Another 6–8 weeks for the Phase 2 IMAP extensions incl. CONDSTORE/QRESYNC, MOVE, LIST-EXTENDED, LIST-STATUS, SPECIAL-USE, MULTIAPPEND, COMPRESS=DEFLATE, ACL. |
| Phase budget cross-check | `docs/implementation/02-phasing.md` Phase 1 is ~14 person-weeks *total* across SMTP relay-in, email-auth, spam, Sieve, IMAP baseline, delivery, directory, OIDC RP, admin, CLI, TLS, FTS. A pure-write IMAP baseline at 8–10 weeks consumes >60% of Phase 1 on one component. That is over-budget. |
| Risks | IMAP lexer edge cases (literals, LITERAL+/LITERAL-, quoted strings, UTF-7 mailbox names, BODY[...] section specs, partial fetches). All fuzzable, all still cost weeks to get right. Full ownership, zero upstream drift. |
| Benefits | No vendor dependency; API shaped exactly for our `store` + broadcaster model; CONDSTORE/QRESYNC baked in from day one rather than bolted on. |

### Option C — Middle path: use parser + grammar from upstream, write our own session + state + storage integration on top

| | |
|---|---|
| Calendar | 1–2 weeks to isolate and depend on the `v2` top-level types package (AST, encoder/decoder, capability constants, NumSet, SearchCriteria, FetchOptions shape). 7–9 weeks for Phase 1 IMAP session + 5–7 weeks for Phase 2 extensions incl. CONDSTORE/QRESYNC, COMPRESS, ACL on our own session loop. |
| Risks | Top-level `v2` package is also beta; AST reshapes can churn. But the top-level types drift less than `imapserver` across betas (parser grammar tracks RFCs; server orchestration is the churny layer). Small risk of importing too much and re-entering upstream-drift territory. |
| Benefits | Keep the expensive-to-get-right piece (parser, formatter, capability tokens) and rewrite the piece whose shape we need to control anyway (session, MODSEQ, per-mailbox state, IDLE broadcaster). Shortest total calendar of the three when CONDSTORE/QRESYNC costs are counted. |

## Tail-risk

`REQ-PROTO-32` is the load-bearing bit. At 2k concurrent IDLE with 1 TB mailboxes, CONDSTORE/QRESYNC failure modes look like:

- Silent MODSEQ regression (client sees monotonicity violation → full resync storm when it happens to thousands of clients simultaneously).
- VANISHED (EARLIER) diffs that omit UIDs expunged just outside the retention window (data loss from the client's point of view).
- Interleaved FETCH responses during IDLE that skip a MODSEQ bump (client's `HIGHESTMODSEQ` drifts ahead of server's → next QRESYNC delta under-reports).
- STORE (UNCHANGEDSINCE) race against a broadcaster-delivered flag update (wrong MODSEQ observed → wrong client).

All four are invariants we own (`docs/architecture/05-sync-and-state.md §Correctness checks`). The library does not help with any of them because it does not speak MODSEQ.

Comparative tail-risk:

- **Fork** has the highest correctness-tail risk. CONDSTORE/QRESYNC are threaded through fetch/store/select/search/expunge in deep ways; bolting them onto interfaces we didn't design, in a codebase that reshapes every ~2 months upstream, leaves seams where invariants can silently break during rebase.
- **Fresh write** has the highest cost but the lowest correctness-tail risk for CONDSTORE/QRESYNC specifically — the API is built MODSEQ-first.
- **Middle** matches fresh-write on correctness-tail (session + MODSEQ are ours, start-to-finish), while outsourcing the lexer/formatter where upstream's test coverage helps us.

## Recommendation

**Adopt Option C (middle path).**

1. **`REQ-PROTO-32` forbids Option A.** CONDSTORE/QRESYNC are entirely absent server-side in `emersion/go-imap/v2/imapserver`. A fork would be a permanent maintenance surface against an alpha-then-beta-for-22-months upstream that reshapes the `Session` API between betas. Our modseq invariants (`docs/architecture/05-sync-and-state.md §Correctness checks`) live on the session-handler seam, exactly where fork conflicts will keep landing.
2. **`REQ-PROTO-31` and the Phase 1 budget forbid Option B at its full scope.** Phase 1 (~14 person-weeks total in `docs/implementation/02-phasing.md`) cannot absorb a 10-week IMAP lexer+formatter+session rewrite on top of SMTP, Sieve, auth, delivery, directory, OIDC. Reusing upstream's mature parser + writer layer cuts the Phase 1 IMAP slice by the 3–4 weeks that IMAP lexing + untagged-response formatting actually cost.
3. **Tail-risk at 2k IDLE + 1 TB mailboxes favours ownership of the session layer.** Option C keeps ownership exactly where the hard invariants live — MODSEQ monotonicity, VANISHED correctness, UNCHANGEDSINCE atomicity, broadcaster/FETCH interleaving — while borrowing the piece (grammar) where upstream's tests and fuzz corpus are a clear win.

Concretely: depend on `github.com/emersion/go-imap/v2` (top-level types only — AST, encoder/decoder, capability tokens, NumSet, SearchCriteria). Do not depend on `v2/imapserver`. Implement our own `internal/protoimap` session loop, selected-mailbox state, IDLE broadcaster hook, and CONDSTORE/QRESYNC logic directly on top of the `store` interface. Revisit if the top-level `v2` types package churns disruptively across three consecutive betas — at which point we own the parser too.

