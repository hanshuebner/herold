package admin

import (
	"strings"
	"testing"
)

func TestCLIHookCreate_PrintsSecretOnce(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("hook", "create",
		"--owner-kind=domain",
		"--owner-id=example.com",
		"--target-url=https://hook.example.com/recv",
		"--mode=inline")
	if err != nil {
		t.Fatalf("hook create: %v", err)
	}
	// In human mode the output has "hmac_secret: <value>"; in JSON mode
	// (non-TTY test buffer) it has "\"hmac_secret\": \"<value>\"".
	// Checking for the field name without the colon covers both.
	if !strings.Contains(out, "hmac_secret") {
		t.Fatalf("expected hmac_secret in output: %s", out)
	}
}

func TestCLIHookList_Filtered(t *testing.T) {
	env := newCLITestEnv(t, nil)
	if _, _, err := env.run("hook", "create",
		"--owner-kind=domain",
		"--owner-id=alpha.example",
		"--target-url=https://recv.alpha.example/h"); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if _, _, err := env.run("hook", "create",
		"--owner-kind=domain",
		"--owner-id=beta.example",
		"--target-url=https://recv.beta.example/h"); err != nil {
		t.Fatalf("create beta: %v", err)
	}
	out, _, err := env.run("hook", "list",
		"--owner-kind=domain",
		"--owner-id=alpha.example",
		"--json")
	if err != nil {
		t.Fatalf("hook list: %v", err)
	}
	if !strings.Contains(out, "alpha.example") {
		t.Fatalf("expected alpha.example in output: %s", out)
	}
	if strings.Contains(out, "beta.example") {
		t.Fatalf("filter should have excluded beta: %s", out)
	}
}

func TestCLIHookCreate_RejectsBadURL(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("hook", "create",
		"--owner-kind=domain",
		"--owner-id=example.com",
		"--target-url=not-a-url")
	if err == nil {
		t.Fatalf("expected error for bad URL")
	}
	if !strings.Contains(err.Error(), "target_url") {
		t.Fatalf("expected target_url in error: %v", err)
	}
}

func TestCLIHookUpdate_TargetURL(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("hook", "create",
		"--owner-kind=domain",
		"--owner-id=ex.example",
		"--target-url=https://old.example/h",
		"--json")
	if err != nil {
		t.Fatalf("hook create: %v", err)
	}
	id := extractFirstJSONNumber(t, out, `"id":`)
	idStr := uintStr(id)

	if _, _, err := env.run("hook", "update", idStr,
		"--target-url=https://new.example/h"); err != nil {
		t.Fatalf("hook update: %v", err)
	}

	got, _, err := env.run("hook", "show", idStr, "--json")
	if err != nil {
		t.Fatalf("hook show: %v", err)
	}
	if !strings.Contains(got, "new.example") {
		t.Fatalf("update did not persist: %s", got)
	}
}

func TestCLIHookDelete(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("hook", "create",
		"--owner-kind=domain",
		"--owner-id=del.example",
		"--target-url=https://del.example/h",
		"--json")
	if err != nil {
		t.Fatalf("hook create: %v", err)
	}
	id := extractFirstJSONNumber(t, out, `"id":`)
	idStr := uintStr(id)
	if _, _, err := env.run("hook", "delete", idStr); err != nil {
		t.Fatalf("hook delete: %v", err)
	}
	if _, _, err := env.run("hook", "show", idStr); err == nil {
		t.Fatalf("expected 404 after delete")
	}
}

// TestCLIHookCreate_SyntheticExtracted exercises the REQ-HOOK-02 +
// REQ-HOOK-EXTRACTED-01..03 surface end-to-end through the CLI.
func TestCLIHookCreate_SyntheticExtracted(t *testing.T) {
	env := newCLITestEnv(t, nil)
	out, _, err := env.run("hook", "create",
		"--target-kind=synthetic",
		"--target-value=app.example.com",
		"--target-url=https://app.internal/v1/mail/inbound",
		"--body-mode=extracted",
		"--extracted-text-max-bytes=5242880",
		"--text-required",
		"--json")
	if err != nil {
		t.Fatalf("hook create: %v", err)
	}
	// JSON output may be pretty-printed; match value-bearing tokens
	// rather than tight-packed JSON.
	if !strings.Contains(out, `"target_kind"`) || !strings.Contains(out, `"synthetic"`) {
		t.Fatalf("target_kind not surfaced: %s", out)
	}
	if !strings.Contains(out, `"body_mode"`) || !strings.Contains(out, `"extracted"`) {
		t.Fatalf("body_mode not surfaced: %s", out)
	}
	if !strings.Contains(out, `"text_required"`) || !strings.Contains(out, "true") {
		t.Fatalf("text_required not surfaced: %s", out)
	}
	if !strings.Contains(out, `"extracted_text_max_bytes"`) || !strings.Contains(out, "5242880") {
		t.Fatalf("extracted_text_max_bytes not surfaced: %s", out)
	}
}

// TestCLIHookCreate_RejectsTextRequiredWithoutExtracted: the
// REQ-HOOK-EXTRACTED-03 drop policy only applies in extracted mode;
// any other body_mode + text_required is a validation failure.
func TestCLIHookCreate_RejectsTextRequiredWithoutExtracted(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("hook", "create",
		"--target-kind=domain",
		"--target-value=example.com",
		"--target-url=https://recv/",
		"--body-mode=inline",
		"--text-required")
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "text_required") {
		t.Fatalf("err = %v, want text_required mention", err)
	}
}

// TestCLIHookCreate_RejectsExtractedTextMaxOverCeiling: the
// REQ-HOOK-EXTRACTED-01 cap of 32 MiB is enforced at the REST/CLI
// boundary.
func TestCLIHookCreate_RejectsExtractedTextMaxOverCeiling(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("hook", "create",
		"--target-kind=synthetic",
		"--target-value=app.example.com",
		"--target-url=https://recv/",
		"--body-mode=extracted",
		"--extracted-text-max-bytes=999999999")
	if err == nil {
		t.Fatalf("expected ceiling error")
	}
}

// extractFirstJSONNumber pulls the first numeric value matching the prefix
// out of raw. Tolerant to whitespace; sufficient for "id" extraction in
// the small JSON bodies our tests inspect.
func extractFirstJSONNumber(t *testing.T, raw, prefix string) uint64 {
	t.Helper()
	idx := strings.Index(raw, prefix)
	if idx < 0 {
		t.Fatalf("prefix %q not found in: %s", prefix, raw)
	}
	tail := raw[idx+len(prefix):]
	tail = strings.TrimLeft(tail, " ")
	end := 0
	for end < len(tail) && tail[end] >= '0' && tail[end] <= '9' {
		end++
	}
	if end == 0 {
		t.Fatalf("no digits after %q in %s", prefix, raw)
	}
	var n uint64
	for i := 0; i < end; i++ {
		n = n*10 + uint64(tail[i]-'0')
	}
	return n
}
