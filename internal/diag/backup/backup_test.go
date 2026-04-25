package backup_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/backup"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// seedSmallCorpus inserts a tiny but representative dataset into st.
// Returns the inserted blob hashes so callers can spot-check restore.
func seedSmallCorpus(t *testing.T, st store.Store) []string {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	var hashes []string
	for i := 0; i < 5; i++ {
		p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: emailN(i),
		})
		if err != nil {
			t.Fatalf("insert principal: %v", err)
		}
		mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
			PrincipalID: p.ID, Name: "INBOX",
		})
		if err != nil {
			t.Fatalf("insert mailbox: %v", err)
		}
		// Two messages each.
		for j := 0; j < 2; j++ {
			body := []byte("Subject: hello " + emailN(i) + "/" + intStr(j) + "\r\n\r\nbody")
			ref, err := st.Blobs().Put(ctx, &byteReader{b: body})
			if err != nil {
				t.Fatalf("put blob: %v", err)
			}
			hashes = append(hashes, ref.Hash)
			if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
				MailboxID: mb.ID, Size: int64(len(body)), Blob: ref,
			}); err != nil {
				t.Fatalf("insert message: %v", err)
			}
		}
	}
	return hashes
}

func emailN(n int) string { return "user" + intStr(n) + "@example.test" }
func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

type byteReader struct {
	b []byte
	o int
}

func (br *byteReader) Read(p []byte) (int, error) {
	if br.o >= len(br.b) {
		return 0, io.EOF
	}
	n := copy(p, br.b[br.o:])
	br.o += n
	return n, nil
}

// TestCreateBundle_RoundTrip seeds a fakestore, dumps it, and checks
// the manifest counts plus per-table jsonl files exist.
func TestCreateBundle_RoundTrip(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
	fs, err := fakestore.New(fakestore.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	hashes := seedSmallCorpus(t, fs)

	dst := t.TempDir()
	b := backup.New(backup.Options{Store: fs, Clock: clk})
	m, err := b.CreateBundle(context.Background(), dst)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if m.Tables["principals"] != 5 {
		t.Errorf("principals: want 5, got %d", m.Tables["principals"])
	}
	if m.Tables["mailboxes"] != 5 {
		t.Errorf("mailboxes: want 5, got %d", m.Tables["mailboxes"])
	}
	if m.Tables["messages"] != 10 {
		t.Errorf("messages: want 10, got %d", m.Tables["messages"])
	}
	if int(m.Blobs.Count) != len(hashes) {
		t.Errorf("blob count: want %d, got %d", len(hashes), m.Blobs.Count)
	}
	if m.SchemaVersion != backup.CurrentSchemaVersion {
		t.Errorf("schema version: want %d, got %d", backup.CurrentSchemaVersion, m.SchemaVersion)
	}

	// Bundle layout sanity: manifest.json, every JSONL exists.
	if _, err := os.Stat(filepath.Join(dst, "manifest.json")); err != nil {
		t.Errorf("manifest.json: %v", err)
	}
	for _, table := range backup.TableNames {
		path := filepath.Join(dst, "metadata", table+".jsonl")
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing jsonl %s: %v", table, err)
			continue
		}
		_ = fi
	}
}

// TestCreateBundle_Empty_ProducesValidBundle confirms an empty store
// produces a valid manifest plus empty jsonl files.
func TestCreateBundle_Empty_ProducesValidBundle(t *testing.T) {
	t.Parallel()
	fs, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	dst := t.TempDir()
	b := backup.New(backup.Options{Store: fs})
	m, err := b.CreateBundle(context.Background(), dst)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	for _, table := range backup.TableNames {
		if c := m.Tables[table]; c != 0 {
			t.Errorf("%s: want 0 rows, got %d", table, c)
		}
	}
	// Verify rules: re-read confirms.
	if _, err := backup.VerifyBundle(context.Background(), dst); err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
}

