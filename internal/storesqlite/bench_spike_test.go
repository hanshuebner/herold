//go:build spike

// Package storesqlite spike: compare modernc.org/sqlite (pure Go) vs.
// mattn/go-sqlite3 (CGO) across three workloads informing the tech-stack
// decision in docs/design/server/implementation/01-tech-stack.md.
//
// Run per driver:
//
//	go test -tags 'spike sqlite_modernc' -run=^$ -bench=. -benchtime=5s \
//	    ./internal/storesqlite/...
//	CGO_ENABLED=1 go test -tags 'spike sqlite_mattn' -run=^$ -bench=. \
//	    -benchtime=5s ./internal/storesqlite/...
//
// The driver choice is compile-time; no production code imports either.
package storesqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const schema = `
CREATE TABLE IF NOT EXISTS messages (
  id          INTEGER PRIMARY KEY,
  mailbox_id  INTEGER NOT NULL,
  uid         INTEGER NOT NULL,
  modseq      INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  size        INTEGER NOT NULL,
  flags       INTEGER NOT NULL,
  blob_hash   BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_mbox_uid
  ON messages(mailbox_id, uid);
`

// pragmas match the SQLite settings documented in
// docs/design/server/architecture/02-storage-architecture.md.
var pragmas = []string{
	"PRAGMA journal_mode=WAL",
	"PRAGMA synchronous=NORMAL",
	"PRAGMA busy_timeout=30000",
	"PRAGMA foreign_keys=ON",
	"PRAGMA temp_store=MEMORY",
	"PRAGMA cache_size=-65536",
}

func openDB(tb testing.TB, dir string) *sql.DB {
	tb.Helper()
	path := filepath.Join(dir, "bench.db")
	dsn := driverDSN(path)
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	// Single connection keeps writer/reader contention deterministic; for the
	// reader-fanout bench we open a separate reader pool below.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			tb.Fatalf("pragma %q: %v", p, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		tb.Fatalf("schema: %v", err)
	}
	return db
}

func openReaderPool(tb testing.TB, dir string, n int) *sql.DB {
	tb.Helper()
	path := filepath.Join(dir, "bench.db")
	db, err := sql.Open(driverName, driverDSN(path))
	if err != nil {
		tb.Fatalf("open reader: %v", err)
	}
	db.SetMaxOpenConns(n)
	db.SetMaxIdleConns(n)
	// WAL already set on writer; readers inherit the on-disk mode.
	if _, err := db.Exec("PRAGMA busy_timeout=30000"); err != nil {
		tb.Fatalf("pragma: %v", err)
	}
	return db
}

