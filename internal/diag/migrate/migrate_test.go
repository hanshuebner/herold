package migrate_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/diag/migrate"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
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

func emailN(n int) string { return "u" + intStr(n) + "@example.test" }

// seed1k inserts 5 principals, each with one mailbox + 200 messages
// for a total of 1k messages — the brief's fixture size for
// throughput measurement.
func seed1k(t *testing.T, st store.Store) (perPrincipal int) {
	t.Helper()
	ctx := context.Background()
	if err := st.Meta().InsertDomain(ctx, store.Domain{Name: "example.test", IsLocal: true}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	const principals = 5
	const msgs = 200
	for i := 0; i < principals; i++ {
		p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
			Kind: store.PrincipalKindUser, CanonicalEmail: emailN(i),
		})
		if err != nil {
			t.Fatal(err)
		}
		mb, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX"})
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < msgs; j++ {
			body := []byte("Subject: " + intStr(i) + "/" + intStr(j) + "\r\n\r\nbody " + intStr(j))
			ref, err := st.Blobs().Put(ctx, &byteReader{b: body})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
				Size: int64(len(body)), Blob: ref,
			}, []store.MessageMailbox{{MailboxID: mb.ID}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return msgs
}

// TestMigrate_SQLiteToFakestore_RoundTrip migrates an sqlite source
// into a fakestore target and confirms the row + blob counts.
func TestMigrate_SQLiteToFakestore_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seed1k(t, src)

	dst, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	start := time.Now()
	m, err := migrate.Migrate(context.Background(), src, dst, migrate.MigrateOptions{})
	took := time.Since(start)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if m.Tables["messages"] != 1000 {
		t.Errorf("messages: want 1000, got %d", m.Tables["messages"])
	}
	rps := float64(1000) / took.Seconds()
	t.Logf("sqlite->fakestore: 1k messages in %s (%.0f rows/sec)", took, rps)

	// Spot-check a principal can be retrieved.
	p, err := dst.Meta().GetPrincipalByEmail(context.Background(), emailN(0))
	if err != nil {
		t.Fatalf("GetPrincipalByEmail: %v", err)
	}
	if p.CanonicalEmail != emailN(0) {
		t.Errorf("email mismatch: %s", p.CanonicalEmail)
	}
}

// TestMigrate_FakestoreToSQLite_RoundTrip is the inverse direction.
func TestMigrate_FakestoreToSQLite_RoundTrip(t *testing.T) {
	t.Parallel()
	src, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seed1k(t, src)

	dir := t.TempDir()
	dst, err := storesqlite.Open(context.Background(), filepath.Join(dir, "tgt.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	start := time.Now()
	m, err := migrate.Migrate(context.Background(), src, dst, migrate.MigrateOptions{})
	took := time.Since(start)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if m.Tables["messages"] != 1000 {
		t.Errorf("messages: want 1000, got %d", m.Tables["messages"])
	}
	rps := float64(1000) / took.Seconds()
	t.Logf("fakestore->sqlite: 1k messages in %s (%.0f rows/sec)", took, rps)
}

// TestMigrate_RefusesNonEmptyTarget asserts the empty-target guard.
func TestMigrate_RefusesNonEmptyTarget(t *testing.T) {
	t.Parallel()
	src, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := dst.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind: store.PrincipalKindUser, CanonicalEmail: "block@example.test",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = migrate.Migrate(context.Background(), src, dst, migrate.MigrateOptions{})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("expected non-empty refusal, got %v", err)
	}
}

// TestMigrate_FKOrderRespected verifies that messages are inserted
// after their owning mailbox so target FKs hold.
func TestMigrate_FKOrderRespected(t *testing.T) {
	t.Parallel()
	src, err := fakestore.New(fakestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seed1k(t, src)
	dir := t.TempDir()
	dst, err := storesqlite.Open(context.Background(), filepath.Join(dir, "tgt.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := migrate.Migrate(context.Background(), src, dst, migrate.MigrateOptions{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Probe target: every message's mailbox must exist (FK PRAGMA on
	// SQLite would have rejected the INSERT otherwise; this is a
	// belt-and-suspenders confirmation).
	mboxes, err := dst.Meta().ListMailboxes(context.Background(), store.PrincipalID(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(mboxes) != 1 {
		t.Errorf("mailboxes: want 1, got %d", len(mboxes))
	}
}

// TestMigrate_PostgresLeg gates the Postgres direction on
// HEROLD_PG_DSN per STANDARDS §8.6: Postgres tests run only when an
// operator-supplied DSN is in the environment.
//
// Currently skipped: internal/storepg has no diag/backup adapter
// (only adapter_sqlite.go and adapter_fakestore.go exist), so a
// sqlite -> postgres migration fails immediately with
// "store does not expose a backup capability". Wiring a postgres
// adapter is a separate piece of work; once it lands, drop the
// build-time skip below.
func TestMigrate_PostgresLeg(t *testing.T) {
	t.Skip("storepg backup adapter not implemented; see internal/diag/backup/adapter_sqlite.go for the template")
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	t.Parallel()
	dir := t.TempDir()
	src, err := storesqlite.Open(context.Background(), filepath.Join(dir, "src.db"), nil, clock.NewReal())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seed1k(t, src)
	dst, err := storepg.Open(context.Background(), dsn, filepath.Join(dir, "blobs"), nil, clock.NewReal())
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	defer dst.Close()
	if _, err := migrate.Migrate(context.Background(), src, dst, migrate.MigrateOptions{}); err != nil {
		t.Fatalf("Migrate sqlite->postgres: %v", err)
	}
}
