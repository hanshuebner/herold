package admin

// spam_transparency_wiring_e2e_test.go is the acceptance test for issue
// #464: LLMTransparency/get's spamPrompt must report the prompt actually
// dispatched to the configured spam/classifier plugin, not a permanently
// empty field. A unit test that injects a protoadmin.SpamPolicyStore
// directly into the llmtransparency handler would have passed all along
// while the deployed server showed nothing, because the nil was supplied
// at the admin.StartServer registration site rather than in the handler
// itself. This test therefore boots the real admin.StartServer (the same
// wiring path composeAdminAndUI uses to build spamPolicyStore from
// cfg.Plugin + the plugin.Manager) with a real classifierfixture child
// process and reads the answer back over the real /jmap HTTP endpoint
// (STANDARDS section 8, no mocks at the process boundary).
//
// classifierfixture's own manifest type -- not the operator's system.toml
// `type` string -- decides which of the two prompt-override options the
// server would actually dispatch (internal/spam.Classifier, issue #304
// Decision 3); the two subtests below configure both option keys at once
// and assert that only the one matching the plugin's declared manifest
// type comes back, proving the wiring reads the live decision rather than
// a hardcoded key.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// classifierPluginTOMLWithPrompts builds a [[plugin]] block for
// classifierfixture carrying both prompt-override option keys plus a
// model, so a test can assert exactly one of the two keys reaches the
// transparency panel depending on the plugin's declared manifest type.
func classifierPluginTOMLWithPrompts(pluginPath, pluginType, classifyPrompt, spamPrompt, model string) string {
	return fmt.Sprintf(`
[[plugin]]
name = "spam"
type = %q
path = %q
lifecycle = "long-running"
options.classify_system_prompt_override = %q
options.system_prompt_override = %q
options.model = %q
`, pluginType, pluginPath, classifyPrompt, spamPrompt, model)
}

// callLLMTransparencyGet issues a real POST /jmap LLMTransparency/get call
// and returns the decoded first list entry.
func callLLMTransparencyGet(t *testing.T, publicAddr, apiKey, accountID string) map[string]any {
	t.Helper()
	args := map[string]any{"accountId": accountID}
	argsBytes, _ := json.Marshal(args)
	envelope := map[string]any{
		"using": []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
			"https://netzhansa.com/jmap/llm-transparency",
		},
		"methodCalls": []any{
			[]any{"LLMTransparency/get", json.RawMessage(argsBytes), "t0"},
		},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, "http://"+publicAddr+"/jmap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /jmap LLMTransparency/get: %v", err)
	}
	raw, _ := readAllAndClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /jmap LLMTransparency/get: status=%d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		MethodResponses []json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode jmap response: %v body=%s", err, raw)
	}
	if len(out.MethodResponses) == 0 {
		t.Fatalf("no method responses: %s", raw)
	}
	var tuple []json.RawMessage
	if err := json.Unmarshal(out.MethodResponses[0], &tuple); err != nil || len(tuple) < 2 {
		t.Fatalf("decode method response tuple: %v body=%s", err, raw)
	}
	var methodName string
	_ = json.Unmarshal(tuple[0], &methodName)
	if methodName != "LLMTransparency/get" {
		t.Fatalf("expected LLMTransparency/get response, got %q: %s", methodName, tuple[1])
	}
	var result struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(tuple[1], &result); err != nil {
		t.Fatalf("decode LLMTransparency/get result: %v body=%s", err, tuple[1])
	}
	if len(result.List) == 0 {
		t.Fatalf("LLMTransparency/get returned an empty list: %s", tuple[1])
	}
	return result.List[0]
}

