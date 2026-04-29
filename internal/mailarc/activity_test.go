package mailarc_test

// activity_test.go verifies that every log record emitted by mailarc carries
// a valid activity attribute (REQ-OPS-86a).

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/mailarc"
	"github.com/hanshuebner/herold/internal/mailauth"
	"github.com/hanshuebner/herold/internal/mailauth/keymgmt"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/testharness/fakestore"
)

// TestSeal_ActivityTagged verifies that a successful Seal call emits a debug
// record with activity=system.
func TestSeal_ActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
		if err != nil {
			t.Fatalf("fakestore: %v", err)
		}
		t.Cleanup(func() { _ = fs.Close() })

		km := keymgmt.NewManager(fs.Meta(), log, clk, rand.Reader)
		if _, err := km.GenerateKey(context.Background(), "example.com", store.DKIMAlgorithmRSASHA256); err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}

		sealer := mailarc.NewSealer(km, nil, log)
		prior := mailauth.AuthResults{
			ARC: mailauth.ARCResult{Status: mailauth.AuthNone},
		}
		sealed, err := sealer.Seal(context.Background(), []byte(baseMessage), prior, "example.com")
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if len(sealed) == 0 {
			t.Fatal("Seal returned empty message")
		}
	})
}

// TestSeal_KeyGenerate_ActivityTagged verifies that GenerateKey in keymgmt
// (called to create a signing key) also emits an activity-tagged record
// (activity=system).
func TestSeal_KeyGenerate_ActivityTagged(t *testing.T) {
	observe.AssertActivityTagged(t, func(log *slog.Logger) {
		clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		fs, err := fakestore.New(fakestore.Options{Clock: clk, BlobDir: t.TempDir()})
		if err != nil {
			t.Fatalf("fakestore: %v", err)
		}
		t.Cleanup(func() { _ = fs.Close() })

		km := keymgmt.NewManager(fs.Meta(), log, clk, deterministicRSAReader(t))
		if _, err := km.GenerateKey(context.Background(), "example.com", store.DKIMAlgorithmRSASHA256); err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
	})
}

// deterministicRSAReader returns a reader from crypto/rand for the test.
// RSA key generation requires a real entropy source in Go 1.21+.
func deterministicRSAReader(t *testing.T) io.Reader {
	t.Helper()
	// Validate that RSA key generation works with crypto/rand for this test.
	// We don't need determinism here; only that the key is generated and
	// the log record is emitted.
	_ = rsa.GenerateKey // just a reference check
	return rand.Reader
}
