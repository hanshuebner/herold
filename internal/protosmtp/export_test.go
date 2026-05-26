package protosmtp

// Test-only re-exports. `export_test.go` is only compiled when running
// `go test ./internal/protosmtp/...`, so the symbols below are not
// available to non-test consumers. Lets external `protosmtp_test`
// callers reach unexported helpers (e.g. seedFromAddress under issue
// #16 regression tests) without inflating the package's public API.

var SeedFromAddressForTest = seedFromAddress
