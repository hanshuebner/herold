package admin

// cmd_diag_dupmessages_test.go covers `herold diag duplicate-messages
// list|merge` at the CLI layer (re #496), following the NewRootCmd() /
// SetArgs() / Execute() pattern the other diag CLI tests use
// (cmd_diag_test.go, cmd_imapimport_test.go's
// TestCLIIMAPImportRestoreArchive_*): seed a store directly, close it,
// invoke the cobra command tree against the same system.toml, then
// re-open the store to assert on the result.
//
// Covers a verifier finding: merge --dry-run always reported "would
// merge 0 duplicate(s)" regardless of group size, because the dry-run
// branch built a dupmessages.MergeResult with Removed left nil instead
// of computing it. dupmessages.Preview (added alongside this test) now
// backs both the dry-run branch and Merge's own result, so the two
// cannot diverge again.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// dupMessagesPostgresConfigFixture is minimalConfigFixture's Postgres
// counterpart: same system.toml shape, [server.storage] pointed at dsn
// instead of an on-disk SQLite file. Truncates the target database first
// (STANDARDS.md's "own throwaway Postgres DB" contract assumes a clean
// start, and unlike a SQLite tempfile this DSN persists across test
// processes) so the CLI sees exactly what this test seeds next.
func dupMessagesPostgresConfigFixture(t *testing.T, dsn string) (string, *sysconfig.Config) {
	t.Helper()
	pre, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clock.NewReal())
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	if tr, ok := pre.(interface {
		TruncateAll(ctx context.Context) error
	}); ok {
		if err := tr.TruncateAll(context.Background()); err != nil {
			_ = pre.Close()
			t.Fatalf("TruncateAll: %v", err)
		}
	}
	if err := pre.Close(); err != nil {
		t.Fatalf("close truncate handle: %v", err)
	}

	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	systomlPath := filepath.Join(dir, "system.toml")
	blobDir := filepath.Join(dir, "blobs")
	toml := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
port_report_file = %q

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "postgres"
[server.storage.postgres]
dsn = %q
blob_dir = %q

[[listener]]
name = "smtp"
address = "127.0.0.1:0"
protocol = "smtp"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "imap"
address = "127.0.0.1:0"
protocol = "imap"
tls = "starttls"
cert_file = %q
key_file = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "http"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "http"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, dir, filepath.Join(dir, "ports.toml"), certPath, keyPath, dsn, blobDir,
		certPath, keyPath, certPath, keyPath)
	if err := os.WriteFile(systomlPath, []byte(toml), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return systomlPath, cfg
}

// seedDupMessagesFixture inserts one principal with two live rows sharing
// a Message-ID, reproducing thread 3994's shape (re #496): an
// imap-import-style row, unread, in Inbox and a provenance label; and an
// SMTP-style row, seen, in Inbox only, with different stored bytes (a
// real mirror-vs-SMTP pair never shares a blob hash). Returns the
// principal's canonical email and the two message ids, oldest (the
// mirror, the row merge keeps) first.
func seedDupMessagesFixture(t *testing.T, st store.Store) (email string, mirrorID, dupID store.MessageID) {
	t.Helper()
	ctx := context.Background()
	email = fmt.Sprintf("cli-dup-%d@example.test", time.Now().UnixNano())
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox,
	})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	label, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: p.ID, Name: "classic-computing.de",
	})
	if err != nil {
		t.Fatalf("InsertMailbox (label): %v", err)
	}

	const msgID = "cli-dup-496@example.test"
	t0 := time.Date(2026, 9, 26, 18, 0, 7, 0, time.UTC)
	t1 := t0.Add(16 * time.Second)

	mirrorRef, err := st.Blobs().Put(ctx, strings.NewReader("mirror body, upstream Received chain"))
	if err != nil {
		t.Fatalf("Blobs.Put (mirror): %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:     p.ID,
		Blob:            mirrorRef,
		Size:            mirrorRef.Size,
		InternalDate:    t0,
		ReceivedAt:      t0,
		Envelope:        store.Envelope{MessageID: msgID, Subject: "dedup probe"},
		IngestSource:    store.IngestSourceIMAPImport,
		IngestSourceRef: "classic-computing.de",
	}, []store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage (mirror): %v", err)
	}
	mirror, err := st.Meta().GetMessageByMessageIDHeader(ctx, p.ID, msgID)
	if err != nil {
		t.Fatalf("GetMessageByMessageIDHeader (mirror): %v", err)
	}
	mirrorID = mirror.ID
	if _, _, err := st.Meta().AddMessageToMailbox(ctx, mirrorID, label.ID); err != nil {
		t.Fatalf("AddMessageToMailbox (label): %v", err)
	}

	dupRef, err := st.Blobs().Put(ctx, strings.NewReader("smtp body, herold Received stamp, different bytes"))
	if err != nil {
		t.Fatalf("Blobs.Put (dup): %v", err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:         p.ID,
		Blob:                dupRef,
		Size:                dupRef.Size,
		InternalDate:        t1,
		ReceivedAt:          t1,
		Envelope:            store.Envelope{MessageID: msgID, Subject: "dedup probe"},
		IngestSource:        store.IngestSourceSMTP,
		DeliveryDisposition: store.DeliveryDispositionInbox,
	}, []store.MessageMailbox{{MailboxID: inbox.ID, Flags: store.MessageFlagSeen}}); err != nil {
		t.Fatalf("InsertMessage (dup): %v", err)
	}
	msgs, err := st.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.ID != mirrorID {
			dupID = m.ID
		}
	}
	if dupID == 0 {
		t.Fatalf("could not find the seeded duplicate row among %+v", msgs)
	}
	return email, mirrorID, dupID
}

