package admin

// jmap_mail_handlers_test.go asserts that composeAdminAndUI correctly
// registers all JMAP Mail handler groups so that the Suite SPA can reach
// Mailbox/*, Email/*, Thread/*, SearchSnippet/*, and VacationResponse/* via
// POST /jmap on the public listener.
//
// Regression guard for the missing-handler bug: previously only
// Identity/* and EmailSubmission/* were wired; every other Mail method
// returned {"type":"unknownMethod"}.
//
// REQ-PROTO-40, REQ-PROTO-41.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// jmapBootstrapFixture boots the full server, creates the first admin
// principal via the bootstrap endpoint, and returns (publicAddr,
// apiKey, accountID).
//
// accountID is derived by calling GET /.well-known/jmap with the
// returned API key and reading primaryAccounts from the session
// descriptor, which mirrors the real SPA boot flow.
func jmapBootstrapFixture(t *testing.T) (publicAddr, apiKey, accountID string) {
	t.Helper()
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})

	publicAddr = addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap creates the first principal and returns initial_api_key.
	b, _ := json.Marshal(map[string]any{
		"email":        "jmap-test@example.com",
		"display_name": "JMAP Test",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &boot); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}
	if boot.InitialAPIKey == "" {
		t.Fatalf("bootstrap returned empty initial_api_key; body=%s", raw)
	}
	apiKey = boot.InitialAPIKey

	// Fetch the JMAP session descriptor to derive the accountId that the
	// client must supply in method calls.
	req, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	sessResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	defer sessResp.Body.Close()
	sessRaw, _ := io.ReadAll(sessResp.Body)
	if sessResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /.well-known/jmap: status=%d body=%s", sessResp.StatusCode, sessRaw)
	}
	var sess struct {
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
	}
	if err := json.Unmarshal(sessRaw, &sess); err != nil {
		t.Fatalf("session descriptor unmarshal: %v body=%s", err, sessRaw)
	}
	// The JMAP Mail capability URI is the standard one; any value in
	// primaryAccounts for this capability is our accountId.
	const capMail = "urn:ietf:params:jmap:mail"
	accountID = sess.PrimaryAccounts[capMail]
	if accountID == "" {
		t.Fatalf("no primaryAccounts entry for %q; session=%s", capMail, sessRaw)
	}
	return publicAddr, apiKey, accountID
}

// invokeJMAP posts a single method call to the public /jmap endpoint and
// returns the response invocation name and raw args. It fails the test on
// transport errors and when the response does not carry exactly one method
// response.
func invokeJMAP(t *testing.T, publicAddr, apiKey string, method string, args any) (string, json.RawMessage) {
	t.Helper()
	argsBytes, _ := json.Marshal(args)
	envelope := map[string]any{
		"using": []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		"methodCalls": []any{
			[]any{method, json.RawMessage(argsBytes), "t0"},
		},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/jmap",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /jmap %s: %v", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /jmap %s: status=%d body=%s", method, resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal response for %s: %v body=%s", method, err, raw)
	}
	if len(out.MethodResponses) != 1 {
		t.Fatalf("%s: got %d method responses, want 1; body=%s",
			method, len(out.MethodResponses), raw)
	}
	var name string
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		t.Fatalf("unmarshal invocation name for %s: %v", method, err)
	}
	return name, out.MethodResponses[0][1]
}

// TestComposeAdminAndUI_RegistersMailJMAPHandlers verifies that the full
// server registers the JMAP Mail handler groups that were absent before
// this fix. Each sub-test exercises one handler group; a response name of
// "error" (unknownMethod) is a failure.
//
// This test boots a real server via startTestServer so it exercises the
// exact composeAdminAndUI wiring path that the Suite SPA hits in
// production.
func TestComposeAdminAndUI_RegistersMailJMAPHandlers(t *testing.T) {
	publicAddr, apiKey, accountID := jmapBootstrapFixture(t)

	cases := []struct {
		method string
		args   map[string]any
	}{
		{
			method: "Mailbox/get",
			args:   map[string]any{"accountId": accountID},
		},
		{
			method: "Email/query",
			args:   map[string]any{"accountId": accountID},
		},
		{
			method: "Thread/get",
			args:   map[string]any{"accountId": accountID, "ids": []string{}},
		},
		{
			method: "SearchSnippet/get",
			args: map[string]any{
				"accountId": accountID,
				"filter":    map[string]any{"text": "test"},
				"emailIds":  []string{},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.method, func(t *testing.T) {
			name, raw := invokeJMAP(t, publicAddr, apiKey, tc.method, tc.args)
			if name == "error" {
				t.Fatalf("%s: got error response (handler not registered?): %s",
					tc.method, raw)
			}
			if name != tc.method {
				t.Fatalf("%s: response name = %q, want %q; body=%s",
					tc.method, name, tc.method, raw)
			}
		})
	}
}

// TestComposeAdminAndUI_RegistersVacationResponseHandlers verifies
// VacationResponse/* is mounted. The capability URI differs from JMAP Mail
// so the "using" list must include it explicitly.
func TestComposeAdminAndUI_RegistersVacationResponseHandlers(t *testing.T) {
	publicAddr, apiKey, accountID := jmapBootstrapFixture(t)

	argsBytes, _ := json.Marshal(map[string]any{"accountId": accountID})
	envelope := map[string]any{
		"using": []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
			"urn:ietf:params:jmap:vacationresponse",
		},
		"methodCalls": []any{
			[]any{"VacationResponse/get", json.RawMessage(argsBytes), "v0"},
		},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/jmap",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /jmap VacationResponse/get: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("VacationResponse/get: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal VacationResponse response: %v body=%s", err, raw)
	}
	if len(out.MethodResponses) != 1 {
		t.Fatalf("VacationResponse/get: got %d method responses, want 1; body=%s",
			len(out.MethodResponses), raw)
	}
	var name string
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		t.Fatalf("unmarshal invocation name: %v", err)
	}
	if name == "error" {
		t.Fatalf("VacationResponse/get: got error response (handler not registered?): %s",
			out.MethodResponses[0][1])
	}
	if name != "VacationResponse/get" {
		t.Fatalf("VacationResponse/get: response name = %q, want VacationResponse/get; body=%s",
			name, raw)
	}
}
