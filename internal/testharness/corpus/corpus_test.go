package corpus_test

import (
	"bytes"
	"context"
	"encoding/gob"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/testharness/corpus"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

func TestDeterminismAcrossSeedRuns(t *testing.T) {
	p1 := corpus.Principals(42, 5)
	p2 := corpus.Principals(42, 5)
	if !deepEqualGob(t, p1, p2) {
		t.Fatalf("Principals not deterministic")
	}

	m1 := corpus.Messages(42, 1, 10)
	m2 := corpus.Messages(42, 1, 10)
	if !deepEqualGob(t, m1, m2) {
		t.Fatalf("Messages not deterministic")
	}

	// A different seed should produce different output.
	m3 := corpus.Messages(43, 1, 10)
	if deepEqualGob(t, m1, m3) {
		t.Fatalf("Different seed produced identical Messages output")
	}
}

func TestSeedPopulatesStore(t *testing.T) {
	fs, err := fakestore.New(fakestore.Options{
		Clock:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		BlobDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("fakestore: %v", err)
	}
	defer fs.Close()
	ps, mbs, msgs := corpus.Seed(t, fs, 42, 2, 5, 3)
	if len(ps) != 2 {
		t.Fatalf("principals: got %d want 2", len(ps))
	}
	// 5 standard mailboxes per principal * 2 principals
	if len(mbs) != 10 {
		t.Fatalf("mailboxes: got %d want 10", len(mbs))
	}
	// 3 messages per principal INBOX * 2 principals
	if len(msgs) != 6 {
		t.Fatalf("messages: got %d want 6", len(msgs))
	}
	// Confirm we can list the mailboxes of the first principal.
	list, err := fs.Meta().ListMailboxes(context.Background(), ps[0].ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("list: got %d want 5", len(list))
	}
}

// deepEqualGob compares two values by gob-encoding and byte-equality.
// Reflect.DeepEqual would work for the simple types we have, but the gob
// round-trip is what the harness's seed-determinism contract really
// promises (two runs produce byte-identical fixtures on the wire).
func deepEqualGob(t *testing.T, a, b any) bool {
	t.Helper()
	var ba, bb bytes.Buffer
	if err := gob.NewEncoder(&ba).Encode(a); err != nil {
		t.Fatalf("encode a: %v", err)
	}
	if err := gob.NewEncoder(&bb).Encode(b); err != nil {
		t.Fatalf("encode b: %v", err)
	}
	return bytes.Equal(ba.Bytes(), bb.Bytes())
}
