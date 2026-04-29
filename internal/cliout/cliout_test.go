package cliout_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/cliout"
)

func TestTable_BasicRendering(t *testing.T) {
	var buf bytes.Buffer
	tbl := cliout.NewTable(&buf)
	tbl.Header("ID", "STATE", "RCPT-TO")
	tbl.Row("1", "deferred", "alice@example.com")
	tbl.Row("2", "failed", "bob@example.org")
	if err := tbl.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "STATE") {
		t.Errorf("header not in output: %s", out)
	}
	if !strings.Contains(out, "alice@example.com") {
		t.Errorf("row 1 not in output: %s", out)
	}
	if !strings.Contains(out, "bob@example.org") {
		t.Errorf("row 2 not in output: %s", out)
	}
}

func TestKV_EmptyValuesOmitted(t *testing.T) {
	var buf bytes.Buffer
	cliout.KV(&buf, [][2]string{
		{"id", "42"},
		{"error", ""},     // empty — should be omitted
		{"state", "done"}, // non-empty — shown
	})
	out := buf.String()
	if !strings.Contains(out, "id") {
		t.Errorf("id not in output: %s", out)
	}
	if strings.Contains(out, "error") {
		t.Errorf("empty 'error' field should be omitted: %s", out)
	}
	if !strings.Contains(out, "state") {
		t.Errorf("state not in output: %s", out)
	}
}

func TestFormatTime_RFC3339(t *testing.T) {
	got := cliout.FormatTime("2026-04-28T23:39:37Z")
	if got == "" || got == "2026-04-28T23:39:37Z" {
		t.Errorf("FormatTime did not reformat timestamp: %q", got)
	}
	// The output should be in short date-time form (no T, no Z suffix).
	if strings.Contains(got, "T") || strings.Contains(got, "Z") {
		t.Errorf("FormatTime should not contain RFC3339 T/Z: %q", got)
	}
	// Should be a non-empty formatted date-time string.
	if len(got) < 10 {
		t.Errorf("FormatTime too short: %q", got)
	}
}

func TestFormatTime_Empty(t *testing.T) {
	if got := cliout.FormatTime(""); got != "" {
		t.Errorf("FormatTime of empty should return empty, got %q", got)
	}
}

func TestFormatTimeValue_Zero(t *testing.T) {
	if got := cliout.FormatTimeValue(time.Time{}); got != "" {
		t.Errorf("FormatTimeValue of zero should return empty, got %q", got)
	}
}

func TestFormatTimeValue_NonZero(t *testing.T) {
	ts := time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC)
	got := cliout.FormatTimeValue(ts)
	if !strings.Contains(got, "2026-04-29") {
		t.Errorf("date not in output: %q", got)
	}
}

func TestTrunc_Short(t *testing.T) {
	if got := cliout.Trunc("hello", 10); got != "hello" {
		t.Errorf("short string should be unchanged: %q", got)
	}
}

func TestTrunc_Long(t *testing.T) {
	got := cliout.Trunc("abcdefghij", 5)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("long string should be truncated with ellipsis: %q", got)
	}
	if len([]rune(got)) != 5+3 { // 5 runes + "..."
		t.Errorf("truncated length wrong: %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := [][2]string{
		{"0", "0 B"},
		{"500", "500 B"},
		{"1024", "1.0 KB"},
		{"2097152", "2.0 MB"},
		{"1073741824", "1.0 GB"},
	}
	for _, c := range cases {
		var n int64
		for _, r := range c[0] {
			n = n*10 + int64(r-'0')
		}
		if got := cliout.HumanBytes(n); got != c[1] {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, c[1])
		}
	}
}

func TestBoolYesNo(t *testing.T) {
	if cliout.BoolYesNo(true) != "yes" {
		t.Error("BoolYesNo(true) should be 'yes'")
	}
	if cliout.BoolYesNo(false) != "no" {
		t.Error("BoolYesNo(false) should be 'no'")
	}
}

func TestStringsJoin_None(t *testing.T) {
	if got := cliout.StringsJoin(nil); got != "(none)" {
		t.Errorf("nil slice should give '(none)', got %q", got)
	}
}

func TestStringsJoin_Values(t *testing.T) {
	if got := cliout.StringsJoin([]string{"a", "b"}); got != "a, b" {
		t.Errorf("StringsJoin = %q, want 'a, b'", got)
	}
}
