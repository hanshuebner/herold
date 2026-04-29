package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func newTestLogger(buf *bytes.Buffer, keys []string) *slog.Logger {
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(NewRedactHandler(base, keys))
}

func decodeOneLine(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &m); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, raw)
	}
	return m
}

func TestRedactHandler_RedactsKnownKeysCaseInsensitive(t *testing.T) {
	cases := []string{"password", "Password", "PASSWORD", "Authorization", "SET-COOKIE"}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			l := newTestLogger(&buf, DefaultSecretKeys)
			l.Info("login", key, "hunter2")
			got := decodeOneLine(t, buf.Bytes())
			if got[key] != RedactedValue {
				t.Fatalf("want %q redacted, got %q", key, got[key])
			}
		})
	}
}

func TestRedactHandler_LeavesNonSecretsAlone(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, DefaultSecretKeys)
	l.Info("login", "principal", "alice@example.com", "remote", "198.51.100.1")
	got := decodeOneLine(t, buf.Bytes())
	if got["principal"] != "alice@example.com" {
		t.Fatalf("principal modified: %v", got["principal"])
	}
	if got["remote"] != "198.51.100.1" {
		t.Fatalf("remote modified: %v", got["remote"])
	}
}

func TestRedactHandler_RecursesIntoGroups(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, DefaultSecretKeys)
	l.Info("auth",
		slog.Group("request",
			slog.String("method", "POST"),
			slog.String("Authorization", "Bearer abc"),
			slog.Group("headers",
				slog.String("cookie", "s=xyz"),
				slog.String("x-request-id", "r1"),
			),
		),
	)
	got := decodeOneLine(t, buf.Bytes())
	req, ok := got["request"].(map[string]any)
	if !ok {
		t.Fatalf("request not a group: %T", got["request"])
	}
	if req["method"] != "POST" {
		t.Fatalf("method changed: %v", req["method"])
	}
	if req["Authorization"] != RedactedValue {
		t.Fatalf("Authorization not redacted: %v", req["Authorization"])
	}
	hdrs, ok := req["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers not a group: %T", req["headers"])
	}
	if hdrs["cookie"] != RedactedValue {
		t.Fatalf("cookie not redacted: %v", hdrs["cookie"])
	}
	if hdrs["x-request-id"] != "r1" {
		t.Fatalf("x-request-id changed: %v", hdrs["x-request-id"])
	}
}

func TestRedactHandler_WithAttrsRedactsPreScoped(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, DefaultSecretKeys)
	scoped := l.With("password", "hunter2", "user", "bob")
	scoped.Info("hi")
	got := decodeOneLine(t, buf.Bytes())
	if got["password"] != RedactedValue {
		t.Fatalf("scoped password not redacted: %v", got["password"])
	}
	if got["user"] != "bob" {
		t.Fatalf("scoped user modified: %v", got["user"])
	}
}

func TestRedactHandler_EnabledForwards(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := NewRedactHandler(base, DefaultSecretKeys)
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatalf("Enabled should defer to base; info is below warn")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatalf("Enabled should be true for error")
	}
}

func TestRedactHandler_CustomKeys(t *testing.T) {
	var buf bytes.Buffer
	l := newTestLogger(&buf, []string{"dkim_private_key"})
	l.Info("load", "DKIM_PRIVATE_KEY", "pem...", "password", "kept")
	got := decodeOneLine(t, buf.Bytes())
	if got["DKIM_PRIVATE_KEY"] != RedactedValue {
		t.Fatalf("custom key not redacted: %v", got["DKIM_PRIVATE_KEY"])
	}
	if got["password"] != "kept" {
		// Default list is not applied when caller supplies a custom set.
		t.Fatalf("non-listed key unexpectedly redacted: %v", got["password"])
	}
}

func TestDefaultSecretKeys_Coverage(t *testing.T) {
	// Covers the base set plus the OAuth/credential additions for external
	// SMTP submission (Phase 6, REQ-OPS-84).
	want := []string{
		"password",
		"token",
		"access_token",
		"refresh_token",
		"xoauth2_token",
		"bearer_token",
		"api_key",
		"secret",
		"client_secret",
		"authorization",
		"cookie",
		"set-cookie",
	}
	joined := strings.Join(DefaultSecretKeys, ",")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("DefaultSecretKeys missing %q (got %q)", w, joined)
		}
	}
}

// TestDefaultSecretKeys_OAuthTokensRedacted is the targeted regression test
// for the XOAUTH2 / OAuth credential keys that must never appear in log
// output (Phase 6, architectural decision 7).
func TestDefaultSecretKeys_OAuthTokensRedacted(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{"access_token", "ya29.SECRET_ACCESS"},
		{"refresh_token", "1//SECRET_REFRESH"},
		{"xoauth2_token", "ya29.XOAUTH2_SENTINEL"},
		{"bearer_token", "Bearer-sentinel-value"},
		{"client_secret", "client-secret-value"},
		{"token", "generic-token-value"},
		{"password", "hunter2"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			var buf bytes.Buffer
			l := newTestLogger(&buf, DefaultSecretKeys)
			l.Info("credential-log", c.key, c.value)
			got := decodeOneLine(t, buf.Bytes())
			if got[c.key] == c.value {
				t.Fatalf("key %q was NOT redacted; value %q visible in log", c.key, c.value)
			}
			if got[c.key] != RedactedValue {
				t.Fatalf("key %q: want %q, got %v", c.key, RedactedValue, got[c.key])
			}
		})
	}
}
