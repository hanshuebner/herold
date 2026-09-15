package llmtransparency

// Coverage for the structured spam_signals/ham_signals lists and the
// Inconsistent flag on Email/llmInspect's spam sub-record (re #396,
// migration 0111). internal/store/storetest already exercises the
// store-level SetLLMClassification/GetLLMClassification/
// BatchGetLLMClassifications round-trip of these fields on both
// backends; these tests exercise the JMAP-facing rendering in
// methods.go on top of that, again on both backends.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// setupHandlersPostgres is setupHandlers' Postgres counterpart. Skips when
// HEROLD_PG_DSN is unset or the connection cannot be established.
func setupHandlersPostgres(t *testing.T, spam *fakeSpamPolicy) (*handlerSet, store.Store, store.Principal) {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, nil)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := insertPrincipal(t, st, fmt.Sprintf("alice-%d@example.test", time.Now().UnixNano()))
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

// llmInspectSignals runs Email/llmInspect for msgID and returns the decoded
// spam sub-object (nil when the response carried no spam sub-record).
func llmInspectSignals(t *testing.T, h *handlerSet, p store.Principal, msgID store.MessageID) *jmapSpamDetail {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"ids":       []string{fmt.Sprintf("%d", msgID)},
	})
	resp, mErr := (&llmInspectHandler{h: h}).executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Email/llmInspect: %v", mErr)
	}
	r, ok := resp.(llmInspectResponse)
	if !ok {
		t.Fatalf("Email/llmInspect: unexpected response type %T", resp)
	}
	if len(r.List) == 0 {
		return nil
	}
	return r.List[0].Spam
}

// testLLMInspectSignalsPresent seeds a classification record carrying both
// signal lists and an inconsistent verdict, and verifies llmInspect surfaces
// spamSignals, hamSignals and inconsistent:true.
func testLLMInspectSignalsPresent(t *testing.T, h *handlerSet, st store.Store, p store.Principal) {
	mb := insertMailboxForPrincipal(t, st, p.ID, "INBOX")
	msgID := insertMessage(t, st, mb, fmt.Sprintf("signals-present-%d@example.test", time.Now().UnixNano()))

	verdict := "ham"
	conf := 0.4
	spamSignals := []string{"urgent action required", "suspicious link"}
	hamSignals := []string{"known sender"}
	inconsistent := true

	if err := st.Meta().SetLLMClassification(context.Background(), store.LLMClassificationRecord{
		MessageID:        msgID,
		PrincipalID:      p.ID,
		SpamVerdict:      &verdict,
		SpamConfidence:   &conf,
		SpamSignals:      &spamSignals,
		HamSignals:       &hamSignals,
		SpamInconsistent: &inconsistent,
	}); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	spam := llmInspectSignals(t, h, p, msgID)
	if spam == nil {
		t.Fatal("expected a spam sub-object")
	}
	if len(spam.SpamSignals) != 2 || spam.SpamSignals[0] != "urgent action required" || spam.SpamSignals[1] != "suspicious link" {
		t.Errorf("SpamSignals = %v, want %v", spam.SpamSignals, spamSignals)
	}
	if len(spam.HamSignals) != 1 || spam.HamSignals[0] != "known sender" {
		t.Errorf("HamSignals = %v, want %v", spam.HamSignals, hamSignals)
	}
	if !spam.Inconsistent {
		t.Errorf("Inconsistent = false, want true")
	}

	// Also check the wire encoding directly: omitempty must not swallow a
	// populated, non-empty list or a true bool.
	js, err := json.Marshal(spam)
	if err != nil {
		t.Fatalf("marshal spam detail: %v", err)
	}
	jsStr := string(js)
	if want := `"spamSignals":["urgent action required","suspicious link"]`; !strings.Contains(jsStr, want) {
		t.Errorf("wire spamSignals missing/wrong: %s", jsStr)
	}
	if want := `"hamSignals":["known sender"]`; !strings.Contains(jsStr, want) {
		t.Errorf("wire hamSignals missing/wrong: %s", jsStr)
	}
	if want := `"inconsistent":true`; !strings.Contains(jsStr, want) {
		t.Errorf("wire inconsistent missing/wrong: %s", jsStr)
	}
}

// testLLMInspectSignalsAbsent seeds a classification record with a spam
// sub-record but no signal lists (the classifier's response carried neither
// key), and verifies llmInspect omits spamSignals/hamSignals/inconsistent
// from the wire response (all three are `omitempty`, so nil/empty/false
// must not appear as explicit keys).
func testLLMInspectSignalsAbsent(t *testing.T, h *handlerSet, st store.Store, p store.Principal) {
	mb := insertMailboxForPrincipal(t, st, p.ID, "INBOX")
	msgID := insertMessage(t, st, mb, fmt.Sprintf("signals-absent-%d@example.test", time.Now().UnixNano()))

	verdict := "spam"
	conf := 0.99

	if err := st.Meta().SetLLMClassification(context.Background(), store.LLMClassificationRecord{
		MessageID:      msgID,
		PrincipalID:    p.ID,
		SpamVerdict:    &verdict,
		SpamConfidence: &conf,
	}); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}

	spam := llmInspectSignals(t, h, p, msgID)
	if spam == nil {
		t.Fatal("expected a spam sub-object")
	}
	if len(spam.SpamSignals) != 0 {
		t.Errorf("SpamSignals = %v, want empty", spam.SpamSignals)
	}
	if len(spam.HamSignals) != 0 {
		t.Errorf("HamSignals = %v, want empty", spam.HamSignals)
	}
	if spam.Inconsistent {
		t.Errorf("Inconsistent = true, want false")
	}

	js, err := json.Marshal(spam)
	if err != nil {
		t.Fatalf("marshal spam detail: %v", err)
	}
	jsStr := string(js)
	for _, key := range []string{`"spamSignals"`, `"hamSignals"`, `"inconsistent"`} {
		if strings.Contains(jsStr, key) {
			t.Errorf("wire response carries %s despite no signals being set: %s", key, jsStr)
		}
	}
}

func TestLLMInspect_SpamSignals_Present(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	testLLMInspectSignalsPresent(t, h, st, p)
}

func TestLLMInspect_SpamSignals_Absent(t *testing.T) {
	h, st, p := setupHandlers(t, nil)
	testLLMInspectSignalsAbsent(t, h, st, p)
}

func TestLLMInspect_SpamSignals_Present_Postgres(t *testing.T) {
	h, st, p := setupHandlersPostgres(t, nil)
	testLLMInspectSignalsPresent(t, h, st, p)
}

func TestLLMInspect_SpamSignals_Absent_Postgres(t *testing.T) {
	h, st, p := setupHandlersPostgres(t, nil)
	testLLMInspectSignalsAbsent(t, h, st, p)
}
