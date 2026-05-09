# 20 — PDF extraction in an isolated subprocess

*(Added 2026-05-09 in response to a real incident: a single PDF
attachment in one user's mailbox drove `github.com/ledongthuc/pdf`'s
PostScript interpreter into a recursive `readArray` ↔ `readObject`
loop that allocated ~1.4 MiB per object and accumulated past 100 GiB
of resident heap before the operator killed the process. The pure-Go
`recover()` guard in `extractPDFText` does not help: the library does
not panic, it allocates. The "byte budget" added to the FTS worker on
the same day (REQ-STORE-FTS via the architecture doc) bounds the
extracted-text *output* but the runaway happens entirely inside the
parser's intermediate state. The library has no API to bound that.
This document specifies the long-term fix: every PDF parse runs in a
short-lived child process whose CPU, wall-clock, memory, and output
size are bounded by the OS, not by trust in the parser.)*

## Scope

Herold's FTS indexer extracts plain text from supported attachment
types (REQ-STORE-60). The OOXML / HTML / plain-text branches are pure
Go and bounded by their own structural limits. The PDF branch is the
outlier: the chosen pure-Go library can be driven into pathological
allocation by malformed input, and there is no in-process way to
contain it. This document scopes the move of **PDF extraction only**
to a short-lived child process; OOXML / HTML / plain text remain in
the existing in-process extractor. Generalising the subprocess pattern
to other formats is explicitly out of scope (REQ-PDFEX-90).

A short-term mitigation lands first: PDF extraction is **disabled by
default** in the in-process extractor. The subprocess implementation
re-enables it once shipped.

## Process model

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-01 | Every PDF extraction is a fresh `fork`+`exec` of the extractor binary. There is no long-running pool, no warm process, no shared state between parses. The async indexer's per-call latency budget tolerates the ~10–50 ms `fork`+`exec` overhead easily; the per-parse isolation eliminates entire classes of failure (an OOM in parse N leaves parse N+1 untouched; a crash kills only the in-flight parse). |
| REQ-PDFEX-02 | The child process exits after writing one extraction result. There is no protocol-level "next request" frame; if the next message also has a PDF, the worker spawns a new child. |
| REQ-PDFEX-03 | The child process MUST be reaped: its stdout, stderr, and exit status are read to completion before the worker treats the parse as finished. A leaked zombie counts as a defect. |
| REQ-PDFEX-04 | The worker holds at most one in-flight child at a time. Concurrency is not part of v1; the indexer is single-threaded today and the subprocess pattern preserves that contract. |

