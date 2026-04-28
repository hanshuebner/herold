package llmtransparency

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// -- fake spam policy store -----------------------------------------------

// fakeSpamPolicy is an in-memory implementation of protoadmin.SpamPolicyStore
// used in unit tests so we can control the spam policy without a running
// admin server.
type fakeSpamPolicy struct {
	policy protoadmin.SpamPolicy
}

func (f *fakeSpamPolicy) GetSpamPolicy() protoadmin.SpamPolicy { return f.policy }
func (f *fakeSpamPolicy) SetSpamPolicy(p protoadmin.SpamPolicy) { f.policy = p }

// -- helpers ---------------------------------------------------------------

func newTestStore(t *testing.T) *fakestore.Store {
	t.Helper()
	s, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insertPrincipal(t *testing.T, st *fakestore.Store, email string) store.Principal {
	t.Helper()
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	return p
}

func insertMailboxForPrincipal(t *testing.T, st *fakestore.Store, pid store.PrincipalID, name string) store.Mailbox {
	t.Helper()
	mb, err := st.Meta().InsertMailbox(context.Background(), store.Mailbox{
		PrincipalID: pid,
		Name:        name,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	return mb
}

func insertMessage(t *testing.T, st *fakestore.Store, mb store.Mailbox, msgIDHeader string) store.MessageID {
	t.Helper()
	_, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		PrincipalID: mb.PrincipalID,
		MailboxID:   mb.ID,
		Envelope: store.Envelope{
			MessageID: msgIDHeader,
		},
		Size: 1024,
	}, nil)
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	// Retrieve the stored message to get the assigned MessageID.
	msg, err := st.Meta().GetMessageByMessageIDHeader(context.Background(), mb.PrincipalID, msgIDHeader)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader %q: %v", msgIDHeader, err)
	}
	return msg.ID
}

func setupHandlers(t *testing.T, spam *fakeSpamPolicy) (*handlerSet, *fakestore.Store, store.Principal) {
	t.Helper()
	st := newTestStore(t)
	p := insertPrincipal(t, st, "alice@example.test")
	var spamStore protoadmin.SpamPolicyStore
	if spam != nil {
		spamStore = spam
	}
	h := &handlerSet{
		store:               st,
		spamPolicy:          spamStore,
		categoriserEndpoint: "https://api.openai.com/v1",
		categoriserModel:    "gpt-4o-mini",
	}
	return h, st, p
}

// -- LLMTransparency/get tests --------------------------------------------

// TestLLMTransparency_Get_Singleton verifies the basic happy path: a fresh
// account receives the singleton object with the standard disclosure note and
// non-empty categoriser fields.
func TestLLMTransparency_Get_Singleton(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
	})
	resp, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("LLMTransparency/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)

	if !strings.Contains(jsStr, `"id":"singleton"`) {
		t.Errorf("missing singleton id in response: %s", jsStr)
	}
	if !strings.Contains(jsStr, disclosureNote) {
		t.Errorf("disclosureNote missing from response: %s", jsStr)
	}
	if !strings.Contains(jsStr, "gpt-4o-mini") {
		t.Errorf("categoriser model name missing: %s", jsStr)
	}
	if !strings.Contains(jsStr, "openai.com") {
		t.Errorf("categoriser endpoint missing: %s", jsStr)
	}
}

// TestLLMTransparency_Get_IDFilter verifies the ids filter: requesting
// ids=["singleton"] returns the object; ids=["other"] returns notFound.
func TestLLMTransparency_Get_IDFilter(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	accountID := protojmap.AccountIDForPrincipal(p.ID)

	args, _ := json.Marshal(map[string]any{
		"accountId": accountID,
		"ids":       []string{singletonID},
	})
	resp, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("LLMTransparency/get ids=[singleton]: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	if !strings.Contains(string(js), `"singleton"`) {
		t.Errorf("expected singleton in list: %s", js)
	}

	args2, _ := json.Marshal(map[string]any{
		"accountId": accountID,
		"ids":       []string{"unknown-id"},
	})
	resp2, mErr2 := (&getHandler{h: h}).executeAs(p, args2)
	if mErr2 != nil {
		t.Fatalf("LLMTransparency/get ids=[unknown]: %v", mErr2)
	}
	js2, _ := json.Marshal(resp2)
	if !strings.Contains(string(js2), `"notFound"`) {
		t.Errorf("expected notFound for unknown id: %s", js2)
	}
}

