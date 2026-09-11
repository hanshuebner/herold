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
	score := -1.0
	classifiedAt := time.Now()
	if err := st.Meta().SetLLMClassification(ctx, store.LLMClassificationRecord{
		MessageID:        msgID,
		PrincipalID:      p.ID,
		SpamVerdict:      &unclassified,
		SpamConfidence:   &score,
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