// TestSpamTransparency_E2E_ReportsPromptOfConfiguredClassifierPlugin boots
// a real admin.StartServer with a "classifier"-typed plugin block carrying
// distinct text under both classify_system_prompt_override and
// system_prompt_override, and asserts LLMTransparency/get -- driven over
// the real /jmap HTTP endpoint of the composed server -- returns the
// classify_system_prompt_override text: the option internal/spam.Classifier
// actually dispatches to a plugin whose manifest declares TypeClassifier
// (classifierfixture's default). The system_prompt_override text, which
// would never reach the model on this path, must NOT appear.
func TestSpamTransparency_E2E_ReportsPromptOfConfiguredClassifierPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	const wantPrompt = "classifier-path prompt: flag anything mentioning free money"
	const decoyPrompt = "spam-path prompt: should never surface here"
	const wantModel = "wiring-test-model"

	pluginTOML := classifierPluginTOMLWithPrompts(fixturePath, "classifier", wantPrompt, decoyPrompt, wantModel)
	h := startClassifyMatrixServer(t, "sqlite", "", pluginTOML, 0)

	st := h.openVerifyStore(t)
	apiKey := insertE2EAPIKey(t, st, h.pid)
	_ = st.Close()

	accountID := jmapAccountID(t, h.publicAddr, apiKey)
	entry := callLLMTransparencyGet(t, h.publicAddr, apiKey, accountID)

	gotPrompt, _ := entry["spamPrompt"].(string)
	if gotPrompt != wantPrompt {
		t.Fatalf("spamPrompt = %q, want %q (the classify_system_prompt_override text: issue #464)", gotPrompt, wantPrompt)
	}
	if gotPrompt == decoyPrompt {
		t.Fatalf("spamPrompt leaked the system_prompt_override text, which this plugin's manifest type never dispatches")
	}
	spamModel, _ := entry["spamModel"].(map[string]any)
	if spamModel == nil {
		t.Fatalf("entry has no spamModel sub-object: %+v", entry)
	}
	if gotModel, _ := spamModel["modelName"].(string); gotModel != wantModel {
		t.Fatalf("spamModel.modelName = %q, want %q", gotModel, wantModel)
	}
}

// TestSpamTransparency_E2E_LegacySpamTypeReadsSpamPromptOption is the
// mirror case (issue #304 Decision 3): a plugin whose manifest declares
// the legacy TypeSpam reads system_prompt_override, even though the
// operator's system.toml [[plugin]] block still says type = "spam"
// alongside a classify_system_prompt_override that would never reach the
// model on this path.
func TestSpamTransparency_E2E_LegacySpamTypeReadsSpamPromptOption(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	fixturePath := buildClassifierFixture(t)
	t.Setenv("HEROLD_TEST_CLASSIFY_PLUGIN_TYPE", "spam")
	const wantPrompt = "legacy spam-type prompt: score urgency language highly"
	const decoyPrompt = "classifier-path prompt: should never surface here"

	pluginTOML := classifierPluginTOMLWithPrompts(fixturePath, "spam", decoyPrompt, wantPrompt, "")
	h := startClassifyMatrixServer(t, "sqlite", "", pluginTOML, 0)

	st := h.openVerifyStore(t)
	apiKey := insertE2EAPIKey(t, st, h.pid)
	_ = st.Close()

	accountID := jmapAccountID(t, h.publicAddr, apiKey)
	entry := callLLMTransparencyGet(t, h.publicAddr, apiKey, accountID)

	gotPrompt, _ := entry["spamPrompt"].(string)
	if gotPrompt != wantPrompt {
		t.Fatalf("spamPrompt = %q, want %q (the system_prompt_override text a TypeSpam manifest reads)", gotPrompt, wantPrompt)
	}
	if gotPrompt == decoyPrompt {
		t.Fatalf("spamPrompt leaked the classify_system_prompt_override text, which this plugin's manifest type never dispatches")
	}
}

// TestSpamTransparency_E2E_NoPluginReportsEmptyNotBroken is the
// "genuinely nothing configured" control: with no [[plugin]] block at
// all, LLMTransparency/get must still answer (the capability is always
// advertised) with an empty spamPrompt -- the same string the panel
// showed before this fix, but now because it is true rather than because
// the dependency was hardcoded nil.
func TestSpamTransparency_E2E_NoPluginReportsEmptyNotBroken(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e wiring test")
	}
	h := startClassifyMatrixServer(t, "sqlite", "", "", 0)

	st := h.openVerifyStore(t)
	apiKey := insertE2EAPIKey(t, st, h.pid)
	_ = st.Close()

	accountID := jmapAccountID(t, h.publicAddr, apiKey)
	entry := callLLMTransparencyGet(t, h.publicAddr, apiKey, accountID)

	if gotPrompt, _ := entry["spamPrompt"].(string); gotPrompt != "" {
		t.Fatalf("spamPrompt = %q, want empty with no plugin configured", gotPrompt)
	}
}
