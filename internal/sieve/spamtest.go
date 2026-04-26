package sieve

// This file documents the shape of the spamtest / spamtestplus mapping.
// The active implementation lives in interp.go (mapSpamScore). Keeping
// the doc alongside the enum definitions makes the RFC 5235 mapping
// discoverable from the package API surface.

// SpamTestRange represents the documented interpretation of the integer
// values returned by the spamtest extension per RFC 5235 §2.
//
//	0   classifier did not run / unknown
//	1   ham (clearly not spam)
//	2-4 slight-to-moderate suspicion
//	5-8 likely spam
//	9-10 definitely spam
//
// mapSpamScore normalises the classifier's [0,1] confidence into this
// range. Auth-layer overrides (for example DMARC p=reject alignment
// failure forcing a 10) are applied by the caller before invoking the
// interpreter; see docs/design/requirements/06-filtering.md §REQ-FILT-80..83.
type SpamTestRange int

// Named spam-test levels provided for callers that want to express
// thresholds symbolically rather than by magic number.
const (
	SpamUnknown SpamTestRange = 0
	SpamHam     SpamTestRange = 1
	SpamSuspect SpamTestRange = 3
	SpamLikely  SpamTestRange = 7
	SpamCertain SpamTestRange = 10
)
