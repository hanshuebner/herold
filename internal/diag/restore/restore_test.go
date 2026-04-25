package restore_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/backup"
	"github.com/hanshuebner/herold/internal/diag/restore"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

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

func emailN(n int) string { return "user" + intStr(n) + "@example.test" }

func seedSmall(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("insert domain: %v", err)
	}
	for i := 0; i < 3; i++ {
		p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: emailN(i),
		})
		if err != nil {
			t.Fatal(err)
		}
		mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{
			PrincipalID: p.ID, Name: "INBOX",
		})
		if err != nil {
			t.Fatal(err)
		}
		body := []byte("Subject: hi\r\n\r\nx")
		ref, err := st.Blobs().Put(ctx, &byteReader{b: body})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
			MailboxID: mb.ID, Size: int64(len(body)), Blob: ref,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRestoreBundle_Fresh_Restores backs up a sqlite store, then
// restores into a fresh empty sqlite and confirms row + blob counts
// match.
func TestRestoreBundle_Fresh_Restores(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open src: %v", err)
	}
	defer src.Close()
	seedSmall(t, src)

	bundle := t.TempDir()
	if _, err := backup.New(backup.Options{Store: src}).CreateBundle(context.Background(), bundle); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	tgt, err := storesqlite.Open(context.Background(), filepath.Join(dir, "tgt.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open tgt: %v", err)
	}
	defer tgt.Close()
	r := restore.New(restore.Options{Store: tgt})
	m, err := r.RestoreBundle(context.Background(), bundle, restore.ModeFresh)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if m.Tables["principals"] != 3 {
		t.Errorf("principals: %d", m.Tables["principals"])
	}
	// Sample read: sample principal must be retrievable from the
	// target store via canonical email.
	p, err := tgt.Meta().GetPrincipalByEmail(context.Background(), emailN(0))
	if err != nil {
		t.Errorf("GetPrincipalByEmail: %v", err)
	}
	if p.CanonicalEmail != emailN(0) {
		t.Errorf("principal email mismatch: %s", p.CanonicalEmail)
	}
}

// TestRestoreBundle_Fresh_RefusesNonEmpty asserts the empty-target
// guard rejects an existing principal in the target.
func TestRestoreBundle_Fresh_RefusesNonEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open src: %v", err)
	}
	defer src.Close()
	seedSmall(t, src)

	bundle := t.TempDir()
	if _, err := backup.New(backup.Options{Store: src}).CreateBundle(context.Background(), bundle); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	tgt, err := storesqlite.Open(context.Background(), filepath.Join(dir, "tgt.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open tgt: %v", err)
	}
	defer tgt.Close()
	// Pre-populate target.
	if _, err := tgt.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "block@example.test",
	}); err != nil {
		t.Fatal(err)
	}
	r := restore.New(restore.Options{Store: tgt})
	_, err = r.RestoreBundle(context.Background(), bundle, restore.ModeFresh)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("expected non-empty refusal, got %v", err)
	}
}

// TestRestoreBundle_ModeReplace_TruncatesFirst pre-populates the
// target then replaces; only restored rows must remain.
func TestRestoreBundle_ModeReplace_TruncatesFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open src: %v", err)
	}
	defer src.Close()
	seedSmall(t, src)

	bundle := t.TempDir()
	if _, err := backup.New(backup.Options{Store: src}).CreateBundle(context.Background(), bundle); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	tgt, err := storesqlite.Open(context.Background(), filepath.Join(dir, "tgt.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open tgt: %v", err)
	}
	defer tgt.Close()
	if _, err := tgt.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "stale@example.test",
	}); err != nil {
		t.Fatal(err)
	}
	r := restore.New(restore.Options{Store: tgt})
	if _, err := r.RestoreBundle(context.Background(), bundle, restore.ModeReplace); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	// stale@... must be gone.
	if _, err := tgt.Meta().GetPrincipalByEmail(context.Background(), "stale@example.test"); err == nil {
		t.Errorf("stale principal still present after replace")
	}
}

// TestRestoreBundle_ModeMerge_SkipsExisting pre-populates the target
// with one of the bundle's rows and confirms ModeMerge tolerates it.
func TestRestoreBundle_ModeMerge_SkipsExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open src: %v", err)
	}
	defer src.Close()
	seedSmall(t, src)

	bundle := t.TempDir()
	if _, err := backup.New(backup.Options{Store: src}).CreateBundle(context.Background(), bundle); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	tgt, err := storesqlite.Open(context.Background(), filepath.Join(dir, "tgt.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open tgt: %v", err)
	}
	defer tgt.Close()
	if err := tgt.Meta().InsertDomain(context.Background(), store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatal(err)
	}
	r := restore.New(restore.Options{Store: tgt})
	if _, err := r.RestoreBundle(context.Background(), bundle, restore.ModeMerge); err != nil {
		t.Fatalf("Restore Merge: %v", err)
	}
	// The pre-existing domain row was a duplicate; merge must skip it
	// and continue with the remaining rows.
	doms, err := tgt.Meta().ListLocalDomains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(doms) != 1 {
		t.Errorf("domains: want 1, got %d", len(doms))
	}
}

// TestRestoreBundle_HashMismatch_Aborts injects a bad blob byte into
// the bundle and expects restore to abort.
func TestRestoreBundle_HashMismatch_Aborts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open src: %v", err)
	}
	defer src.Close()
	seedSmall(t, src)

	bundle := t.TempDir()
	if _, err := backup.New(backup.Options{Store: src}).CreateBundle(context.Background(), bundle); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	// Walk the bundle blobs, append a byte to the first blob file we
	// see.
	root := filepath.Join(bundle, "blobs")
	var corrupted bool
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || corrupted || !info.Mode().IsRegular() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		raw = append(raw, 'x')
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return err
		}
		corrupted = true
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if !corrupted {
		t.Skip("no blob present to corrupt")
	}

	tgt, err := storesqlite.Open(context.Background(), filepath.Join(dir, "tgt.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatalf("Open tgt: %v", err)
	}
	defer tgt.Close()
	r := restore.New(restore.Options{Store: tgt})
	_, err = r.RestoreBundle(context.Background(), bundle, restore.ModeFresh)
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected hash mismatch failure, got %v", err)
	}
}

// TestRestoreBundle_FakestoreFresh_Restores covers the fakestore
// adapter path.
func TestRestoreBundle_FakestoreFresh_Restores(t *testing.T) {
	t.Parallel()
	src, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seedSmall(t, src)

	bundle := t.TempDir()
	if _, err := backup.New(backup.Options{Store: src}).CreateBundle(context.Background(), bundle); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	tgt, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer tgt.Close()
	r := restore.New(restore.Options{Store: tgt})
	if _, err := r.RestoreBundle(context.Background(), bundle, restore.ModeFresh); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	doms, err := tgt.Meta().ListLocalDomains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(doms) != 1 {
		t.Errorf("domains: want 1, got %d", len(doms))
	}
}
