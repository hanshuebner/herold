# Fuzz target inventory — Phase 1 Wave 4

Audit-only; no fuzz targets are added in this ticket. The gaps listed
below are the suggested Wave 4.5 / Phase 1.5 priority work.

## Existing `Fuzz*` targets

Enumerated from `grep -rnE '^func Fuzz' internal plugins --include='*_test.go'`:

| Target | Location | Fuzzes |
|---|---|---|
| `FuzzParse` | `internal/mailparse/fuzz_test.go` | MIME parser against an `.eml` spike corpus; asserts no panic and round-trip Raw equality for successful parses. |
| `FuzzExtractSignatureTags` | `internal/maildkim/fuzz_test.go` | DKIM-Signature header tag-list scanner; asserts no panic and no CR/LF leakage into extracted tag fields. |
| `FuzzParseRecord` | `internal/maildmarc/fuzz_test.go` | DMARC TXT record parser (`go-msgauth/dmarc.Parse`); panic-safety only. |
| `FuzzParseRecord` | `internal/mailspf/fuzz_test.go` | SPF record parser; bounded to 10 KiB; panic-safety only. |
| `FuzzVerify` | `internal/mailarc/fuzz_test.go` | ARC chain walker + signature verifier; input capped at 64 KiB; no internal errors expected. |
| `FuzzParse` | `internal/sieve/fuzz_test.go` | Sieve parser; panic-safety. |
| `FuzzInterp` | `internal/sieve/fuzz_test.go` | Sieve interpreter against a canonical script with fuzzed message bytes; panic-safety under a 100 ms budget. |
| `FuzzBuildRequest` | `internal/spam/fuzz_test.go` | Spam classifier request builder over parsed messages; asserts body excerpt respects cap. |
| `FuzzLoad` | `internal/sysconfig/fuzz_test.go` | System config TOML parser; panic-safety. |

## Wire parsers without a fuzz target

From the Phase 1 checklist in the ticket, cross-referenced with the
table above:

| Parser / surface | Fuzz target? | Notes |
|---|---|---|
| SMTP command parser (`internal/protosmtp` session) | NO | Line-oriented state machine in `session.go`; ingests untrusted bytes per command. |
| SMTP address parser (MAIL / RCPT) | NO | Reject surface between protocol and directory resolution; high value for malformed-angle-bracket cases. |
| IMAP command parser (`internal/protoimap/parser.go`) | NO | Tagged literal + list parser; highest-volume untrusted surface in Phase 1. |
| IMAP literal / continuation logic | NO | Two-phase read (`{N}\r\n` + payload) with deadline semantics; easy to OOM on adversarial N. |
| MIME parser (`internal/mailparse`) | YES | Covered by `FuzzParse`. |
| RFC 5322 header / address parser | NO | Today the MIME fuzzer exercises the top-level scan but the dedicated address parser (From / To / Cc / Bcc extractor) has no direct target. |
| DKIM signature tag scanner | YES | `FuzzExtractSignatureTags`. |
| SPF record parser | YES | `FuzzParseRecord` (mailspf). |
| DMARC record parser | YES | `FuzzParseRecord` (maildmarc). |
| ARC chain walker | YES | `FuzzVerify` (mailarc). |
| Sieve script parser | YES | `FuzzParse` (sieve). |
| Sieve interpreter | YES | `FuzzInterp` (sieve). |
| JSON-RPC plugin codec (`internal/plugin` framing) | YES (implicit via stdlib `encoding/json`; no dedicated target) | Consider an explicit target over the length-prefixed frame reader to harden the plugin boundary. |
| Config TOML (`internal/sysconfig`) | YES | `FuzzLoad`. |

## Priority list for Wave 4.5 / Phase 1.5

In decreasing order of untrusted-input exposure × lack of coverage:

1. **IMAP command parser + literal/continuation reader**
   (`internal/protoimap/parser.go`). Wire-facing, complex, pre-auth.
   One target per layer: (a) a pure `ParseCommand([]byte)` fuzzer, and
   (b) a literal-reader fuzzer that interleaves `{N}\r\n` frames with
   random payloads under a bounded total size.
2. **SMTP command parser** (`internal/protosmtp/session.go`
   `readCommand` / `parseMAIL`). Pre-auth, and Phase 2 will widen the
   surface with BDAT chunking.
3. **SMTP address parser** (extract from MAIL / RCPT lines). Feeds the
   directory resolver; malformed input should always surface as 501,
   never as a crash.
4. **RFC 5322 address / header parser** split out of `mailparse` —
   the MIME fuzzer is structural; a dedicated address-parser fuzzer
   would hit the `go-message` address-list code path.
5. **JSON-RPC plugin framer** (`internal/plugin`). Lower priority
   because the trust boundary is operator-installed plugin binaries,
   but still worth a small target over the stdio framer.

Estimated effort: one engineer-week for (1) + (2) + (3) including
seed corpora mined from existing protocol tests; (4) and (5) are
half a week combined.