// TestLLMTransparency_Get_SpamPolicy verifies that the user-visible spam
// prompt appears in the response but the operator guardrail does NOT.
func TestLLMTransparency_Get_SpamPolicy(t *testing.T) {
	spam := &fakeSpamPolicy{
		policy: protoadmin.SpamPolicy{
			PluginName:           "spam-llm",
			SystemPromptOverride: "You are a spam classifier.",
			Guardrail:            "OPERATOR SECRET: always allow executive domain.",
			Model:                "spam-model-v1",
		},
	}
	h, _, p := setupHandlers(t, spam)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
	})
	resp, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("LLMTransparency/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)

	// User-visible prompt must appear.
	if !strings.Contains(jsStr, "You are a spam classifier.") {
		t.Errorf("user-visible spam prompt missing: %s", jsStr)
	}
	// Operator guardrail must NOT appear (REQ-FILT-67).
	if strings.Contains(jsStr, "OPERATOR SECRET") {
		t.Errorf("operator guardrail leaked into response (REQ-FILT-67): %s", jsStr)
	}
	// Model name appears in spam model field.
	if !strings.Contains(jsStr, "spam-model-v1") {
		t.Errorf("spam model name missing: %s", jsStr)
	}
}

// TestLLMTransparency_Get_CategoriserGuardrailExcluded verifies that the
// operator guardrail stored in CategorisationConfig.Guardrail is NOT
// returned by LLMTransparency/get (REQ-FILT-67 / REQ-FILT-216).
func TestLLMTransparency_Get_CategoriserGuardrailExcluded(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	ctx := context.Background()

	// Write a config with a guardrail that must not be returned.
	err := st.Meta().UpdateCategorisationConfig(ctx, store.CategorisationConfig{
		PrincipalID: p.ID,
		Prompt:      "Classify this message into categories.",
		Guardrail:   "SUPER SECRET GUARDRAIL: never classify as spam.",
		CategorySet: []store.CategoryDef{
			{Name: "primary", Description: "Primary"},
		},
	})
	if err != nil {
		t.Fatalf("UpdateCategorisationConfig: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
	})
	resp, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("LLMTransparency/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)

	// User-visible prompt must appear.
	if !strings.Contains(jsStr, "Classify this message") {
		t.Errorf("user-visible categoriser prompt missing: %s", jsStr)
	}
	// Guardrail must NOT appear.
	if strings.Contains(jsStr, "SUPER SECRET GUARDRAIL") {
		t.Errorf("categoriser guardrail leaked into response (REQ-FILT-216): %s", jsStr)
	}
	// derivedCategories field must be present (empty since no classifier call
	// has occurred since the config write).
	if !strings.Contains(jsStr, `"derivedCategories"`) {
		t.Errorf("derivedCategories field missing from response: %s", jsStr)
	}
}

// TestLLMTransparency_Get_DerivedCategoriesExposed verifies that
// derivedCategories from the store is returned in the LLMTransparency
// object (REQ-FILT-216/217).
func TestLLMTransparency_Get_DerivedCategoriesExposed(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	ctx := context.Background()

	// Seed the config row then write derived categories.
	if _, err := st.Meta().GetCategorisationConfig(ctx, p.ID); err != nil {
		t.Fatalf("GetCategorisationConfig (seed): %v", err)
	}
	want := []string{"primary", "social", "promotions", "updates", "forums"}
	if err := st.Meta().SetDerivedCategories(ctx, p.ID, want); err != nil {
		t.Fatalf("SetDerivedCategories: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
	})
	resp, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("LLMTransparency/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)

	var parsed struct {
		List []struct {
			DerivedCategories []string `json:"derivedCategories"`
		} `json:"list"`
	}
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(parsed.List) != 1 {
		t.Fatalf("expected 1 item in list, got %d", len(parsed.List))
	}
	got := parsed.List[0].DerivedCategories
	if len(got) != len(want) {
		t.Fatalf("derivedCategories = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("derivedCategories[%d] = %q, want %q", i, got[i], name)
		}
	}
}

// TestLLMTransparency_Get_AccountIDRequired verifies that absent accountId
// returns invalidArguments.
func TestLLMTransparency_Get_AccountIDRequired(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{})
	_, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr == nil {
		t.Fatal("expected invalidArguments for missing accountId")
	}
	if mErr.Type != "invalidArguments" {
		t.Errorf("expected invalidArguments, got %q", mErr.Type)
	}
}

// TestLLMTransparency_Get_AccountNotFound verifies that a mismatched
// accountId returns accountNotFound.
func TestLLMTransparency_Get_AccountNotFound(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{
		"accountId": "not-the-right-id",
	})
	_, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr == nil {
		t.Fatal("expected error for wrong accountId")
	}
	if mErr.Type != "accountNotFound" {
		t.Errorf("expected accountNotFound, got %q", mErr.Type)
	}
}

