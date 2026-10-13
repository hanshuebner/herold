package admin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// TestCLI_DevSeedAppThread checks that `herold dev seed-app-thread`
// seeds one two-message thread and is a no-op on a second run
// (REQ-APP-71).
func TestCLI_DevSeedAppThread(t *testing.T) {
	t.Parallel()
	systomlPath, cfg := minimalConfigFixture(t)
	ctx := context.Background()
	st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{Kind: store.PrincipalKindUser, CanonicalEmail: "alice@test.local"})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	_ = st.Close()

	for i, want := range []string{"seeded app thread", "already present"} {
		root := NewRootCmd()
		var stdout bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"--system-config", systomlPath, "dev", "seed-app-thread", "--principal", "alice@test.local"})
		if err := root.ExecuteContext(ctx); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("run %d output %q lacks %q", i, stdout.String(), want)
		}
	}

	st, err = openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	msgs, err := st.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("ListMessages = %d, %v", len(msgs), err)
	}
	exp, err := protojmap.NewThreadExporter(st).Resolve(ctx, p.ID, p.ID, protojmap.Id("t"+strings.TrimSpace(uintStr(uint64(msgs[0].ID)))))
	if err != nil || len(exp.Messages) != 2 || exp.Messages[0].Subject != devAppThreadSubject {
		t.Fatalf("seeded thread export = %+v, %v", exp, err)
	}
}
