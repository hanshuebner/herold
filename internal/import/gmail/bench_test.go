package gmail_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	gmail "github.com/hanshuebner/herold/internal/import/gmail"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storesqlite"
)

// generateMbox builds an N-message synthetic Gmail-shaped mbox written
// to path. Each message is roughly 1 KB with deterministic
// Message-IDs so the benchmark can re-run idempotently.
func generateMbox(b *testing.B, path string, n int) {
	b.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "From sender@example.com Wed May 06 18:21:37 +0000 2026\n")
		fmt.Fprintf(&sb, "X-GM-THRID: %d\n", 1864000000000+i)
		fmt.Fprintf(&sb, "X-Gmail-Labels: Posteingang,Wichtig\n")
		fmt.Fprintf(&sb, "From: bench-%d@example.com\n", i)
		fmt.Fprintf(&sb, "To: hans@example.com\n")
		fmt.Fprintf(&sb, "Subject: bench message %d\n", i)
		fmt.Fprintf(&sb, "Message-ID: <bench-%d@example.com>\n", i)
		fmt.Fprintf(&sb, "Date: Wed, 6 May 2026 18:21:24 +0000\n")
		fmt.Fprintf(&sb, "Content-Type: text/plain\n\n")
		// ~1 KB body
		fmt.Fprintf(&sb, "%s\n", strings.Repeat("x", 1024))
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		b.Fatalf("write mbox: %v", err)
	}
}

func openBenchStore(b *testing.B, bulk bool) store.Store {
	b.Helper()
	dir := b.TempDir()
	s, err := storesqlite.OpenWithOpts(context.Background(),
		filepath.Join(dir, "meta.db"), nil,
		clock.NewFake(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)),
		storesqlite.Options{BulkImport: bulk})
	if err != nil {
		b.Fatalf("OpenWithOpts: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

func mustInsertBenchPrincipal(b *testing.B, s store.Store) store.Principal {
	b.Helper()
	p, err := s.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "bench@example.com",
	})
	if err != nil {
		b.Fatalf("InsertPrincipal: %v", err)
	}
	return p
}

// BenchmarkImporterEndToEnd measures msg/sec under realistic single-
// goroutine bulk-import conditions. The matrix toggles synchronous=OFF
// and the batched-insert path so each combination's contribution is
// visible. Run with:
//
//	go test -bench=BenchmarkImporterEndToEnd -benchtime=1x ./internal/import/gmail
func BenchmarkImporterEndToEnd(b *testing.B) {
	const n = 5000
	cases := []struct {
		name      string
		bulk      bool
		batchSize int
	}{
		{"baseline_synchronousNORMAL_batch1", false, 1},
		{"bulkImport_synchronousOFF_batch1", true, 1},
		{"baseline_synchronousNORMAL_batch500", false, 500},
		{"bulkImport_synchronousOFF_batch500", true, 500},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				dir := b.TempDir()
				mboxPath := filepath.Join(dir, "bench.mbox")
				generateMbox(b, mboxPath, n)
				s := openBenchStore(b, c.bulk)
				p := mustInsertBenchPrincipal(b, s)
				imp := gmail.Importer{
					Store:     s,
					Principal: p,
					Options: gmail.Options{
						Source:         dir,
						SkipSettings:   true,
						FreshPrincipal: true,
						BatchSize:      c.batchSize,
						ProgressEvery:  1_000_000, // suppress progress logs in bench
					},
				}
				start := time.Now()
				res, err := imp.Run(context.Background())
				if err != nil {
					b.Fatalf("Run: %v", err)
				}
				elapsed := time.Since(start)
				if res.MessagesImported != n {
					b.Fatalf("imported = %d, want %d", res.MessagesImported, n)
				}
				rate := float64(n) / elapsed.Seconds()
				b.ReportMetric(rate, "msgs/sec")
			}
		})
	}
}
