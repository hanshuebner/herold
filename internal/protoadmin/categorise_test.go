package protoadmin_test

// Tests for GET/PUT /api/v1/principals/{pid}/categorisation
// (REQ-FILT-210, REQ-FILT-211, REQ-FILT-212).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestCategorisationConfig_GetPut exercises the happy path for both verbs.
func TestCategorisationConfig_GetPut(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@cat.local")
	pid := h.createPrincipal(adminKey, "user@cat.local")

	path := fmt.Sprintf("/api/v1/principals/%d/categorisation", pid)

	// GET before any explicit PUT should return defaults seeded by the store.
	res, buf := h.doRequest("GET", path, adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET before PUT: %d: %s", res.StatusCode, buf)
	}
	var cfg map[string]any
	if err := json.Unmarshal(buf, &cfg); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, buf)
	}
	// The store seeds a non-empty category_set by default (REQ-FILT-210).
	cats, _ := cfg["category_set"].([]any)
	if len(cats) == 0 {
		t.Fatalf("expected non-empty category_set from defaults, got %v", cfg)
	}

	// PUT with a new prompt.
	putBody := map[string]any{
		"prompt":       "You are a mail sorter. Categorise the message.",
		"category_set": []map[string]any{{"name": "primary", "description": "primary inbox"}},
		"enabled":      true,
	}
	res, buf = h.doRequest("PUT", path, adminKey, putBody)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d: %s", res.StatusCode, buf)
	}
	var updated map[string]any
	if err := json.Unmarshal(buf, &updated); err != nil {
		t.Fatalf("unmarshal PUT resp: %v: %s", err, buf)
	}
	if got, _ := updated["prompt"].(string); got != putBody["prompt"] {
		t.Fatalf("prompt round-trip: got %q, want %q", got, putBody["prompt"])
	}
	if en, _ := updated["enabled"].(bool); !en {
		t.Fatalf("enabled round-trip: got false")
	}

	// GET after PUT should reflect the new prompt.
	res, buf = h.doRequest("GET", path, adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET after PUT: %d: %s", res.StatusCode, buf)
	}
	var got2 map[string]any
	if err := json.Unmarshal(buf, &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p, _ := got2["prompt"].(string); p != putBody["prompt"] {
		t.Fatalf("GET after PUT: prompt = %q, want %q", p, putBody["prompt"])
	}
}

// TestCategorisationConfig_AdminGating verifies that non-admin callers get 403.
func TestCategorisationConfig_AdminGating(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@cat2.local")
	pid := h.createPrincipal(adminKey, "nonadmin@cat2.local")

	// Create an API key for the non-admin principal.
	res, buf := h.doRequest("POST",
		fmt.Sprintf("/api/v1/principals/%d/api-keys", pid),
		adminKey,
		map[string]any{"label": "test key"},
	)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create api key: %d: %s", res.StatusCode, buf)
	}
	var keyResp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(buf, &keyResp); err != nil {
		t.Fatalf("unmarshal key: %v", err)
	}
	nonAdminKey := keyResp.Key

	path := fmt.Sprintf("/api/v1/principals/%d/categorisation", pid)

	// GET as non-admin: expect 403.
	res, _ = h.doRequest("GET", path, nonAdminKey, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("GET non-admin: want 403, got %d", res.StatusCode)
	}

	// PUT as non-admin: expect 403.
	res, _ = h.doRequest("PUT", path, nonAdminKey, map[string]any{
		"prompt":       "x",
		"category_set": []any{},
		"enabled":      false,
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT non-admin: want 403, got %d", res.StatusCode)
	}
}

// TestCategorisationConfig_APIKeyEnvValidation checks that inline secret
// values are rejected and reference forms are accepted (STANDARDS §9).
func TestCategorisationConfig_APIKeyEnvValidation(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@cat3.local")
	pid := h.createPrincipal(adminKey, "user@cat3.local")
	path := fmt.Sprintf("/api/v1/principals/%d/categorisation", pid)

	cases := []struct {
		name       string
		apiKeyEnv  string
		wantStatus int
	}{
		{"inline secret", "sk-verysecret", http.StatusBadRequest},
		{"dollar-var accepted", "$OPENAI_KEY", http.StatusOK},
		{"file-path accepted", "file:/run/secrets/openai", http.StatusOK},
		{"empty string accepted", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"prompt":       "sort mail",
				"category_set": []any{},
				"enabled":      false,
				"api_key_env":  tc.apiKeyEnv,
			}
			if tc.apiKeyEnv == "" {
				// omit the field entirely rather than sending null
				delete(body, "api_key_env")
			}
			res, buf := h.doRequest("PUT", path, adminKey, body)
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("api_key_env=%q: want %d, got %d: %s",
					tc.apiKeyEnv, tc.wantStatus, res.StatusCode, buf)
			}
		})
	}
}

// TestCategorisationConfig_MalformedBody verifies 400 on bad JSON.
func TestCategorisationConfig_MalformedBody(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@cat4.local")
	pid := h.createPrincipal(adminKey, "user@cat4.local")
	path := fmt.Sprintf("/api/v1/principals/%d/categorisation", pid)

	// Send raw bytes that are not valid JSON.
	req, _ := http.NewRequest("PUT", h.baseURL+path,
		strings.NewReader("not json {{{"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminKey)
	res, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body: want 400, got %d", res.StatusCode)
	}
}

// TestCategorisationConfig_Unauthenticated verifies 401 with no token.
func TestCategorisationConfig_Unauthenticated(t *testing.T) {
	h := newHarness(t)
	_, adminKey := h.bootstrap("admin@cat5.local")
	pid := h.createPrincipal(adminKey, "user@cat5.local")
	path := fmt.Sprintf("/api/v1/principals/%d/categorisation", pid)

	res, _ := h.doRequest("GET", path, "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth GET: want 401, got %d", res.StatusCode)
	}
}
