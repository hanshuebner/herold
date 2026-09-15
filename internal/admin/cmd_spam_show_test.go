package admin

// cmd_spam_show_test.go covers `herold spam show <message-id>` (re
// #326): wires the cobra command tree against a minimal sysconfig
// fixture like TestCLI_DiagReparseEnvelopes_DryRunThenApply, seeds a
// message plus an llm_classifications row directly through the store,
// and asserts the CLI's rendered output.

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

func TestCLI_SpamShow_UnclassifiedWithReason(t *testing.T) {
	t.Parallel()
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "owner@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	ref, err := st.Blobs().Put(ctx, strings.NewReader("From: a@example.test\r\nTo: owner@test.local\r\n\r\nBody.\r\n"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         ref.Size,
		Blob:         ref,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("ListMessages: %v (len=%d)", err, len(msgs))
	}
	msgID := msgs[0].ID

	unclassified := "unclassified"
	reason := "timeout: json-rpc error -32001: rpc deadline exceeded"
	classifiedAt := time.Now()
	// No SpamConfidence (re #326): an Unclassified outcome never
	// produced a score, so the write path leaves it nil rather than
	// persisting -1.
	if err := st.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID:        msgID,
		PrincipalID:      p.ID,
		SpamVerdict:      &unclassified,
		SpamReason:       &reason,
		SpamClassifiedAt: &classifiedAt,
	}); err != nil {
		t.Fatalf("SetLLMClassification: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--system-config", systomlPath, "--json",
		"spam", "show", strconv.FormatUint(uint64(msgID), 10),
	})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("spam show: %v\nstderr=%s", err, stderr.String())
	}

	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, stdout.String())
	}
	if out["spam_verdict"] != "unclassified" {
		t.Errorf("spam_verdict = %v, want unclassified", out["spam_verdict"])
	}
	if out["spam_reason"] != reason {
		t.Errorf("spam_reason = %v, want %q", out["spam_reason"], reason)
	}
	if out["message_id"] != float64(msgID) {
		t.Errorf("message_id = %v, want %d", out["message_id"], msgID)
	}
	if _, has := out["spam_confidence"]; has {
		t.Errorf("spam_confidence must be absent for an unclassified record, got %v", out["spam_confidence"])
	}
}

// TestCLI_SpamShow_SignalsAndInconsistent verifies `spam show`'s rendered
// map carries spam_signals/ham_signals/spam_inconsistent (re #396,
// migration 0111) when the classification record carries them, and that a
// record with no signal lists omits all three keys rather than rendering
// null/empty values (llmClassificationRecordToMap, cmd_spam.go).
func TestCLI_SpamShow_SignalsAndInconsistent(t *testing.T) {
	t.Parallel()
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "owner3@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}

	// Message A: a full spam sub-record with both signal lists and an
	// inconsistent (ham verdict, spam signals present) outcome.
	refA, err := st.Blobs().Put(ctx, strings.NewReader("From: a@example.test\r\nTo: owner3@test.local\r\n\r\nBody A.\r\n"))
	if err != nil {
		t.Fatalf("Blobs.Put A: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         refA.Size,
		Blob:         refA,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage A: %v", err)
	}

	// Message B: a spam sub-record with no signal lists.
	refB, err := st.Blobs().Put(ctx, strings.NewReader("From: b@example.test\r\nTo: owner3@test.local\r\n\r\nBody B.\r\n"))
	if err != nil {
		t.Fatalf("Blobs.Put B: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         refB.Size,
		Blob:         refB,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage B: %v", err)
	}

	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("ListMessages: %v (len=%d)", err, len(msgs))
	}
	msgIDA, msgIDB := msgs[0].ID, msgs[1].ID

	verdictHam := "ham"
	confA := 0.4
	spamSignals := []string{"urgent action required", "suspicious link"}
	hamSignals := []string{"known sender"}
	inconsistent := true
	if err := st.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID:        msgIDA,
		PrincipalID:      p.ID,
		SpamVerdict:      &verdictHam,
		SpamConfidence:   &confA,
		SpamSignals:      &spamSignals,
		HamSignals:       &hamSignals,
		SpamInconsistent: &inconsistent,
	}); err != nil {
		t.Fatalf("SetLLMClassification A: %v", err)
	}

	verdictSpam := "spam"
	confB := 0.99
	if err := st.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID:      msgIDB,
		PrincipalID:    p.ID,
		SpamVerdict:    &verdictSpam,
		SpamConfidence: &confB,
	}); err != nil {
		t.Fatalf("SetLLMClassification B: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	runShow := func(msgID store.MessageID) map[string]any {
		t.Helper()
		root := NewRootCmd()
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--system-config", systomlPath, "--json",
			"spam", "show", strconv.FormatUint(uint64(msgID), 10),
		})
		root.SetContext(context.Background())
		if err := root.Execute(); err != nil {
			t.Fatalf("spam show: %v\nstderr=%s", err, stderr.String())
		}
		var out map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v: %s", err, stdout.String())
		}
		return out
	}

	outA := runShow(msgIDA)
	gotSpamSignals, ok := outA["spam_signals"].([]any)
	if !ok || len(gotSpamSignals) != 2 || gotSpamSignals[0] != "urgent action required" || gotSpamSignals[1] != "suspicious link" {
		t.Errorf("spam_signals = %v, want %v", outA["spam_signals"], spamSignals)
	}
	gotHamSignals, ok := outA["ham_signals"].([]any)
	if !ok || len(gotHamSignals) != 1 || gotHamSignals[0] != "known sender" {
		t.Errorf("ham_signals = %v, want %v", outA["ham_signals"], hamSignals)
	}
	if v, ok := outA["spam_inconsistent"].(bool); !ok || !v {
		t.Errorf("spam_inconsistent = %v, want true", outA["spam_inconsistent"])
	}

	outB := runShow(msgIDB)
	for _, key := range []string{"spam_signals", "ham_signals", "spam_inconsistent"} {
		if v, has := outB[key]; has {
			t.Errorf("%s must be absent for a record with no signal lists, got %v", key, v)
		}
	}
}

func TestCLI_SpamShow_NoRecord(t *testing.T) {
	t.Parallel()
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	clk := clock.NewReal()

	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "owner2@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	ref, err := st.Blobs().Put(ctx, strings.NewReader("From: a@example.test\r\nTo: owner2@test.local\r\n\r\nBody.\r\n"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		InternalDate: time.Now(),
		ReceivedAt:   time.Now(),
		Size:         ref.Size,
		Blob:         ref,
	}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{Limit: 10})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("ListMessages: %v (len=%d)", err, len(msgs))
	}
	msgID := msgs[0].ID
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--system-config", systomlPath, "spam", "show", strconv.FormatUint(uint64(msgID), 10)})
	root.SetContext(context.Background())
	if err := root.Execute(); err == nil {
		t.Fatalf("expected error for a message with no classification record")
	}
}