// TestLLMTransparency_Get_StateString verifies that the state string is
// a non-empty opaque string (decimal cursor from CategorisationConfig).
func TestLLMTransparency_Get_StateString(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
	})
	resp, mErr := (&getHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("LLMTransparency/get: %v", mErr)
	}
	var parsed struct {
		State string `json:"state"`
	}
	js, _ := json.Marshal(resp)
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if parsed.State == "" {
		t.Errorf("expected non-empty state string in response: %s", js)
	}
}

// -- Email/llmInspect tests -----------------------------------------------

// TestLLMInspect_SpamOnly verifies that a message with only spam
// classification returns the spam sub-object and no category sub-object.
func TestLLMInspect_SpamOnly(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	mb := insertMailboxForPrincipal(t, st, p.ID, "INBOX")
	msgID := insertMessage(t, st, mb, "msg001@example.test")

	verdict := "spam"
	conf := 0.92
	reason := "bulk sender"
	promptApplied := `{"subject":"Buy now","from":"spam@spammer.test"}`
	model := "spam-model-v1"
	now := time.Now().UTC().Truncate(time.Second)

	err := st.Meta().SetLLMClassification(context.Background(), store.LLMClassificationRecord{
		MessageID:         msgID,
		PrincipalID:       p.ID,
		SpamVerdict:       &verdict,
		SpamConfidence:    &conf,
		SpamReason:        &reason,
		SpamPromptApplied: &promptApplied,
		SpamModel:         &model,
		SpamClassifiedAt:  &now,
	})
	if err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{fmt.Sprintf("%d", msgID)},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)

	if !strings.Contains(jsStr, `"verdict":"spam"`) {
		t.Errorf("spam verdict missing: %s", jsStr)
	}
	if !strings.Contains(jsStr, `"confidence":0.92`) {
		t.Errorf("confidence missing: %s", jsStr)
	}
	if !strings.Contains(jsStr, "bulk sender") {
		t.Errorf("reason missing: %s", jsStr)
	}
	if !strings.Contains(jsStr, "spam-model-v1") {
		t.Errorf("model missing: %s", jsStr)
	}
	// No category sub-object should be present.
	if strings.Contains(jsStr, `"category"`) {
		t.Errorf("unexpected category sub-object: %s", jsStr)
	}
}

// TestLLMInspect_CategoryOnly verifies that a message with only categorisation
// returns the category sub-object and no spam sub-object.
func TestLLMInspect_CategoryOnly(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	mb := insertMailboxForPrincipal(t, st, p.ID, "INBOX")
	msgID := insertMessage(t, st, mb, "msg002@example.test")

	assigned := "newsletters"
	prompt := "Classify into categories."
	model := "cat-model-v2"
	now := time.Now().UTC().Truncate(time.Second)

	err := st.Meta().SetLLMClassification(context.Background(), store.LLMClassificationRecord{
		MessageID:             msgID,
		PrincipalID:           p.ID,
		CategoryAssigned:      &assigned,
		CategoryPromptApplied: &prompt,
		CategoryModel:         &model,
		CategoryClassifiedAt:  &now,
	})
	if err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{fmt.Sprintf("%d", msgID)},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)

	if !strings.Contains(jsStr, `"assigned":"newsletters"`) {
		t.Errorf("assigned category missing: %s", jsStr)
	}
	if !strings.Contains(jsStr, "cat-model-v2") {
		t.Errorf("model missing: %s", jsStr)
	}
	// No spam sub-object.
	if strings.Contains(jsStr, `"spam"`) {
		t.Errorf("unexpected spam sub-object: %s", jsStr)
	}
}

