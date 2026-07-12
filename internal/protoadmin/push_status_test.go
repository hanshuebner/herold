package protoadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/directoryoidc"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/storesqlite/sqlitetest"
	"github.com/hanshuebner/herold/internal/sysconfig"
	"github.com/hanshuebner/herold/internal/testharness"
)

// newHarnessWithPush is a variant of newHarness that threads a
// *sysconfig.PushConfig into protoadmin.Options.Push (re #200), so the
// read-only push-status surface at GET /api/v1/server/status can be
// exercised against both an unconfigured and a configured [server.push]
// block without touching the shared newHarness constructor other tests
// depend on.
func newHarnessWithPush(t *testing.T, push *sysconfig.PushConfig) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fs := sqlitetest.Open(t, clk)
	h, _ := testharness.Start(t, testharness.Options{
		Store: fs,
		Clock: clk,
		Listeners: []testharness.ListenerSpec{
			{Name: "admin", Protocol: "http"},
		},
	})
	dir := directory.New(fs.Meta(), nil, clk, nil)
	rp := directoryoidc.New(fs.Meta(), nil, &http.Client{Timeout: 5 * time.Second}, clk)
	srv := protoadmin.NewServer(fs, dir, rp, nil, clk, protoadmin.Options{
		BootstrapPerWindow:      1,
		BootstrapWindow:         5 * time.Minute,
		RequestsPerMinutePerKey: 100,
		Push:                    push,
	})
	if err := h.AttachAdmin("admin", srv, protoadmin.ListenerModePlain); err != nil {
		t.Fatalf("AttachAdmin: %v", err)
	}
	client, base := h.DialAdminByName(context.Background(), "admin")
	return &harness{
		t: t, h: h, srv: srv, client: client, baseURL: base,
		clk: clk, dir: dir, rp: rp,
	}
}

// TestServerStatus_PushStatus_Unconfigured asserts that an absent
// [server.push] block (nil Options.Push, and a present-but-empty
// PushConfig) reports both transports as not configured -- the
// dev-instance / zero-config posture (re #200).
func TestServerStatus_PushStatus_Unconfigured(t *testing.T) {
	for _, push := range []*sysconfig.PushConfig{nil, {}} {
		h := newHarnessWithPush(t, push)
		_, key := h.bootstrap("admin@example.com")

		res, buf := h.doRequest("GET", "/api/v1/server/status", key, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("server/status: %d: %s", res.StatusCode, buf)
		}
		var out struct {
			Push struct {
				WebPushConfigured bool `json:"webpush_configured"`
				FCMConfigured     bool `json:"fcm_configured"`
			} `json:"push"`
		}
		if err := json.Unmarshal(buf, &out); err != nil {
			t.Fatalf("unmarshal: %v: %s", err, buf)
		}
		if out.Push.WebPushConfigured {
			t.Errorf("webpush_configured = true, want false (push=%#v)", push)
		}
		if out.Push.FCMConfigured {
			t.Errorf("fcm_configured = true, want false (push=%#v)", push)
		}
	}
}

// TestServerStatus_PushStatus_Configured asserts that a
// [server.push] block with credential references set reports both
// transports as configured, and that the credential values themselves
// never appear in the response (re #200: the FCM/VAPID secrets are
// system config, read-only through this endpoint).
func TestServerStatus_PushStatus_Configured(t *testing.T) {
	push := &sysconfig.PushConfig{
		VAPIDPrivateKeyEnv:       "HEROLD_TEST_VAPID_KEY",
		FCMServiceAccountJSONEnv: "HEROLD_TEST_FCM_JSON",
	}
	h := newHarnessWithPush(t, push)
	_, key := h.bootstrap("admin@example.com")

	res, buf := h.doRequest("GET", "/api/v1/server/status", key, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("server/status: %d: %s", res.StatusCode, buf)
	}
	var out struct {
		Push struct {
			WebPushConfigured bool `json:"webpush_configured"`
			FCMConfigured     bool `json:"fcm_configured"`
		} `json:"push"`
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	if !out.Push.WebPushConfigured {
		t.Error("webpush_configured = false, want true")
	}
	if !out.Push.FCMConfigured {
		t.Error("fcm_configured = false, want true")
	}
	if strings.Contains(string(buf), "HEROLD_TEST_VAPID_KEY") || strings.Contains(string(buf), "HEROLD_TEST_FCM_JSON") {
		t.Errorf("server/status response leaked the credential env var name: %s", buf)
	}
}
