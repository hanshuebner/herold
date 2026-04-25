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
	if !strings.Contains(out, "hmac_secret:") {
		t.Fatalf("expected hmac_secret line in output: %s", out)
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