// runDiagDupMessagesCLI executes `herold diag duplicate-messages ...`
// against systomlPath and returns stdout/stderr, or a non-nil err on a
// non-zero exit.
func runDiagDupMessagesCLI(t *testing.T, systomlPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	full := append([]string{"--json", "--system-config", systomlPath}, args...)
	root.SetArgs(full)
	root.SetContext(context.Background())
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// dupGroupJSON mirrors writeDupMessageGroupsJSON's wire shape.
type dupGroupJSONForTest struct {
	MessageID string   `json:"messageId"`
	Messages  []uint64 `json:"messages"`
}

// dupMergeResultsJSON mirrors writeDupMergeResults' wire shape.
type dupMergeResultsJSONForTest struct {
	DryRun  bool `json:"dryRun"`
	Results []struct {
		MessageID     string `json:"MessageID"`
		KeptMessageID uint64 `json:"KeptMessageID"`
		Removed       []struct {
			MessageID uint64 `json:"MessageID"`
			BlobHash  string `json:"BlobHash"`
			Mailboxes []struct {
				MailboxID uint64   `json:"MailboxID"`
				Flags     uint64   `json:"Flags"`
				Keywords  []string `json:"Keywords"`
			} `json:"Mailboxes"`
		} `json:"Removed"`
	} `json:"results"`
}

func TestCLI_DuplicateMessages_List_SQLite(t *testing.T) {
	systomlPath, cfg := minimalConfigFixture(t)
	testCLIDuplicateMessagesList(t, systomlPath, cfg)
}

func TestCLI_DuplicateMessages_List_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	systomlPath, cfg := dupMessagesPostgresConfigFixture(t, dsn)
	testCLIDuplicateMessagesList(t, systomlPath, cfg)
}

func testCLIDuplicateMessagesList(t *testing.T, systomlPath string, cfg *sysconfig.Config) {
	ctx := context.Background()
	st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	email, mirrorID, dupID := seedDupMessagesFixture(t, st)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stdout, stderr, err := runDiagDupMessagesCLI(t, systomlPath,
		"diag", "duplicate-messages", "list", "--principal", email)
	if err != nil {
		t.Fatalf("list: %v\nstderr=%s", err, stderr)
	}
	var groups []dupGroupJSONForTest
	if err := json.Unmarshal([]byte(stdout), &groups); err != nil {
		t.Fatalf("parse JSON: %v\nstdout=%s", err, stdout)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1: %+v", len(groups), groups)
	}
	want := map[uint64]bool{uint64(mirrorID): true, uint64(dupID): true}
	if len(groups[0].Messages) != 2 || !want[groups[0].Messages[0]] || !want[groups[0].Messages[1]] {
		t.Fatalf("group messages = %v, want %v", groups[0].Messages, want)
	}
}

func TestCLI_DuplicateMessages_MergeDryRun_SQLite(t *testing.T) {
	systomlPath, cfg := minimalConfigFixture(t)
	testCLIDuplicateMessagesMergeDryRun(t, systomlPath, cfg)
}

func TestCLI_DuplicateMessages_MergeDryRun_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	systomlPath, cfg := dupMessagesPostgresConfigFixture(t, dsn)
	testCLIDuplicateMessagesMergeDryRun(t, systomlPath, cfg)
}