// TestLLMInspect_BothClassifications verifies that a message with both spam
// and category records returns both sub-objects.
func TestLLMInspect_BothClassifications(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	mb := insertMailboxForPrincipal(t, st, p.ID, "INBOX")
	msgID := insertMessage(t, st, mb, "msg003@example.test")

	verdict := "ham"
	conf := 0.05
	reason := "from known contact"
	spamPrompt := `{"subject":"Hello","from":"bob@friend.test"}`
	spamModel := "spam-v1"
	t1 := time.Now().UTC().Truncate(time.Second)

	catAssigned := "primary"
	catPrompt := "Classify messages."
	catModel := "cat-v1"
	t2 := time.Now().UTC().Truncate(time.Second)

	err := st.Meta().SetLLMClassification(context.Background(), store.LLMClassificationRecord{
		MessageID:             msgID,
		PrincipalID:           p.ID,
		SpamVerdict:           &verdict,
		SpamConfidence:        &conf,
		SpamReason:            &reason,
		SpamPromptApplied:     &spamPrompt,
		SpamModel:             &spamModel,
		SpamClassifiedAt:      &t1,
		CategoryAssigned:      &catAssigned,
		CategoryPromptApplied: &catPrompt,
		CategoryModel:         &catModel,
		CategoryClassifiedAt:  &t2,
	})
	if err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{fmt.Sprintf("%d", msgID)},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	jsStr := string(js)

	if !strings.Contains(jsStr, `"verdict":"ham"`) {
		t.Errorf("ham verdict missing: %s", jsStr)
	}
	if !strings.Contains(jsStr, `"assigned":"primary"`) {
		t.Errorf("category assigned missing: %s", jsStr)
	}
}

// TestLLMInspect_NoClassification verifies that a message with no
// classification record is omitted from the response list.
func TestLLMInspect_NoClassification(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	mb := insertMailboxForPrincipal(t, st, p.ID, "INBOX")
	msgID := insertMessage(t, st, mb, "msg004@example.test")

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{fmt.Sprintf("%d", msgID)},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect: %v", mErr)
	}
	var parsed struct {
		List []any `json:"list"`
	}
	js, _ := json.Marshal(resp)
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(parsed.List) != 0 {
		t.Errorf("expected empty list for unclassified message; got %d items: %s",
			len(parsed.List), js)
	}
}

// TestLLMInspect_CrossAccountBlocked verifies that a classification record
// belonging to principal B is not returned when queried by principal A.
func TestLLMInspect_CrossAccountBlocked(t *testing.T) {
	h, st, p := setupHandlers(t, nil) // p = alice

	// Insert a second principal (bob) and give him a message.
	bob := insertPrincipal(t, st, "bob@example.test")
	bobMB := insertMailboxForPrincipal(t, st, bob.ID, "INBOX")
	bobMsgID := insertMessage(t, st, bobMB, "bob001@example.test")

	verdict := "spam"
	conf := 0.99
	err := st.Meta().SetLLMClassification(context.Background(), store.LLMClassificationRecord{
		MessageID:      bobMsgID,
		PrincipalID:    bob.ID, // record belongs to bob
		SpamVerdict:    &verdict,
		SpamConfidence: &conf,
	})
	if err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	// Alice requests bob's message ID -- should be silently excluded.
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{fmt.Sprintf("%d", bobMsgID)},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect: %v", mErr)
	}
	var parsed struct {
		List []any `json:"list"`
	}
	js, _ := json.Marshal(resp)
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(parsed.List) != 0 {
		t.Errorf("cross-account classification leaked: %s", js)
	}
}

// TestLLMInspect_EmptyIDs verifies that an empty ids array returns an empty
// list without error.
func TestLLMInspect_EmptyIDs(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect empty ids: %v", mErr)
	}
	var parsed struct {
		List []any `json:"list"`
	}
	js, _ := json.Marshal(resp)
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(parsed.List) != 0 {
		t.Errorf("expected empty list, got %d items", len(parsed.List))
	}
}

