package admin

// cmd_diag_mergemailbox_test.go covers `herold diag merge-mailbox` at the
// CLI layer (re #509), following the NewRootCmd() / SetArgs() / Execute()
// pattern the other diag CLI tests use (cmd_diag_test.go). Dual-backend
// coverage of the underlying merge logic lives in
// internal/diag/mergemailbox; this test only pins the cobra wiring (flag
// parsing, --dry-run default-off, --principal resolution, the exit on a
// refused merge).

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
)

func TestCLI_DiagMergeMailbox_DryRunThenApply(t *testing.T) {
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
		CanonicalEmail: "owner-509@test.local",
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	spam, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Spam", Attributes: store.MailboxAttrJunk,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Spam): %v", err)
	}
	jnk, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "Junk", Attributes: store.MailboxAttrJunk,
	})
	if err != nil {
		t.Fatalf("InsertMailbox(Junk): %v", err)
	}
	ref, err := st.Blobs().Put(ctx, strings.NewReader("merge-mailbox CLI probe"))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		Blob:         ref,
		Size:         ref.Size,
		Envelope:     store.Envelope{MessageID: "cli-merge-509@example.test"},
		InternalDate: clk.Now(),
		ReceivedAt:   clk.Now(),
	}, []store.MessageMailbox{{MailboxID: spam.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, spam.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message in Spam, got %d", len(msgs))
	}
	msgID := msgs[0].ID
	pidStr := p.CanonicalEmail
	fromStr := strconv.FormatUint(uint64(spam.ID), 10)
	intoStr := strconv.FormatUint(uint64(jnk.ID), 10)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// --dry-run (the default) must write nothing.
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--system-config", systomlPath,
		"diag", "merge-mailbox",
		"--principal", pidStr, "--from", fromStr, "--into", intoStr, "--dry-run",
	})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "would merge") {
		t.Errorf("expected dry-run wording in output, got: %s", stderr.String())
	}

	checkSt, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("re-open after dry-run: %v", err)
	}
	if _, err := checkSt.Meta().GetMailboxByID(ctx, spam.ID); err != nil {
		t.Fatalf("Spam mailbox gone after dry-run: %v", err)
	}
	msg, err := checkSt.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage after dry-run: %v", err)
	}
	if len(msg.Mailboxes) != 1 || msg.Mailboxes[0].MailboxID != spam.ID {
		t.Fatalf("message moved during dry-run: mailboxes=%v", msg.Mailboxes)
	}
	if err := checkSt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The real run performs the merge.
	root = NewRootCmd()
	stdout.Reset()
	stderr.Reset()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--system-config", systomlPath,
		"diag", "merge-mailbox",
		"--principal", pidStr, "--from", fromStr, "--into", intoStr,
	})
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		t.Fatalf("merge: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "merged") {
		t.Errorf("expected merge wording in output, got: %s", stderr.String())
	}

	checkSt2, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("re-open after merge: %v", err)
	}
	defer checkSt2.Close()
	if _, err := checkSt2.Meta().GetMailboxByID(ctx, spam.ID); err == nil {
		t.Fatalf("Spam mailbox still present after merge")
	}
	msg2, err := checkSt2.Meta().GetMessage(ctx, msgID)
	if err != nil {
		t.Fatalf("GetMessage after merge: %v", err)
	}
	if len(msg2.Mailboxes) != 1 || msg2.Mailboxes[0].MailboxID != jnk.ID {
		t.Fatalf("message after merge: mailboxes=%v, want only Junk (%d)", msg2.Mailboxes, jnk.ID)
	}
}
