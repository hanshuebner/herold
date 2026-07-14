package admin

// cmd_list_moderation_test.go — CLI coverage for the `herold list held*` /
// `moderator*` tree (v2 moderation milestone, issue #189, REQ-MLIST-80,
// REQ-MLIST-41, REQ-AC-41), mirroring cmd_list_test.go's own CRUD
// coverage. Uses rebuildAdminServer to wire a real *maillist.Expander
// (backed by a real outbound queue.Queue over the env's own store) as
// MailingListModerator, the same chicken-and-egg pattern
// newCLITestEnvWithRenewer uses for CertRenewer.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/maillist"
	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/queue"
	"github.com/hanshuebner/herold/internal/store"
)

func discardCLIModerationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newCLIModerationTestEnv builds a cliTestEnv whose server has a real
// *maillist.Expander wired as MailingListModerator, and returns it
// alongside the Expander so tests can drive a held post into existence
// via a real Expand call (mirroring an SMTP post to a `moderated` list).
func newCLIModerationTestEnv(t *testing.T) (*cliTestEnv, *maillist.Expander) {
	t.Helper()
	env := newCLITestEnv(t, nil)
	env.httpSrv.Close()
	q := queue.New(queue.Options{Store: env.store, Logger: discardCLIModerationLogger(), Clock: env.clk})
	exp := maillist.NewExpander(env.store.Meta(), q, nil, env.clk, discardCLIModerationLogger())
	exp.Blobs = env.store.Blobs()
	rebuildAdminServer(t, env, func(o *protoadmin.Options) {
		o.MailingListModerator = exp
	})
	return env, exp
}

func postToModeratedListCLI(t *testing.T, exp *maillist.Expander, ml store.MailingList, from string) store.MailingListHeldPostID {
	t.Helper()
	raw := "From: " + from + "\r\n" +
		"To: " + ml.PostingAddress + "\r\n" +
		"Subject: cli moderation test\r\n" +
		"Message-ID: <cli-mod-test@sender.test>\r\n" +
		"Date: Mon, 01 Jan 2026 00:00:00 +0000\r\n" +
		"\r\n" +
		"Body.\r\n"
	msg, err := mailparse.Parse(strings.NewReader(raw), mailparse.NewParseOptions())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := exp.Expand(context.Background(), maillist.ExpandInput{List: ml, Parsed: msg, Raw: []byte(raw)})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !res.Held {
		t.Fatalf("Expand result = %+v, want Held=true", res)
	}
	return res.HeldPostID
}