func insertRow(tx *sql.Tx, mbox, uid, modseq int64) error {
	var hash [32]byte
	rand.New(rand.NewSource(uid)).Read(hash[:])
	_, err := tx.Exec(`INSERT INTO messages
		(mailbox_id, uid, modseq, received_at, size, flags, blob_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mbox, uid, modseq, time.Now().Unix(), 4096+uid%8192, uid&0xff, hash[:])
	return err
}

// BenchmarkInsertSustained measures single-writer saturation and the
// distribution of per-insert latency at saturation. b.N iterations; we
// treat each as one row committed in its own transaction (delivery path).
func BenchmarkInsertSustained(b *testing.B) {
	dir := b.TempDir()
	db := openDB(b, dir)
	defer db.Close()

	latencies := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		tx, err := db.Begin()
		if err != nil {
			b.Fatalf("begin: %v", err)
		}
		if err := insertRow(tx, 1, int64(i+1), int64(i+1)); err != nil {
			b.Fatalf("insert: %v", err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatalf("commit: %v", err)
		}
		latencies = append(latencies, time.Since(t0))
	}
	elapsed := time.Since(start)
	b.StopTimer()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p := func(q float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		idx := int(q * float64(len(latencies)-1))
		return latencies[idx]
	}
	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "inserts/sec")
	b.ReportMetric(float64(p(0.50).Microseconds()), "p50_us")
	b.ReportMetric(float64(p(0.95).Microseconds()), "p95_us")
	b.ReportMetric(float64(p(0.99).Microseconds()), "p99_us")
}

// BenchmarkConcurrentReadsDuringWrite models IMAP FETCH-shape queries from
// 32 readers running concurrently with a ~20 msg/s insert stream.
func BenchmarkConcurrentReadsDuringWrite(b *testing.B) {
	const readers = 32
	const writerRate = 20 // msg/s

	dir := b.TempDir()
	writer := openDB(b, dir)
	defer writer.Close()

	// Seed the mailbox with 100k rows so FETCH queries land on a realistic
	// index.
	seedRows(b, writer, 1, 100_000)

	readerDB := openReaderPool(b, dir, readers)
	defer readerDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Writer goroutine: one insert every 1/writerRate seconds.
	var writes atomic.Int64
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		ticker := time.NewTicker(time.Second / time.Duration(writerRate))
		defer ticker.Stop()
		uid := int64(100_001)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tx, err := writer.Begin()
				if err != nil {
					return
				}
				if err := insertRow(tx, 1, uid, uid); err != nil {
					_ = tx.Rollback()
					return
				}
				if err := tx.Commit(); err != nil {
					return
				}
				uid++
				writes.Add(1)
			}
		}
	}()

	var wg sync.WaitGroup
	var totalOps atomic.Int64
	latC := make(chan time.Duration, b.N+readers*4)
	perReader := b.N / readers
	if perReader < 1 {
		perReader = 1
	}

	b.ResetTimer()
	start := time.Now()
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < perReader; i++ {
				uidStart := rng.Int63n(90_000) + 1
				t0 := time.Now()
				rows, err := readerDB.QueryContext(ctx,
					`SELECT id, uid, modseq, size, flags FROM messages
					   WHERE mailbox_id = ? AND uid >= ?
					   ORDER BY uid LIMIT 50`,
					1, uidStart)
				if err != nil {
					return
				}
				var n int
				for rows.Next() {
					var id, uid, modseq, size, flags int64
					if err := rows.Scan(&id, &uid, &modseq, &size, &flags); err != nil {
						_ = rows.Close()
						return
					}
					n++
				}
				rows.Close()
				latC <- time.Since(t0)
				totalOps.Add(1)
			}
		}(int64(r))
	}
	wg.Wait()
	elapsed := time.Since(start)
	cancel()
	<-writerDone
	close(latC)
	b.StopTimer()

	lats := make([]time.Duration, 0, totalOps.Load())
	for d := range latC {
		lats = append(lats, d)
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p := func(q float64) time.Duration {
		if len(lats) == 0 {
			return 0
		}
		return lats[int(q*float64(len(lats)-1))]
	}
	b.ReportMetric(float64(totalOps.Load())/elapsed.Seconds(), "reads/sec")
	b.ReportMetric(float64(writes.Load())/elapsed.Seconds(), "writes/sec")
	b.ReportMetric(float64(p(0.50).Microseconds()), "p50_us")
	b.ReportMetric(float64(p(0.95).Microseconds()), "p95_us")
	b.ReportMetric(float64(p(0.99).Microseconds()), "p99_us")
}

// BenchmarkLargeScan times a full-mailbox scan over 1M rows. We build the
// table once per b.N; b.N = 1 is the expected case. The metric is rows/s.
func BenchmarkLargeScan(b *testing.B) {
	const rows = 1_000_000
	dir := b.TempDir()
	db := openDB(b, dir)
	defer db.Close()
	seedRows(b, db, 7, rows)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		r, err := db.Query(
			`SELECT id, uid, modseq, size, flags FROM messages
			   WHERE mailbox_id = ? ORDER BY uid`, 7)
		if err != nil {
			b.Fatalf("query: %v", err)
		}
		var count int64
		for r.Next() {
			var id, uid, modseq, size, flags int64
			if err := r.Scan(&id, &uid, &modseq, &size, &flags); err != nil {
				r.Close()
				b.Fatalf("scan: %v", err)
			}
			count++
		}
		r.Close()
		elapsed := time.Since(t0)
		if count != rows {
			b.Fatalf("expected %d rows, got %d", rows, count)
		}
		b.ReportMetric(float64(rows)/elapsed.Seconds(), "rows/sec")
		b.ReportMetric(float64(elapsed.Milliseconds()), "scan_ms")
	}
}

func seedRows(tb testing.TB, db *sql.DB, mbox, n int64) {
	tb.Helper()
	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("seed begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO messages
		(mailbox_id, uid, modseq, received_at, size, flags, blob_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tb.Fatalf("seed prepare: %v", err)
	}
	defer stmt.Close()
	var hash [32]byte
	for i := int64(1); i <= n; i++ {
		rand.New(rand.NewSource(i)).Read(hash[:])
		if _, err := stmt.Exec(mbox, i, i, time.Now().Unix(),
			4096+i%8192, i&0xff, hash[:]); err != nil {
			tb.Fatalf("seed exec at %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("seed commit: %v", err)
	}
}

// Silence unused warning when only one driver tag builds.
var _ = fmt.Sprintf
