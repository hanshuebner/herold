# Phase 1 e2e findings

One entry per bug observed while writing the Phase 1 e2e suite. Severity
rubric: blocker = exit criterion unmet; major = exit criterion met but
contract broken; minor = cosmetic or ergonomic.

(No open findings — Wave 4.5 closed Finding 11 by stamping the spam
verdict into the stored `Authentication-Results:` header as an
`x-herold-spam=<verdict>` method token. See `internal/protosmtp/deliver.go`
and `TestPhase1_LLMClassifier_HeaderStamped` for the regression guard.)