// TestCLIList_Held_ApproveFansOut exercises `list held`, `list
// held-show`, `list held-raw`, and `list held-approve` end to end
// against a real fan-out.
func TestCLIList_Held_ApproveFansOut(t *testing.T) {
	env, exp := newCLIModerationTestEnv(t)
	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "modcli.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}

	out, _, err := env.run("list", "create", "mods@modcli.test", "Mods", "--arc-seal=false", "--json")
	if err != nil {
		t.Fatalf("list create: %v", err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	listID := itoa(uint64(created["id"].(float64)))

	if _, _, err := env.run("list", "set", listID, "--posting-policy", "moderated", "--arc-seal=false", "--json"); err != nil {
		t.Fatalf("list set posting-policy: %v", err)
	}
	if _, _, err := env.run("list", "member-add", listID, "recipient@example.net", "--json"); err != nil {
		t.Fatalf("member-add: %v", err)
	}

	ml, err := env.store.Meta().GetMailingListByPostingAddress(context.Background(), "mods@modcli.test")
	if err != nil {
		t.Fatalf("GetMailingListByPostingAddress: %v", err)
	}
	heldID := postToModeratedListCLI(t, exp, ml, "poster@sender.test")
	heldIDStr := itoa(uint64(heldID))

	out, _, err = env.run("list", "held", listID, "--status", "pending", "--json")
	if err != nil {
		t.Fatalf("list held: %v", err)
	}
	if !strings.Contains(out, "cli moderation test") {
		t.Fatalf("expected subject in held queue output: %s", out)
	}

	out, _, err = env.run("list", "held-show", listID, heldIDStr, "--json")
	if err != nil {
		t.Fatalf("list held-show: %v", err)
	}
	if !strings.Contains(out, "\"status\": \"pending\"") {
		t.Fatalf("expected pending status in held-show output: %s", out)
	}

	rawOut, _, err := env.run("list", "held-raw", listID, heldIDStr)
	if err != nil {
		t.Fatalf("list held-raw: %v", err)
	}
	if !strings.Contains(rawOut, "cli moderation test") {
		t.Fatalf("expected raw message content: %s", rawOut)
	}

	out, _, err = env.run("list", "held-approve", listID, heldIDStr, "--json")
	if err != nil {
		t.Fatalf("list held-approve: %v", err)
	}
	if !strings.Contains(out, "\"status\": \"approved\"") {
		t.Fatalf("expected approved status in held-approve output: %s", out)
	}

	items, err := env.store.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("queue items after approve = %d, want 1", len(items))
	}

	// A second approve is a conflict, surfaced as a CLI error.
	if _, _, err := env.run("list", "held-approve", listID, heldIDStr, "--json"); err == nil {
		t.Fatalf("second held-approve: expected an error (already decided)")
	}
}

// TestCLIList_Held_RejectAndDiscard_NeverFanOut exercises `list
// held-reject` and `list held-discard`.
func TestCLIList_Held_RejectAndDiscard_NeverFanOut(t *testing.T) {
	env, exp := newCLIModerationTestEnv(t)
	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "modcli2.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	out, _, err := env.run("list", "create", "mods@modcli2.test", "Mods2", "--arc-seal=false", "--json")
	if err != nil {
		t.Fatalf("list create: %v", err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	listID := itoa(uint64(created["id"].(float64)))
	if _, _, err := env.run("list", "set", listID, "--posting-policy", "moderated", "--arc-seal=false", "--json"); err != nil {
		t.Fatalf("list set posting-policy: %v", err)
	}

	ml, err := env.store.Meta().GetMailingListByPostingAddress(context.Background(), "mods@modcli2.test")
	if err != nil {
		t.Fatalf("GetMailingListByPostingAddress: %v", err)
	}

	heldID1 := postToModeratedListCLI(t, exp, ml, "poster-a@sender.test")
	out, _, err = env.run("list", "held-reject", listID, itoa(uint64(heldID1)), "--note", "spam", "--json")
	if err != nil {
		t.Fatalf("list held-reject: %v", err)
	}
	if !strings.Contains(out, "\"status\": \"rejected\"") || !strings.Contains(out, "spam") {
		t.Fatalf("expected rejected status and note in output: %s", out)
	}

	heldID2 := postToModeratedListCLI(t, exp, ml, "poster-b@sender.test")
	out, _, err = env.run("list", "held-discard", listID, itoa(uint64(heldID2)), "--json")
	if err != nil {
		t.Fatalf("list held-discard: %v", err)
	}
	if !strings.Contains(out, "\"status\": \"discarded\"") {
		t.Fatalf("expected discarded status in output: %s", out)
	}

	items, err := env.store.Meta().ListQueueItems(context.Background(), store.QueueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListQueueItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("queue items after reject+discard = %d, want 0", len(items))
	}
}

// TestCLIList_Moderators_GrantAndRevoke exercises `list moderator-add`,
// `list moderators`, and `list moderator-remove` (REQ-AC-41).
func TestCLIList_Moderators_GrantAndRevoke(t *testing.T) {
	env, _ := newCLIModerationTestEnv(t)
	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name: "modcli3.test", IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	out, _, err := env.run("list", "create", "mods@modcli3.test", "Mods3", "--arc-seal=false", "--json")
	if err != nil {
		t.Fatalf("list create: %v", err)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	listID := itoa(uint64(created["id"].(float64)))

	modPID := uint64(seedPrincipal(t, env, "moderator@modcli3.test").ID)

	if _, _, err := env.run("list", "moderator-add", listID, "moderator@modcli3.test"); err != nil {
		t.Fatalf("moderator-add: %v", err)
	}

	out, _, err = env.run("list", "moderators", listID, "--json")
	if err != nil {
		t.Fatalf("moderators: %v", err)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page.Items) != 1 || uint64(page.Items[0]["principal_id"].(float64)) != modPID {
		t.Fatalf("moderators output = %s, want exactly principal_id=%d", out, modPID)
	}

	if _, _, err := env.run("list", "moderator-remove", listID, itoa(modPID)); err != nil {
		t.Fatalf("moderator-remove: %v", err)
	}
	out, _, err = env.run("list", "moderators", listID, "--json")
	if err != nil {
		t.Fatalf("moderators (after remove): %v", err)
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("moderators after remove = %s, want empty", out)
	}
}
