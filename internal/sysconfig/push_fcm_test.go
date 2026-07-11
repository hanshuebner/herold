package sysconfig

// push_fcm_test.go — validation coverage for [server.push]'s FCM
// service-account fields (re #200), mirroring the VAPID secret-ref
// rules the same section already enforces.

import (
	"strings"
	"testing"
)

func TestPushConfig_FCM_MutuallyExclusive(t *testing.T) {
	toml := minimalNoObs + `
[server.push]
fcm_service_account_json_env  = "$HEROLD_FCM_SERVICE_ACCOUNT_JSON"
fcm_service_account_json_file = "/etc/herold/fcm-service-account.json"
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for fcm env+file set together, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutually exclusive, got: %v", err)
	}
}

func TestPushConfig_FCM_EnvMustStartWithDollar(t *testing.T) {
	toml := minimalNoObs + `
[server.push]
fcm_service_account_json_env = "HEROLD_FCM_SERVICE_ACCOUNT_JSON"
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for a bare env var name, got nil")
	}
	if !strings.Contains(err.Error(), `must start with "$"`) {
		t.Errorf("error should mention the \"$\" requirement, got: %v", err)
	}
}

func TestPushConfig_FCM_FileMustBeAbsolute(t *testing.T) {
	toml := minimalNoObs + `
[server.push]
fcm_service_account_json_file = "relative/fcm-service-account.json"
`
	_, err := Parse([]byte(toml))
	if err == nil {
		t.Fatal("expected error for a relative file path, got nil")
	}
	if !strings.Contains(err.Error(), "absolute path") {
		t.Errorf("error should mention absolute path, got: %v", err)
	}
}

func TestPushConfig_FCM_UnconfiguredIsValid(t *testing.T) {
	// No fcm_* fields at all: a valid, Web-Push-only (or push-less)
	// posture. FCMServiceAccountJSONRef must report "" so the admin
	// wiring skips constructing an fcm.Sender.
	cfg, err := Parse([]byte(minimalNoObs))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ref := cfg.Server.Push.FCMServiceAccountJSONRef(); ref != "" {
		t.Errorf("FCMServiceAccountJSONRef() = %q, want empty when unconfigured", ref)
	}
}

func TestPushConfig_FCM_EnvRefResolves(t *testing.T) {
	toml := minimalNoObs + `
[server.push]
fcm_service_account_json_env = "$HEROLD_FCM_SERVICE_ACCOUNT_JSON"
fcm_project_id = "my-firebase-project"
`
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ref := cfg.Server.Push.FCMServiceAccountJSONRef(); ref != "$HEROLD_FCM_SERVICE_ACCOUNT_JSON" {
		t.Errorf("FCMServiceAccountJSONRef() = %q", ref)
	}
	if cfg.Server.Push.FCMProjectID != "my-firebase-project" {
		t.Errorf("FCMProjectID = %q", cfg.Server.Push.FCMProjectID)
	}
}