## Library choice

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-10 | The child invokes **`pdftotext`** from poppler-utils (the canonical mature open-source PDF text extractor). The herold binary itself does NOT link any PDF library; the in-process `github.com/ledongthuc/pdf` dependency is removed when this lands. |
| REQ-PDFEX-11 | `pdftotext` is invoked as: `pdftotext -nopgbrk -enc UTF-8 - -` — read PDF bytes from stdin, write extracted UTF-8 text to stdout, no page-break form-feeds. The exact argv list is defined in code; no operator-tunable flags in v1 (operators tune the wrapper, not pdftotext's CLI). |
| REQ-PDFEX-12 | If `pdftotext` is not present on `$PATH` (or at the operator-configured path) at startup, the FTS extractor logs a single warn-level message naming the missing binary and routes the PDF branch to "skip" for the lifetime of the process. herold MUST NOT refuse to start. The check is performed once at startup, not on every parse. |
| REQ-PDFEX-13 | The `pdftotext` binary path is operator-configurable via `appconfig: fts.extract.pdf.binary_path` (default: `pdftotext`, resolved via `$PATH`). Operators with unusual installs (a sandboxed copy under `/opt/herold/bin/pdftotext`) point this at the absolute path. |

## Resource limits

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-20 | Each child has a hard **wall-clock timeout** enforced by the parent via `exec.CommandContext` with a `context.WithTimeout`. Default 30 s, configurable via `appconfig: fts.extract.pdf.timeout_ms`. On expiry the parent SIGKILLs the child and treats the parse as a timeout failure. |
| REQ-PDFEX-21 | Each child has a **memory cap** applied via `RLIMIT_AS` (Linux) or `RLIMIT_DATA` (macOS / BSDs) in `SysProcAttr.Setrlimit`-equivalent. Default 1 GiB, configurable via `appconfig: fts.extract.pdf.max_memory_bytes`. The kernel kills the child on overrun; the parent observes the non-zero exit and records the failure. **macOS exception (resolved 2026-05-09):** the Darwin kernel ignores `RLIMIT_AS` / `RLIMIT_DATA`; the wall-clock timeout (REQ-PDFEX-20) is the only reliable bound on macOS. The gap is accepted as-is — we will NOT poll the child's RSS via `proc_pidinfo` or build a Darwin-specific watchdog. macOS deployments are dev-grade or single-tenant; in those contexts a 50 GiB transient allocation inside the timeout window is a tolerable failure mode (the OS will swap and the parent will eventually `SIGKILL`). The configuration MUST still be plumbed so Linux operators get the hard cap and so the same code path works across platforms. |
| REQ-PDFEX-22 | The parent reads the child's stdout through an `io.LimitReader` capped at `appconfig: fts.extract.pdf.max_output_bytes` (default 1 MiB; aligned with `defaultPerMessageMaxBytes`). Past the cap the truncation is recorded in the per-format metric (`herold_fts_attachment_extracted_total{format="pdf",outcome="truncated_attachment"}`). |
| REQ-PDFEX-23 | The parent rejects PDF blobs larger than `appconfig: fts.extract.pdf.max_input_bytes` (default 50 MiB) **without spawning the child**. Cheap pre-check before fork; protects the `fork`+`exec` budget from obvious abuse. Larger inputs are recorded as `outcome="oversized"`. |
| REQ-PDFEX-24 | The parent gives the child a **CPU-time** rlimit via `RLIMIT_CPU` set to `2 × timeout_ms / 1000` seconds (rounded up, minimum 5 s). Belt-and-suspenders against a parent-side timer that fires too late; the kernel takes the child out before our timeout would. |

## Invocation contract

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-30 | Input transport: PDF bytes are piped to the child via stdin. No temp files, no disk round-trip. The pipe write happens on a dedicated goroutine while the parent simultaneously reads stdout / stderr; classic three-way concurrent IO with a context-aware shutdown. |
| REQ-PDFEX-31 | Output transport: extracted UTF-8 text on stdout, capped per REQ-PDFEX-22. The child MUST NOT emit anything else on stdout. |
| REQ-PDFEX-32 | Diagnostic transport: stderr is captured into a bounded buffer (default 16 KiB; first-bytes-win). On non-zero exit the contents are logged at `info` on `subsystem=fts-pdf` to support post-mortem diagnosis without leaking arbitrary content into the structured log. |
| REQ-PDFEX-33 | Exit codes: `0` = success (use stdout); non-zero = failure (drop output, record metric). The wrapper does NOT distinguish between pdftotext's specific non-zero codes; the failure shape is recorded per REQ-PDFEX-50. |
| REQ-PDFEX-34 | The wrapper does NOT use JSON-RPC, the herold plugin SDK, or any structured protocol. The plugin SDK is for long-running plugins with rich call surfaces; pdftotext is a one-shot Unix filter and the existing `os/exec` machinery is the right tool. This is a deliberate divergence from the "out-of-process plugins only" rule in CLAUDE.md, justified by the one-shot, single-call nature of the subprocess. |

## Failure handling

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-40 | A failed extraction (timeout, OOM, non-zero exit, missing binary, oversized input) does NOT mark the message as permanently un-extractable. The FTS worker indexes the message **without** PDF text and advances the cursor; the message body and other parts still contribute to the index. A future cursor pass after a `pdftotext` upgrade or a wrapper bugfix re-attempts the parse on the same message. (REQ-IMPORT-style "permanent skip" persistence is intentionally NOT adopted here.) |
| REQ-PDFEX-41 | Failures are observable via `herold_fts_attachment_extracted_total{format="pdf",outcome="<o>"}` with the closed vocabulary `{"ok","error","timeout","oom","crash","oversized","truncated_attachment","truncated_message","binary_missing","disabled"}`. The wrapper distinguishes timeout from crash by checking `ProcessState.ExitCode()` and signal cause; OOM is detected by a child killed with SIGKILL with no parent timeout having fired. The `disabled` value is emitted by the in-process stub during the REQ-PDFEX-110 stopgap window and by the subprocess wrapper itself when `appconfig: fts.extract.pdf.enabled = false`. |
| REQ-PDFEX-42 | A burst of failures from one corpus does NOT degrade the worker's overall throughput. The wrapper's per-call cost is bounded by the timeout (REQ-PDFEX-20), so a stream of failing PDFs costs at most `N × timeout_ms` regardless of input shape. |

## Configuration surface

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-50 | All knobs live under a single `[fts.extract.pdf]` block in appconfig (NOT system.toml; PDF extraction policy is a runtime decision, not a deploy-time identity decision). The full set: |
| | - `enabled` (bool, default `true` once subprocess wrapper ships; pre-wrapper stopgap defaults to `false`). |
| | - `binary_path` (string, default `pdftotext`). |
| | - `timeout_ms` (int, default `30000`). |
| | - `max_input_bytes` (int, default `52428800` — 50 MiB). |
| | - `max_output_bytes` (int, default `1048576` — 1 MiB). |
| | - `max_memory_bytes` (int, default `1073741824` — 1 GiB). |
| REQ-PDFEX-51 | `enabled = false` routes the PDF branch to `outcome="skipped"` (NOT `"binary_missing"`); the metric distinguishes "operator turned it off" from "binary went missing". The CLI / admin UI surfaces this distinction so an operator who *thinks* PDF is enabled but `binary_path` is wrong gets a clear signal. |
| REQ-PDFEX-52 | A reload (`SIGHUP`) re-reads the config and re-runs the binary-path probe (REQ-PDFEX-12). An operator can install poppler, reload herold, and PDF extraction goes from "skipped" to "ok" without a restart. |

## Security

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-60 | The child runs with the same uid/gid as the herold parent. v1 does not drop privileges (no `setuid` step). pdftotext is a long-audited tool; the subprocess boundary alone is the safety story. |
| REQ-PDFEX-61 | The child has no inherited file descriptors beyond stdin/stdout/stderr. `Cmd.ExtraFiles` is empty, and Go's `os/exec` already closes inherited fds on exec by default. |
| REQ-PDFEX-62 | The child is launched with a clean environment: `Cmd.Env = []string{"PATH=" + secureBinPath, "LANG=C.UTF-8"}`. No `HOME`, no `LD_PRELOAD`-equivalent, no operator-set env leaks into the parser. |
| REQ-PDFEX-63 | Sandbox profiles (`sandbox-exec` on macOS, `seccomp` / `namespaces` on Linux) are explicitly out of v1 scope (REQ-PDFEX-91). Adding them is a future hardening; not a launch blocker. |

## Distribution and install

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-70 | `pdftotext` is a runtime dependency, not a build-time dependency. The herold binary remains pure-Go and CGO-free per the project hard rule. Operators install poppler-utils themselves via their distro package manager. |
| REQ-PDFEX-71 | The default herold container image MUST include `poppler-utils` (Alpine: `apk add poppler-utils`; Debian: `apt-get install -y poppler-utils`). The image-build script asserts the binary is present at the expected path. The `:slim` image variant — built for minimum surface area, no JS bundle, no docs — does **NOT** include `poppler-utils`; it ships with `appconfig: fts.extract.pdf.enabled = false` baked into the default config and stays that way unless the operator installs poppler in their own runtime layer and flips the flag. The image-build script for `:slim` asserts the binary is **absent** so a leaked install does not silently change the variant's surface. |
| REQ-PDFEX-72 | The operator manual (`docs/operate.md` or successor) lists `poppler-utils` under "Optional runtime dependencies" with per-distro install commands. The operator manual MUST also document the `enabled = false` default and the path forward to enabling PDF text indexing. |

## Operator surface

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-80 | `herold admin diag pdftotext` (new subcommand) probes the configured binary, reports `version`, `path`, and a synthetic-PDF round-trip latency. Used at install time to verify the integration without waiting for an indexer cycle. |
| REQ-PDFEX-81 | The metrics surface (REQ-PDFEX-41) is the primary operator-facing signal. A grafana panel in the shipped dashboard (`docs/design/server/notes/grafana/` if it lands) shows the per-outcome breakdown. |
| REQ-PDFEX-82 | Per-extraction wall-clock latency histogram: `herold_fts_pdf_extract_seconds` with the standard prom buckets. Operators see the p99 climbing if pdftotext is being slow on the corpus. |

## Out of scope (v1)

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-90 | Generalising the subprocess pattern to OOXML / HTML / plain text. Those extractors are pure-Go and bounded by their own loops; a corresponding incident has not occurred. Revisit when one does. |
| REQ-PDFEX-91 | OS-level sandboxing (`sandbox-exec`, `seccomp`, separate uid). Subprocess + rlimit + timeout is the v1 isolation; further sandboxing is a phase-2 hardening item once the basic wiring is proven in production. |
| REQ-PDFEX-92 | OCR for scanned PDFs (REQ-STORE-60 already excluded this). pdftotext extracts the text layer only; image-only PDFs return empty output, which is correct. |
| REQ-PDFEX-93 | Reusing the herold plugin SDK for this. The plugin SDK targets long-running protocol-rich plugins (DNS providers, NATS publisher, spam classifier); pdftotext is a one-shot Unix filter and the value of JSON-RPC framing is negative. (REQ-PDFEX-34 makes this explicit; this entry restates it as a deliberate non-goal so it does not get re-litigated.) |
| REQ-PDFEX-94 | A pdftotext alternative selection mechanism (e.g., `mutool`, `pdf2txt.py`). One canonical extractor per format; if pdftotext proves inadequate the replacement is a code change, not a config knob. |

## Test strategy

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-100 | Unit tests for the wrapper use a mockable executor interface so the test does not require pdftotext on the test runner. The default executor (`exec.CommandContext`) is exercised in a build-tagged integration test that skips if pdftotext is not on `$PATH`. |
| REQ-PDFEX-101 | Pathological PDF fixtures: at minimum (a) an empty file, (b) a non-PDF blob with `application/pdf` content-type, (c) a synthetically deeply-nested array PDF that triggered the original incident if the corpus owner can share a redacted copy, (d) a PDF designed to exceed `max_output_bytes`, (e) a PDF designed to exceed `max_input_bytes`. Fixtures live under `internal/storefts/pdfextract/testdata/`. |
| REQ-PDFEX-102 | Timeout test: feed a fixture that pdftotext takes long enough to process to trip the test's `timeout_ms` (set low for the test). Assert the wrapper kills the child, returns a timeout error, and increments the metric. |
| REQ-PDFEX-103 | Reload test: with the wrapper running, change `binary_path` from a valid to an invalid value via SIGHUP, assert the next parse routes to `binary_missing` without restarting herold. |
| REQ-PDFEX-104 | The wrapper is fuzzed (`STANDARDS.md` §8.2) only on its INPUT-VALIDATION path (PDF size pre-check, config parse). The pdftotext binary itself is not fuzzed by herold; that is upstream's job. |

## Migration

| ID | Requirement |
|----|-------------|
| REQ-PDFEX-110 | The stopgap commit (lands first, before the subprocess wrapper) sets the in-process PDF extractor to skip every PDF and increment a metric. This unblocks the operator immediately; the FTS cursor advances past the message that pinned 100 GiB. The stopgap commit modifies `internal/storefts/attachments.go` only and is reverted when the subprocess wrapper merges. |
| REQ-PDFEX-111 | The subprocess wrapper lands as a new package `internal/storefts/pdfextract/`. Removing the in-process pdf import (`github.com/ledongthuc/pdf` from `attachments.go`) is part of the same commit; `go.mod` is tidied. |
| REQ-PDFEX-112 | The cursor reconciliation is automatic: every message processed under the in-process extractor that hit a PDF is currently indexed *without* PDF text (because the stopgap skipped it). Once the subprocess wrapper ships, the operator can run `herold admin diag fts reindex --kind pdf-attachments [--principal <email>]` (new subcommand) to re-extract messages that have a PDF attachment against the new wrapper. The subcommand walks the messages table, replays the FTS path for matching rows, and bumps the same metrics. The `--principal <email>` form scopes the walk to one principal so an operator can stage a rollout (verify on one mailbox first) before kicking off the corpus-wide pass; omitting the flag walks every principal. The subcommand acquires the same per-principal lock the FTS worker uses so a reindex cannot race a live indexer pass on the same rows. |

## Open questions remaining

All resolved 2026-05-09; folded into the requirements above:

- Container image policy → REQ-PDFEX-71 (`:slim` omits poppler, ships
  with PDF disabled in default config; build script asserts absence).
- macOS `RLIMIT_AS` gap → REQ-PDFEX-21 (gap accepted; wall-clock
  timeout is the sole macOS bound; no `proc_pidinfo` watchdog).
- Reindex subcommand scope → REQ-PDFEX-112 (`--principal <email>`
  flag added; omitting it walks every principal).
