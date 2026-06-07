package store_test

// parse_backfill_horizon_test.go tests store.ParseBackfillHorizon with the
// full set of accepted and rejected forms.
// REQ-IMAP-IMP-15, REQ-IMAP-IMP-16.

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// refNow is the reference instant used by all horizon tests.
var refNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func TestParseBackfillHorizon_All(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("all", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor != nil {
		t.Fatalf("expected nil floor for \"all\", got %v", *floor)
	}
}

func TestParseBackfillHorizon_30d(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("30d", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil for 30d")
	}
	expected := refNow.Add(-30 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !floor.Equal(expected) {
		t.Fatalf("30d floor = %v, want %v", *floor, expected)
	}
}

func TestParseBackfillHorizon_90d(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("90d", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil for 90d")
	}
	expected := refNow.Add(-90 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !floor.Equal(expected) {
		t.Fatalf("90d floor = %v, want %v", *floor, expected)
	}
}

func TestParseBackfillHorizon_1y(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("1y", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil for 1y")
	}
	expected := refNow.Add(-365 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !floor.Equal(expected) {
		t.Fatalf("1y floor = %v, want %v", *floor, expected)
	}
}

func TestParseBackfillHorizon_365d(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("365d", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil for 365d")
	}
	expected := refNow.Add(-365 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !floor.Equal(expected) {
		t.Fatalf("365d floor = %v, want %v", *floor, expected)
	}
}

func TestParseBackfillHorizon_180d(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("180d", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil for 180d")
	}
	expected := refNow.Add(-180 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !floor.Equal(expected) {
		t.Fatalf("180d floor = %v, want %v", *floor, expected)
	}
}

func TestParseBackfillHorizon_2y(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("2y", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil for 2y")
	}
	expected := refNow.Add(-2 * 365 * 24 * time.Hour).UTC().Truncate(24 * time.Hour)
	if !floor.Equal(expected) {
		t.Fatalf("2y floor = %v, want %v", *floor, expected)
	}
}

func TestParseBackfillHorizon_AbsoluteDate(t *testing.T) {
	floor, err := store.ParseBackfillHorizon("2025-06-01", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil for absolute date")
	}
	expected, _ := time.Parse("2006-01-02", "2025-06-01")
	expected = expected.UTC()
	if !floor.Equal(expected) {
		t.Fatalf("absolute date floor = %v, want %v", *floor, expected)
	}
}

func TestParseBackfillHorizon_AbsoluteDateTruncatesTime(t *testing.T) {
	// An absolute date "2024-01-15" must parse as midnight UTC regardless of now.
	floor, err := store.ParseBackfillHorizon("2024-01-15", refNow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if floor == nil {
		t.Fatalf("floor must not be nil")
	}
	if !floor.Equal(time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("floor = %v, want 2024-01-15 00:00:00 UTC", *floor)
	}
}

func TestParseBackfillHorizon_InvalidForm(t *testing.T) {
	cases := []string{
		"forever",
		"7d", // not in the supported set
		"",   // empty string
		"never",
		"1y2d",
		"notadate",
		"2026/06/01", // wrong date separator
		"0",
		"all-of-it",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := store.ParseBackfillHorizon(c, refNow)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", c)
			}
		})
	}
}

// TestParseBackfillHorizon_FloorIsMidnightUTC verifies that relative horizons
// produce a floor whose time component is always midnight UTC.
func TestParseBackfillHorizon_FloorIsMidnightUTC(t *testing.T) {
	// Use a reference time with non-midnight time to ensure truncation.
	now := time.Date(2026, 3, 15, 17, 45, 30, 0, time.UTC)
	for _, h := range []string{"30d", "90d", "1y"} {
		floor, err := store.ParseBackfillHorizon(h, now)
		if err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		if floor.Hour() != 0 || floor.Minute() != 0 || floor.Second() != 0 || floor.Nanosecond() != 0 {
			t.Fatalf("%s: floor is not midnight UTC: %v", h, *floor)
		}
		if floor.Location() != time.UTC {
			t.Fatalf("%s: floor is not in UTC: %v", h, floor.Location())
		}
	}
}