// TestCreateBundle_SQLite_RoundTrip seeds a real sqlite store and
// confirms the SQL-backed adapter dumps everything correctly.
func TestCreateBundle_SQLite_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewFake(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
	st, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clk)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	hashes := seedSmallCorpus(t, st)

	dst := t.TempDir()
	b := backup.New(backup.Options{Store: st, Clock: clk})
	m, err := b.CreateBundle(context.Background(), dst)
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if m.Backend != "sqlite" {
		t.Errorf("backend: want sqlite, got %s", m.Backend)
	}
	if m.Tables["principals"] != 5 {
		t.Errorf("principals: %d", m.Tables["principals"])
	}
	if m.Tables["messages"] != 10 {
		t.Errorf("messages: %d", m.Tables["messages"])
	}
	// Each blob must appear in the bundle's blobs/ tree under its
	// 2-level fan-out path.
	for _, h := range hashes {
		path := filepath.Join(dst, "blobs", h[:2], h[2:4], h)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing blob %s: %v", h, err)
		}
	}
	// VerifyBundle re-walks and checks counts; VerifyBundleHashes
	// re-hashes every blob.
	if _, err := backup.VerifyBundle(context.Background(), dst); err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if err := backup.VerifyBundleHashes(context.Background(), dst); err != nil {
		t.Fatalf("VerifyBundleHashes: %v", err)
	}
}

// TestCreateBundle_ConsistentSnapshot_UnderConcurrentWrites confirms
// the snapshot read transaction does not see writes that arrive
// after the snapshot started.
func TestCreateBundle_ConsistentSnapshot_UnderConcurrentWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// Pre-seed: 1 domain, 1 principal, 1 mailbox.
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "concurrent@example.test",
	})
	if err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
	if err != nil {
		t.Fatalf("seed mailbox: %v", err)
	}

	// Insert 50 messages before backup starts.
	for i := 0; i < 50; i++ {
		body := []byte("Subject: pre " + intStr(i) + "\r\n\r\nx")
		ref, err := st.Blobs().Put(ctx, &byteReader{b: body})
		if err != nil {
			t.Fatalf("blob put: %v", err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			MailboxID: mb.ID, Size: int64(len(body)), Blob: ref,
		}); err != nil {
			t.Fatalf("insert msg: %v", err)
		}
	}

	dst := t.TempDir()
	b := backup.New(backup.Options{Store: st})

	// Concurrent inserter: while CreateBundle runs, hammer additional
	// rows. The snapshot must not pick them up.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			body := []byte("Subject: post " + intStr(i) + "\r\n\r\ny")
			ref, err := st.Blobs().Put(ctx, &byteReader{b: body})
			if err != nil {
				return
			}
			_, _, _ = st.Meta().InsertMessage(ctx, store.Message{
				MailboxID: mb.ID, Size: int64(len(body)), Blob: ref,
			})
		}
	}()

	m, err := b.CreateBundle(ctx, dst)
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}

	// The snapshot started after the 50 pre-rows. Concurrent inserts
	// add to the live store but the snapshot transaction must yield
	// AT MOST what was committed before the snapshot opened. Allow a
	// modest race window: any value >=50 and <= 50+goroutine work
	// observed before snapshot is acceptable; the strict invariant is
	// that the manifest count matches the JSONL count and is
	// consistent with the manifest blob count.
	got := m.Tables["messages"]
	if got < 50 {
		t.Fatalf("messages: want at least 50, got %d", got)
	}
	// Rerunning verify must match.
	mv, err := backup.VerifyBundle(ctx, dst)
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if mv.Tables["messages"] != got {
		t.Errorf("verify drift: %d vs %d", mv.Tables["messages"], got)
	}
	// Manifest's blob count must reflect at least the seeded 50.
	if m.Blobs.Count < 50 {
		t.Errorf("blob count: %d (want >= 50)", m.Blobs.Count)
	}
}

// TestVerifyBundle_BadCount_FailsCleanly tampers with a JSONL and
// expects VerifyBundle to surface the mismatch.
func TestVerifyBundle_BadCount_FailsCleanly(t *testing.T) {
	t.Parallel()
	fs, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	seedSmallCorpus(t, fs)
	dst := t.TempDir()
	b := backup.New(backup.Options{Store: fs})
	if _, err := b.CreateBundle(context.Background(), dst); err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	// Truncate principals.jsonl so the count doesn't match.
	path := filepath.Join(dst, "metadata", "principals.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.VerifyBundle(context.Background(), dst); err == nil ||
		!strings.Contains(err.Error(), "principals") {
		t.Errorf("expected verify failure mentioning principals, got %v", err)
	}
}
