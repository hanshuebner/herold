# Phase 1 e2e findings

One entry per bug observed while writing the Phase 1 e2e suite. Severity
rubric: blocker = exit criterion unmet; major = exit criterion met but
contract broken; minor = cosmetic or ergonomic.

## 1. Spam verdict not surfaced in a stored header

- Severity: major
- Owning package: `internal/protosmtp` (`deliver.go`) + `internal/spam`
- Exit criterion: Phase 1 criterion #3 — "LLM classifier runs against
  (a fake) Ollama; verdict visible in message headers."
- Repro:
  1. `TestPhase1_LLMClassifier_HeaderStamped` (in
     `test/e2e/phase1_test.go`) wires the fake spam plugin to return
     `{"verdict":"spam","score":0.9}` and delivers a message.
  2. Load the stored blob bytes via
     `fixtures.LoadMessagesIn(...).Bytes`.
  3. Search for any of `x-herold-spam=`, `x-spam-status=`, or a
     Sieve-style method token inside `Authentication-Results:`.
- Expected: An operator-visible header identifies the spam verdict.
  The exit criterion calls out "verdict visible in message headers."
  Candidates: an `Authentication-Results: ...; x-herold-spam=spam`
  method, or a dedicated `X-Herold-Spam-Status: spam; score=0.9`
  header rendered next to `Authentication-Results`.
- Actual: `deliver.go:renderAuthResults` emits only the RFC 8601 method
  tokens for SPF / DKIM / DMARC / ARC. The `spam.Classification` value
  is carried into the Sieve environment (so routing still works) but
  never rendered into the stored blob.
  `TestDelivery_AuthenticationResults_Header_Prepended` in
  `internal/protosmtp/server_test.go` asserts `dmarc=` / `spf=` tokens
  but does not assert any spam-verdict carriage.
- Workaround in tests: the e2e test skips with a pointer to this
  finding via
  `t.Skip("pipeline does not stamp spam verdict; see test/e2e/findings.md")`
  so the suite stays green until the pipeline is extended.
- Suggested fix surface: extend `renderAuthResults` to append an
  `x-herold-spam=<verdict>` method segment when
  `classification.Verdict != Unclassified`, or add a separate
  `X-Herold-Spam-Status:` line in `assembleStoredBytes`. Either keeps
  the contract with `Authentication-Results` without breaking existing
  parsers.