// TestLLMInspect_TooManyIDs verifies that requests exceeding maxIDs are
// rejected with requestTooLarge.
func TestLLMInspect_TooManyIDs(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = fmt.Sprintf("%d", i+1)
	}
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       ids,
	})
	_, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr == nil {
		t.Fatal("expected requestTooLarge error")
	}
	if mErr.Type != "requestTooLarge" {
		t.Errorf("expected requestTooLarge, got %q: %s", mErr.Type, mErr.Description)
	}
}

// TestLLMInspect_AccountIDRequired verifies that absent accountId returns
// invalidArguments.
func TestLLMInspect_AccountIDRequired(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{
		"ids": []string{"1"},
	})
	_, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr == nil {
		t.Fatal("expected error for missing accountId")
	}
	if mErr.Type != "invalidArguments" {
		t.Errorf("expected invalidArguments, got %q", mErr.Type)
	}
}

// TestLLMInspect_AccountNotFound verifies that a mismatched accountId returns
// accountNotFound.
func TestLLMInspect_AccountNotFound(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{
		"accountId": "not-the-right-id",
		"ids":       []string{"1"},
	})
	_, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr == nil {
		t.Fatal("expected error for wrong accountId")
	}
	if mErr.Type != "accountNotFound" {
		t.Errorf("expected accountNotFound, got %q", mErr.Type)
	}
}

// TestLLMInspect_BatchMultiple verifies that multiple IDs in one call return
// all classified messages in request order.
func TestLLMInspect_BatchMultiple(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	mb := insertMailboxForPrincipal(t, st, p.ID, "INBOX")

	var ids []string
	for i := 1; i <= 3; i++ {
		msgID := insertMessage(t, st, mb, fmt.Sprintf("batch%03d@example.test", i))
		verdict := "ham"
		conf := 0.1
		err := st.Meta().SetLLMClassification(context.Background(), store.LLMClassificationRecord{
			MessageID:      msgID,
			PrincipalID:    p.ID,
			SpamVerdict:    &verdict,
			SpamConfidence: &conf,
		})
		if err != nil {
			t.Fatalf("SetLLMClassification msg %d: %v", i, err)
		}
		ids = append(ids, fmt.Sprintf("%d", msgID))
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       ids,
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect batch: %v", mErr)
	}
	var parsed struct {
		List []jmapLLMInspectEntry `json:"list"`
	}
	js, _ := json.Marshal(resp)
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(parsed.List) != 3 {
		t.Errorf("expected 3 entries, got %d: %s", len(parsed.List), js)
	}
}

// TestLLMInspect_InvalidEmailID verifies that unparseable IDs are silently
// skipped and do not cause a method error.
func TestLLMInspect_InvalidEmailID(t *testing.T) {
	h, _, p := setupHandlers(t, nil)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{"not-a-number"},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("expected no error for invalid id, got: %v", mErr)
	}
	var parsed struct {
		List []any `json:"list"`
	}
	js, _ := json.Marshal(resp)
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.List) != 0 {
		t.Errorf("expected empty list for invalid id, got %d items: %s", len(parsed.List), js)
	}
}

// TestRegister_CapabilityRegistered verifies that Register installs both
// handlers and registers the capability descriptor in the registry.
func TestRegister_CapabilityRegistered(t *testing.T) {
	st := newTestStore(t)
	reg := protojmap.NewCapabilityRegistry()
	Register(reg, st, nil, "https://endpoint.test", "model-test")

	if !reg.HasCapability(protojmap.CapabilityLLMTransparency) {
		t.Errorf("CapabilityLLMTransparency not registered")
	}
	if h, ok := reg.Resolve("LLMTransparency/get"); !ok || h == nil {
		t.Errorf("LLMTransparency/get not resolved")
	}
	if h, ok := reg.Resolve("Email/llmInspect"); !ok || h == nil {
		t.Errorf("Email/llmInspect not resolved")
	}

	caps := reg.Capabilities()
	if _, ok := caps[protojmap.CapabilityLLMTransparency]; !ok {
		t.Errorf("CapabilityLLMTransparency missing from Capabilities() map")
	}
}