// testCLIDuplicateMessagesMergeDryRun is the CLI-level regression test
// for the verifier's finding: --dry-run must report the same Removed
// count a real merge would, and must leave the store byte-identical.
func testCLIDuplicateMessagesMergeDryRun(t *testing.T, systomlPath string, cfg *sysconfig.Config) {
	ctx := context.Background()
	st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	email, mirrorID, dupID := seedDupMessagesFixture(t, st)
	before, err := st.Meta().GetMessage(ctx, mirrorID)
	if err != nil {
		t.Fatalf("GetMessage (before): %v", err)
	}
	beforeDup, err := st.Meta().GetMessage(ctx, dupID)
	if err != nil {
		t.Fatalf("GetMessage (dup, before): %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stdout, stderr, err := runDiagDupMessagesCLI(t, systomlPath,
		"diag", "duplicate-messages", "merge", "--principal", email, "--dry-run")
	if err != nil {
		t.Fatalf("merge --dry-run: %v\nstderr=%s", err, stderr)
	}
	var resp dupMergeResultsJSONForTest
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse JSON: %v\nstdout=%s", err, stdout)
	}
	if !resp.DryRun {
		t.Fatalf("dryRun = false, want true")
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1: %+v", len(resp.Results), resp.Results)
	}
	res := resp.Results[0]
	if res.KeptMessageID != uint64(mirrorID) {
		t.Fatalf("KeptMessageID = %d, want %d (the older row)", res.KeptMessageID, mirrorID)
	}
	// The regression this test guards: Removed must reflect the real
	// group size (1 duplicate here), not the always-empty nil slice the
	// dry-run branch built before dupmessages.Preview existed.
	if len(res.Removed) != 1 {
		t.Fatalf("dry-run Removed = %d, want 1 (the verifier's finding: dry-run always reported 0)", len(res.Removed))
	}
	if res.Removed[0].MessageID != uint64(dupID) {
		t.Fatalf("dry-run Removed[0].MessageID = %d, want %d", res.Removed[0].MessageID, dupID)
	}

	// The store must be byte-identical afterward: re-open and confirm
	// both rows still exist, unchanged.
	st2, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("re-openStore: %v", err)
	}
	defer st2.Close()
	afterMirror, err := st2.Meta().GetMessage(ctx, mirrorID)
	if err != nil {
		t.Fatalf("GetMessage (mirror, after dry-run): %v -- dry-run must not remove the mirror row", err)
	}
	if afterMirror.Flags != before.Flags || len(afterMirror.Mailboxes) != len(before.Mailboxes) {
		t.Fatalf("dry-run mutated the kept row: before=%+v after=%+v", before, afterMirror)
	}
	afterDup, err := st2.Meta().GetMessage(ctx, dupID)
	if err != nil {
		t.Fatalf("GetMessage (dup, after dry-run): %v -- dry-run must not remove the duplicate row", err)
	}
	if afterDup.Flags != beforeDup.Flags {
		t.Fatalf("dry-run mutated the duplicate row's flags: before=%v after=%v", beforeDup.Flags, afterDup.Flags)
	}
}

func TestCLI_DuplicateMessages_Merge_SQLite(t *testing.T) {
	systomlPath, cfg := minimalConfigFixture(t)
	testCLIDuplicateMessagesMerge(t, systomlPath, cfg)
}

func TestCLI_DuplicateMessages_Merge_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	systomlPath, cfg := dupMessagesPostgresConfigFixture(t, dsn)
	testCLIDuplicateMessagesMerge(t, systomlPath, cfg)
}

// testCLIDuplicateMessagesMerge runs the real merge (no --dry-run) with
// --undo-log and asserts the duplicate row is actually removed and the
// undo log carries a data row for it.
func testCLIDuplicateMessagesMerge(t *testing.T, systomlPath string, cfg *sysconfig.Config) {
	ctx := context.Background()
	st, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	email, mirrorID, dupID := seedDupMessagesFixture(t, st)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	undoPath := filepath.Join(t.TempDir(), "undo.csv")
	stdout, stderr, err := runDiagDupMessagesCLI(t, systomlPath,
		"diag", "duplicate-messages", "merge", "--principal", email, "--undo-log", undoPath)
	if err != nil {
		t.Fatalf("merge: %v\nstderr=%s", err, stderr)
	}
	var resp dupMergeResultsJSONForTest
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse JSON: %v\nstdout=%s", err, stdout)
	}
	if resp.DryRun {
		t.Fatalf("dryRun = true, want false")
	}
	if len(resp.Results) != 1 || len(resp.Results[0].Removed) != 1 {
		t.Fatalf("results = %+v, want exactly 1 result with 1 removed row", resp.Results)
	}

	st2, err := openStore(ctx, cfg, discardLogger(), clock.NewReal())
	if err != nil {
		t.Fatalf("re-openStore: %v", err)
	}
	defer st2.Close()
	if _, err := st2.Meta().GetMessage(ctx, dupID); err == nil {
		t.Fatalf("duplicate row %d still exists after merge", dupID)
	}
	kept, err := st2.Meta().GetMessage(ctx, mirrorID)
	if err != nil {
		t.Fatalf("GetMessage (kept, after merge): %v", err)
	}
	if kept.Flags&store.MessageFlagSeen == 0 {
		t.Fatalf("kept row not marked seen after merging a seen duplicate: flags=%v", kept.Flags)
	}

	undoBytes, err := os.ReadFile(undoPath)
	if err != nil {
		t.Fatalf("read undo log: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(undoBytes))).ReadAll()
	if err != nil {
		t.Fatalf("parse undo log CSV: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("undo log has %d row(s) (header + data), want at least 2: %q", len(rows), undoBytes)
	}
	foundDup := false
	for _, row := range rows[1:] {
		if row[0] == strconv.FormatUint(uint64(dupID), 10) {
			foundDup = true
		}
	}
	if !foundDup {
		t.Fatalf("undo log has no row for removed message %d: %q", dupID, undoBytes)
	}
}
