package protojmap_test

// device_token_grant_test.go verifies the end-to-end bearer-token grant
// for native clients (issue #199): a token minted by
// internal/directory.Directory.IssueDeviceToken -- the same credential-
// exchange grant POST /api/v1/auth/device-token drives -- authenticates
// against the JMAP session endpoint exactly like an operator-issued API
// key, with zero changes to protojmap's Bearer-verification path.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/store"
)

func TestSession_AcceptsDeviceToken(t *testing.T) {
	f := newFixture(t)

	plaintext, key, err := f.dir.IssueDeviceToken(context.Background(),
		"alice@example.com", "correct-horse-battery-staple-1", "", "Pixel 8")
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, directory.DeviceTokenPrefix) {
		t.Fatalf("device token missing Bearer prefix: %q", plaintext)
	}

	res, body := f.doRequest("GET", "/.well-known/jmap", plaintext, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	var desc map[string]any
	if err := json.Unmarshal(body, &desc); err != nil {
		t.Fatalf("decode session descriptor: %v", err)
	}
	if _, ok := desc["accounts"]; !ok {
		t.Fatalf("session descriptor missing accounts: %v", desc)
	}

	// Revocation: deleting the underlying APIKey row (the existing
	// self-service DELETE /api/v1/api-keys/{id} path) immediately makes
	// the device token unusable on JMAP too.
	if err := f.store.Meta().DeleteAPIKey(context.Background(), key.ID); err != nil {
		t.Fatalf("revoke device token: %v", err)
	}
	res2, _ := f.doRequest("GET", "/.well-known/jmap", plaintext, nil)
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked device token should be rejected: status = %d, want 401", res2.StatusCode)
	}
}

func TestSession_RejectsDeviceTokenForDisabledPrincipal(t *testing.T) {
	f := newFixture(t)
	plaintext, _, err := f.dir.IssueDeviceToken(context.Background(),
		"alice@example.com", "correct-horse-battery-staple-1", "", "laptop")
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}

	p, err := f.store.Meta().GetPrincipalByID(context.Background(), f.pid)
	if err != nil {
		t.Fatalf("load principal: %v", err)
	}
	p.Flags |= store.PrincipalFlagDisabled
	if err := f.store.Meta().UpdatePrincipal(context.Background(), p); err != nil {
		t.Fatalf("disable principal: %v", err)
	}

	res, _ := f.doRequest("GET", "/.well-known/jmap", plaintext, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled principal's device token should be rejected: status = %d, want 401", res.StatusCode)
	}
}
