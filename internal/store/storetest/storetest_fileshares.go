package storetest

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// REQ-SHARE-01..23, REQ-SHARE-10..12, REQ-SHARE-50 -- file-share store compliance.

// defaultShareConfig returns a FileSharesConfig suitable for tests:
// short TTLs for observable sweep behaviour, small caps for cap tests.
func defaultShareConfig() store.FileSharesConfig {
	return store.FileSharesConfig{
		DefaultTTL:             30 * 24 * time.Hour,
		MaxTTL:                 90 * 24 * time.Hour,
		PendingTTL:             time.Hour,
		RevokedGrace:           24 * time.Hour,
		MaxSharesPerPrincipal:  1000,
		ShareQuotaPerPrincipal: 5 * 1024 * 1024 * 1024, // 5 GiB
	}
}

// mustCreateShare is a test helper that inserts a pending share and
// fatals on error.
func mustCreateShare(t *testing.T, s store.Store, pid store.PrincipalID, blobHash string, blobSize int64, filename, contentType string) store.FileShare {
	t.Helper()
	ctx := ctxT(t)
	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: pid,
		BlobHash:    blobHash,
		BlobSize:    blobSize,
		Filename:    filename,
		ContentType: contentType,
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}
	return fs
}

// putShareBlob uploads bytes to the blob store and returns the hash+size.
func putShareBlob(t *testing.T, s store.Store, content string) (hash string, size int64) {
	t.Helper()
	ref, err := s.Blobs().Put(ctxT(t), strings.NewReader(content))
	if err != nil {
		t.Fatalf("Blobs.Put: %v", err)
	}
	return ref.Hash, ref.Size
}

func testFileShareCreateAndGetRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-rt@example.com")
	hash, size := putShareBlob(t, s, "hello world attachment")

	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "hello.txt",
		ContentType: "text/plain",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}

	// Token must be non-empty and URL-safe (no +, /, =).
	if fs.ID == "" {
		t.Fatal("CreateFileShare returned empty ID")
	}
	for _, ch := range fs.ID {
		if ch == '+' || ch == '/' || ch == '=' {
			t.Fatalf("token %q contains non-URL-safe character %q", fs.ID, ch)
		}
	}

	// State must be pending.
	if fs.State != store.FileShareStatePending {
		t.Fatalf("State = %q, want pending", fs.State)
	}
	if fs.PrincipalID != p.ID {
		t.Fatalf("PrincipalID = %d, want %d", fs.PrincipalID, p.ID)
	}
	if fs.BlobHash != hash {
		t.Fatalf("BlobHash = %q, want %q", fs.BlobHash, hash)
	}
	if fs.Filename != "hello.txt" {
		t.Fatalf("Filename = %q, want hello.txt", fs.Filename)
	}
	if fs.DownloadCount != 0 {
		t.Fatalf("DownloadCount = %d, want 0", fs.DownloadCount)
	}
	if fs.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not stamped")
	}
	if fs.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt not stamped")
	}

	// GetByID must return the same row.
	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.ID != fs.ID {
		t.Fatalf("GetByID.ID = %q, want %q", got.ID, fs.ID)
	}
	if got.State != store.FileShareStatePending {
		t.Fatalf("GetByID.State = %q, want pending", got.State)
	}
}

func testFileShareTokenEntropy(t *testing.T, s store.Store) {
	// REQ-SHARE-11b: IDs must be non-sequential and unique.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-entropy@example.com")
	hash, size := putShareBlob(t, s, "entropy test blob")

	cfg := defaultShareConfig()
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
			PrincipalID: p.ID,
			BlobHash:    hash,
			BlobSize:    size,
			Filename:    fmt.Sprintf("f%d.txt", i),
			ContentType: "text/plain",
			PendingTTL:  cfg.PendingTTL,
			Config:      cfg,
		})
		if err != nil {
			t.Fatalf("CreateFileShare #%d: %v", i, err)
		}
		if seen[fs.ID] {
			t.Fatalf("duplicate token %q at iteration %d", fs.ID, i)
		}
		seen[fs.ID] = true
		// Minimum length check: 20 bytes base64url => 27 chars (160 bits).
		if len(fs.ID) < 27 {
			t.Fatalf("token %q too short (len=%d), want >= 27 for >= 160 bits", fs.ID, len(fs.ID))
		}
	}
}

func testFileShareConfirmIdempotent(t *testing.T, s store.Store) {
	// REQ-SHARE-21: confirming an already-active share is a no-op success.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-confirm-idemp@example.com")
	hash, size := putShareBlob(t, s, "confirm idempotent blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "file.bin", "application/octet-stream")
	cfg := defaultShareConfig()

	// First confirm: pending -> active.
	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare (first): %v", err)
	}
	if active.State != store.FileShareStateActive {
		t.Fatalf("State after first confirm = %q, want active", active.State)
	}
	if active.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt zero after confirm")
	}
	// ExpiresAt must be approximately CreatedAt+DefaultTTL, not the short
	// PendingTTL. Use fs.CreatedAt (from the store clock, which may be a
	// fake) rather than time.Now() so the assertion is clock-agnostic.
	minExpires := fs.CreatedAt.Add(cfg.DefaultTTL / 2)
	if active.ExpiresAt.Before(minExpires) {
		t.Fatalf("ExpiresAt %v seems too short for DefaultTTL=%v (want >= %v)", active.ExpiresAt, cfg.DefaultTTL, minExpires)
	}

	// Second confirm: idempotent, must succeed and return active.
	active2, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare (second, idempotent): %v", err)
	}
	if active2.State != store.FileShareStateActive {
		t.Fatalf("State after second confirm = %q, want active", active2.State)
	}
}

func testFileShareConfirmClampedToMaxTTL(t *testing.T, s store.Store) {
	// REQ-SHARE-20: expires_at clamped to max_ttl on confirm.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-clamp@example.com")
	hash, size := putShareBlob(t, s, "clamp test")

	fs := mustCreateShare(t, s, p.ID, hash, size, "clamp.bin", "application/octet-stream")

	cfg := defaultShareConfig()
	cfg.DefaultTTL = 120 * 24 * time.Hour // 120 days (> MaxTTL 90 days)
	cfg.MaxTTL = 90 * 24 * time.Hour

	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	// ExpiresAt must be <= now + MaxTTL.
	maxExpires := time.Now().Add(cfg.MaxTTL).Add(5 * time.Second) // small clock slack
	if active.ExpiresAt.After(maxExpires) {
		t.Fatalf("ExpiresAt %v exceeds max_ttl ceiling %v", active.ExpiresAt, maxExpires)
	}
}

func testFileShareConfirmRevokedFails(t *testing.T, s store.Store) {
	// REQ-SHARE-21: confirming a revoked share must fail.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-confirm-revoked@example.com")
	hash, size := putShareBlob(t, s, "revoked confirm blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "r.bin", "application/octet-stream")
	cfg := defaultShareConfig()

	// Confirm first, then revoke.
	if _, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{}); err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	if err := s.Meta().RevokeFileShare(ctx, p.ID, fs.ID); err != nil {
		t.Fatalf("RevokeFileShare: %v", err)
	}

	_, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if !errors.Is(err, store.ErrShareNotConfirmable) {
		t.Fatalf("ConfirmFileShare(revoked) = %v, want ErrShareNotConfirmable", err)
	}
}

func testFileShareRevokeAndGetReturnsRevoked(t *testing.T, s store.Store) {
	// REQ-SHARE-22: revoke sets state=revoked; row persists.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-revoke@example.com")
	hash, size := putShareBlob(t, s, "revoke blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "rev.bin", "application/octet-stream")

	if err := s.Meta().RevokeFileShare(ctx, p.ID, fs.ID); err != nil {
		t.Fatalf("RevokeFileShare: %v", err)
	}

	// Row still exists with state=revoked.
	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID after revoke: %v", err)
	}
	if got.State != store.FileShareStateRevoked {
		t.Fatalf("State after revoke = %q, want revoked", got.State)
	}
	if got.RevokedAt == nil {
		t.Fatal("RevokedAt not set after revoke")
	}
}

func testFileShareRevokeWrongPrincipalFails(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "share-revoke-owner@example.com")
	other := mustInsertPrincipal(t, s, "share-revoke-other@example.com")
	hash, size := putShareBlob(t, s, "wrong principal blob")

	fs := mustCreateShare(t, s, owner.ID, hash, size, "f.bin", "application/octet-stream")

	err := s.Meta().RevokeFileShare(ctx, other.ID, fs.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RevokeFileShare(wrong principal) = %v, want ErrNotFound", err)
	}
}

func testFileShareDestroyDeletesRow(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-destroy@example.com")
	hash, size := putShareBlob(t, s, "destroy blob content")

	fs := mustCreateShare(t, s, p.ID, hash, size, "destroy.bin", "application/octet-stream")

	if err := s.Meta().DestroyFileShare(ctx, p.ID, fs.ID); err != nil {
		t.Fatalf("DestroyFileShare: %v", err)
	}

	_, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetFileShareByID after destroy = %v, want ErrNotFound", err)
	}
}

func testFileShareGetByIDReturnsAllStates(t *testing.T, s store.Store) {
	// REQ-SHARE-11e: GetByID returns regardless of state.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-allstates@example.com")
	hash, size := putShareBlob(t, s, "all states blob")
	cfg := defaultShareConfig()

	// pending
	fs := mustCreateShare(t, s, p.ID, hash, size, "f.bin", "application/octet-stream")
	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil || got.State != store.FileShareStatePending {
		t.Fatalf("GetByID pending: err=%v state=%q", err, got.State)
	}

	// active
	if _, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{}); err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	got, err = s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil || got.State != store.FileShareStateActive {
		t.Fatalf("GetByID active: err=%v state=%q", err, got.State)
	}

	// revoked
	if err := s.Meta().RevokeFileShare(ctx, p.ID, fs.ID); err != nil {
		t.Fatalf("RevokeFileShare: %v", err)
	}
	got, err = s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil || got.State != store.FileShareStateRevoked {
		t.Fatalf("GetByID revoked: err=%v state=%q", err, got.State)
	}

	// not found
	_, err = s.Meta().GetFileShareByID(ctx, "no-such-token")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetByID(missing) = %v, want ErrNotFound", err)
	}
}

func testFileShareCountCap(t *testing.T, s store.Store) {
	// REQ-SHARE-12: create fails at MaxSharesPerPrincipal.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-cap@example.com")
	hash, size := putShareBlob(t, s, "cap blob")

	cfg := defaultShareConfig()
	cfg.MaxSharesPerPrincipal = 3 // small cap for test speed

	for i := 0; i < 3; i++ {
		_, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
			PrincipalID: p.ID,
			BlobHash:    hash,
			BlobSize:    size,
			Filename:    fmt.Sprintf("cap%d.bin", i),
			ContentType: "application/octet-stream",
			PendingTTL:  cfg.PendingTTL,
			Config:      cfg,
		})
		if err != nil {
			t.Fatalf("CreateFileShare #%d: %v", i, err)
		}
	}

	_, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "over.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if !errors.Is(err, store.ErrTooManyShares) {
		t.Fatalf("Create at cap = %v, want ErrTooManyShares", err)
	}
}

func testFileShareQuotaEnforced(t *testing.T, s store.Store) {
	// REQ-SHARE-50/51: quota measured over distinct blobs.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-quota@example.com")

	// Two distinct blobs: 100 bytes each.
	hash1, size1 := putShareBlob(t, s, strings.Repeat("A", 100))
	hash2, _ := putShareBlob(t, s, strings.Repeat("B", 100))

	cfg := defaultShareConfig()
	cfg.ShareQuotaPerPrincipal = 150 // allows two 100-byte blobs? No: 100+100 > 150

	// First share (hash1, 100 bytes): OK.
	_, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash1,
		BlobSize:    size1,
		Filename:    "a.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare (first, within quota): %v", err)
	}

	// Second share (hash2, another 100 bytes): total would be 200 > 150, fail.
	_, err = s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash2,
		BlobSize:    100, // same size as hash2 blob
		Filename:    "b.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if !errors.Is(err, store.ErrQuotaExceeded) {
		t.Fatalf("CreateFileShare (over quota) = %v, want ErrQuotaExceeded", err)
	}
}

func testFileShareQuotaDedupZeroCost(t *testing.T, s store.Store) {
	// REQ-SHARE-50: a second share of the SAME blob adds zero quota cost.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-quota-dedup@example.com")
	hash, size := putShareBlob(t, s, strings.Repeat("X", 200))

	cfg := defaultShareConfig()
	cfg.ShareQuotaPerPrincipal = 250 // enough for one blob

	// First share: 200 bytes, OK.
	_, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "x1.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare (first): %v", err)
	}

	// Second share of the SAME blob: zero marginal cost — must succeed.
	_, err = s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "x2.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare (same blob, zero cost) = %v, want nil", err)
	}
}

func testFileShareRecordDownload(t *testing.T, s store.Store) {
	// REQ-SHARE-30, REQ-SHARE-44: RecordDownload increments count atomically.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-dl@example.com")
	hash, size := putShareBlob(t, s, "download me")

	fs := mustCreateShare(t, s, p.ID, hash, size, "dl.bin", "application/octet-stream")

	for i := 1; i <= 3; i++ {
		post, err := s.Meta().RecordFileShareDownload(ctx, fs.ID)
		if err != nil {
			t.Fatalf("RecordDownload #%d: %v", i, err)
		}
		if post.DownloadCount != int64(i) {
			t.Fatalf("DownloadCount after #%d = %d, want %d", i, post.DownloadCount, i)
		}
		if post.LastDownloadedAt == nil {
			t.Fatalf("LastDownloadedAt nil after download #%d", i)
		}
	}

	// Not found on missing id.
	_, err := s.Meta().RecordFileShareDownload(ctx, "no-such-id")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RecordDownload(missing) = %v, want ErrNotFound", err)
	}
}

func testFileShareListByPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-list@example.com")
	other := mustInsertPrincipal(t, s, "share-list-other@example.com")
	hash, size := putShareBlob(t, s, "list blob")
	cfg := defaultShareConfig()

	// Insert 3 shares for p and 1 for other.
	for i := 0; i < 3; i++ {
		_, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
			PrincipalID: p.ID,
			BlobHash:    hash,
			BlobSize:    size,
			Filename:    fmt.Sprintf("list%d.bin", i),
			ContentType: "application/octet-stream",
			PendingTTL:  cfg.PendingTTL,
			Config:      cfg,
		})
		if err != nil {
			t.Fatalf("CreateFileShare #%d: %v", i, err)
		}
	}
	mustCreateShare(t, s, other.ID, hash, size, "other.bin", "application/octet-stream")

	got, err := s.Meta().ListFileSharesByPrincipal(ctx, p.ID, store.FileShareListFilter{})
	if err != nil {
		t.Fatalf("ListByPrincipal: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListByPrincipal len = %d, want 3", len(got))
	}
	// All belong to p.
	for _, fs := range got {
		if fs.PrincipalID != p.ID {
			t.Fatalf("got share belonging to %d, want %d", fs.PrincipalID, p.ID)
		}
	}

	// Filter by state.
	gotPending, err := s.Meta().ListFileSharesByPrincipal(ctx, p.ID, store.FileShareListFilter{State: store.FileShareStatePending})
	if err != nil {
		t.Fatalf("ListByPrincipal(pending): %v", err)
	}
	if len(gotPending) != 3 {
		t.Fatalf("ListByPrincipal(pending) len = %d, want 3", len(gotPending))
	}

	gotActive, err := s.Meta().ListFileSharesByPrincipal(ctx, p.ID, store.FileShareListFilter{State: store.FileShareStateActive})
	if err != nil {
		t.Fatalf("ListByPrincipal(active): %v", err)
	}
	if len(gotActive) != 0 {
		t.Fatalf("ListByPrincipal(active) len = %d, want 0", len(gotActive))
	}
}

func testFileShareSweepPending(t *testing.T, s store.Store) {
	// REQ-SHARE-23: sweeper deletes pending shares past pending_ttl.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-sweep-pending@example.com")
	hash, size := putShareBlob(t, s, "sweep pending blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "sp.bin", "application/octet-stream")

	cfg := defaultShareConfig()
	cfg.PendingTTL = time.Millisecond // tiny TTL so "now - PendingTTL" is in the past

	// Sweep with now = far future so pending_ttl has elapsed.
	farFuture := time.Now().Add(2 * time.Hour)
	stats, err := s.Meta().SweepFileShares(ctx, farFuture, cfg)
	if err != nil {
		t.Fatalf("SweepFileShares: %v", err)
	}
	if stats.DeletedPending != 1 {
		t.Fatalf("SweepFileShares.DeletedPending = %d, want 1", stats.DeletedPending)
	}
	if stats.DeletedExpired != 0 {
		t.Fatalf("SweepFileShares.DeletedExpired = %d, want 0", stats.DeletedExpired)
	}

	// Row must be gone.
	_, err = s.Meta().GetFileShareByID(ctx, fs.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetFileShareByID after sweep = %v, want ErrNotFound", err)
	}
}

func testFileShareSweepExpiredActive(t *testing.T, s store.Store) {
	// REQ-SHARE-23: sweeper deletes active shares past expires_at.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-sweep-active@example.com")
	hash, size := putShareBlob(t, s, "sweep active blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "sa.bin", "application/octet-stream")
	cfg := defaultShareConfig()

	// Confirm to active with a very short TTL.
	cfg.DefaultTTL = time.Millisecond
	cfg.MaxTTL = time.Millisecond
	if _, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{}); err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}

	// Sweep with now = far future.
	stats, err := s.Meta().SweepFileShares(ctx, time.Now().Add(time.Hour), cfg)
	if err != nil {
		t.Fatalf("SweepFileShares: %v", err)
	}
	if stats.DeletedExpired != 1 {
		t.Fatalf("SweepFileShares.DeletedExpired = %d, want 1", stats.DeletedExpired)
	}

	_, err = s.Meta().GetFileShareByID(ctx, fs.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetFileShareByID after sweep = %v, want ErrNotFound", err)
	}
}

func testFileShareSweepRevoked(t *testing.T, s store.Store) {
	// REQ-SHARE-23: sweeper deletes revoked shares past revoked_grace.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-sweep-revoked@example.com")
	hash, size := putShareBlob(t, s, "sweep revoked blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "sr.bin", "application/octet-stream")
	cfg := defaultShareConfig()

	if _, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{}); err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	if err := s.Meta().RevokeFileShare(ctx, p.ID, fs.ID); err != nil {
		t.Fatalf("RevokeFileShare: %v", err)
	}

	// Row is still visible immediately after revoke (grace period).
	cfg.RevokedGrace = time.Millisecond
	stats, err := s.Meta().SweepFileShares(ctx, time.Now().Add(time.Hour), cfg)
	if err != nil {
		t.Fatalf("SweepFileShares: %v", err)
	}
	if stats.DeletedRevoked != 1 {
		t.Fatalf("SweepFileShares.DeletedRevoked = %d, want 1", stats.DeletedRevoked)
	}

	_, err = s.Meta().GetFileShareByID(ctx, fs.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetFileShareByID after sweep = %v, want ErrNotFound", err)
	}
}

func testFileShareBlobGCLiveness(t *testing.T, s store.Store) {
	// REQ-SHARE-01, REQ-STORE-12: blob is kept alive by a share and
	// released after the share is swept.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-blob-gc@example.com")
	hash, size := putShareBlob(t, s, "gc liveness blob content")

	// Before any share: blob_refs should show the blob has been put but
	// refcount may be 0 (just uploaded, no message holding it).
	// IsFileShareBlobReferenced must be false.
	live, err := s.Meta().IsFileShareBlobReferenced(ctx, hash)
	if err != nil {
		t.Fatalf("IsFileShareBlobReferenced (before): %v", err)
	}
	if live {
		t.Fatal("IsFileShareBlobReferenced = true before any share, want false")
	}

	// Create a share: blob is now referenced.
	fs := mustCreateShare(t, s, p.ID, hash, size, "gc.bin", "application/octet-stream")
	live, err = s.Meta().IsFileShareBlobReferenced(ctx, hash)
	if err != nil {
		t.Fatalf("IsFileShareBlobReferenced (after create): %v", err)
	}
	if !live {
		t.Fatal("IsFileShareBlobReferenced = false after create, want true")
	}

	// Also check via GetBlobRef that ref_count > 0.
	_, refCount, err := s.Meta().GetBlobRef(ctx, hash)
	if err != nil {
		t.Fatalf("GetBlobRef: %v", err)
	}
	if refCount <= 0 {
		t.Fatalf("GetBlobRef.ref_count = %d, want > 0", refCount)
	}

	// Sweep the pending share away.
	cfg := defaultShareConfig()
	cfg.PendingTTL = time.Millisecond
	stats, err := s.Meta().SweepFileShares(ctx, time.Now().Add(time.Hour), cfg)
	if err != nil {
		t.Fatalf("SweepFileShares: %v", err)
	}
	if stats.DeletedPending != 1 {
		t.Fatalf("SweepFileShares.DeletedPending = %d, want 1", stats.DeletedPending)
	}

	// After sweep: share row gone, blob no longer referenced by shares.
	live, err = s.Meta().IsFileShareBlobReferenced(ctx, hash)
	if err != nil {
		t.Fatalf("IsFileShareBlobReferenced (after sweep): %v", err)
	}
	if live {
		t.Fatal("IsFileShareBlobReferenced = true after sweep, want false")
	}
	_ = fs
}

func testFileSharePrincipalCascade(t *testing.T, s store.Store) {
	// ON DELETE CASCADE: deleting the principal must delete all their shares.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-cascade@example.com")
	hash, size := putShareBlob(t, s, "cascade blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "cascade.bin", "application/octet-stream")

	if err := s.Meta().DeletePrincipal(ctx, p.ID); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}

	_, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetFileShareByID after principal delete = %v, want ErrNotFound", err)
	}
}

func testFileShareInvalidArguments(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-invalid@example.com")
	hash, size := putShareBlob(t, s, "invalid arg blob")
	cfg := defaultShareConfig()

	// Missing blob hash.
	_, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    "",
		BlobSize:    size,
		Filename:    "f.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Create(empty blob_hash) = %v, want ErrInvalidArgument", err)
	}

	// Missing filename.
	_, err = s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Create(empty filename) = %v, want ErrInvalidArgument", err)
	}

	// Missing content type.
	_, err = s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "f.bin",
		ContentType: "",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Create(empty content_type) = %v, want ErrInvalidArgument", err)
	}
}

func testFileSharePassword(t *testing.T, s store.Store) {
	// REQ-SHARE-11c: password_hash is stored; row round-trips correctly.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-pw@example.com")
	hash, size := putShareBlob(t, s, "password protected blob")
	cfg := defaultShareConfig()

	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "secret.zip",
		ContentType: "application/zip",
		PendingTTL:  cfg.PendingTTL,
		Password:    "hunter2",
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare (with password): %v", err)
	}
	if fs.PasswordHash == "" {
		t.Fatal("PasswordHash empty after create with password")
	}
	// Hash must not be the plaintext.
	if fs.PasswordHash == "hunter2" {
		t.Fatal("PasswordHash is plaintext, want Argon2id encoded")
	}
	// Round-trip via GetByID.
	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.PasswordHash == "" {
		t.Fatal("GetByID: PasswordHash empty, want persisted hash")
	}

	// No-password share: PasswordHash must be empty.
	fs2 := mustCreateShare(t, s, p.ID, hash, size, "open.bin", "application/octet-stream")
	got2, err := s.Meta().GetFileShareByID(ctx, fs2.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID (no pw): %v", err)
	}
	if got2.PasswordHash != "" {
		t.Fatalf("PasswordHash non-empty for no-password share: %q", got2.PasswordHash)
	}
}

func testFileShareMaxDownloads(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-maxdl@example.com")
	hash, size := putShareBlob(t, s, "max downloads blob")
	cfg := defaultShareConfig()

	var maxDL int64 = 2
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID:  p.ID,
		BlobHash:     hash,
		BlobSize:     size,
		Filename:     "maxdl.bin",
		ContentType:  "application/octet-stream",
		PendingTTL:   cfg.PendingTTL,
		MaxDownloads: &maxDL,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}
	if fs.MaxDownloads == nil || *fs.MaxDownloads != 2 {
		t.Fatalf("MaxDownloads = %v, want 2", fs.MaxDownloads)
	}

	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.MaxDownloads == nil || *got.MaxDownloads != 2 {
		t.Fatalf("GetByID.MaxDownloads = %v, want 2", got.MaxDownloads)
	}
}

// --- REQ-SHARE-42: UpdateFileShareExpiry compliance tests -----------------

func testFileShareUpdateExpiryShorten(t *testing.T, s store.Store) {
	// Successful shorten: newExpiry in the future and before current ExpiresAt.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-expiry-shorten@example.com")
	hash, size := putShareBlob(t, s, "expiry shorten blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "f.bin", "application/octet-stream")
	cfg := defaultShareConfig()
	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}

	// Shorten by a week — must succeed.
	newExpiry := active.ExpiresAt.Add(-7 * 24 * time.Hour)
	if err := s.Meta().UpdateFileShareExpiry(ctx, p.ID, fs.ID, newExpiry); err != nil {
		t.Fatalf("UpdateFileShareExpiry (shorten): %v", err)
	}

	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID after shorten: %v", err)
	}
	// Allow 1-second rounding slack from microsecond storage.
	diff := got.ExpiresAt.Sub(newExpiry)
	if diff < -time.Second || diff > time.Second {
		t.Fatalf("ExpiresAt after shorten = %v, want ~%v (diff %v)", got.ExpiresAt, newExpiry, diff)
	}
}

func testFileShareUpdateExpiryRejectNotShortening(t *testing.T, s store.Store) {
	// Reject when newExpiry == current ExpiresAt (not shorter).
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-expiry-not-short@example.com")
	hash, size := putShareBlob(t, s, "not shorter blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "f.bin", "application/octet-stream")
	cfg := defaultShareConfig()
	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}

	// Same instant as current: must fail.
	err = s.Meta().UpdateFileShareExpiry(ctx, p.ID, fs.ID, active.ExpiresAt)
	if !errors.Is(err, store.ErrShareExpiryNotShortenable) {
		t.Fatalf("UpdateFileShareExpiry(same expiry) = %v, want ErrShareExpiryNotShortenable", err)
	}
}

func testFileShareUpdateExpiryRejectExtend(t *testing.T, s store.Store) {
	// Reject when newExpiry is after current ExpiresAt (extension attempt).
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-expiry-extend@example.com")
	hash, size := putShareBlob(t, s, "extend blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "f.bin", "application/octet-stream")
	cfg := defaultShareConfig()
	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}

	extended := active.ExpiresAt.Add(24 * time.Hour)
	err = s.Meta().UpdateFileShareExpiry(ctx, p.ID, fs.ID, extended)
	if !errors.Is(err, store.ErrShareExpiryNotShortenable) {
		t.Fatalf("UpdateFileShareExpiry(extend) = %v, want ErrShareExpiryNotShortenable", err)
	}
}

func testFileShareUpdateExpiryWrongPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "share-expiry-owner@example.com")
	other := mustInsertPrincipal(t, s, "share-expiry-other@example.com")
	hash, size := putShareBlob(t, s, "wrong principal expiry blob")

	fs := mustCreateShare(t, s, owner.ID, hash, size, "f.bin", "application/octet-stream")
	cfg := defaultShareConfig()
	active, err := s.Meta().ConfirmFileShare(ctx, owner.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}

	newExpiry := active.ExpiresAt.Add(-time.Hour)
	err = s.Meta().UpdateFileShareExpiry(ctx, other.ID, fs.ID, newExpiry)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateFileShareExpiry(wrong principal) = %v, want ErrNotFound", err)
	}
}

func testFileShareUpdateExpiryRevokedShare(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-expiry-revoked@example.com")
	hash, size := putShareBlob(t, s, "revoked expiry blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "f.bin", "application/octet-stream")
	cfg := defaultShareConfig()
	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	if err := s.Meta().RevokeFileShare(ctx, p.ID, fs.ID); err != nil {
		t.Fatalf("RevokeFileShare: %v", err)
	}

	newExpiry := active.ExpiresAt.Add(-time.Hour)
	err = s.Meta().UpdateFileShareExpiry(ctx, p.ID, fs.ID, newExpiry)
	if !errors.Is(err, store.ErrShareNotConfirmable) {
		t.Fatalf("UpdateFileShareExpiry(revoked) = %v, want ErrShareNotConfirmable", err)
	}
}

// --- REQ-SHARE-42: UpdateFileShareMaxDownloads compliance tests -----------

func testFileShareUpdateMaxDownloadsLower(t *testing.T, s store.Store) {
	// Successful lower: newMax < current MaxDownloads and >= download_count.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-maxdl-lower@example.com")
	hash, size := putShareBlob(t, s, "lower max dl blob")

	var maxDL int64 = 10
	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID:  p.ID,
		BlobHash:     hash,
		BlobSize:     size,
		Filename:     "lower.bin",
		ContentType:  "application/octet-stream",
		PendingTTL:   cfg.PendingTTL,
		MaxDownloads: &maxDL,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}

	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 5); err != nil {
		t.Fatalf("UpdateFileShareMaxDownloads(lower): %v", err)
	}

	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID after lower: %v", err)
	}
	if got.MaxDownloads == nil || *got.MaxDownloads != 5 {
		t.Fatalf("MaxDownloads after lower = %v, want 5", got.MaxDownloads)
	}
}

func testFileShareUpdateMaxDownloadsRejectRaise(t *testing.T, s store.Store) {
	// Reject when newMax >= current MaxDownloads.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-maxdl-raise@example.com")
	hash, size := putShareBlob(t, s, "raise max dl blob")

	var maxDL int64 = 5
	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID:  p.ID,
		BlobHash:     hash,
		BlobSize:     size,
		Filename:     "raise.bin",
		ContentType:  "application/octet-stream",
		PendingTTL:   cfg.PendingTTL,
		MaxDownloads: &maxDL,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}

	// Same value: must fail.
	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 5); !errors.Is(err, store.ErrShareMaxDownloadsNotLowerable) {
		t.Fatalf("UpdateFileShareMaxDownloads(same) = %v, want ErrShareMaxDownloadsNotLowerable", err)
	}

	// Higher value: must fail.
	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 10); !errors.Is(err, store.ErrShareMaxDownloadsNotLowerable) {
		t.Fatalf("UpdateFileShareMaxDownloads(raise) = %v, want ErrShareMaxDownloadsNotLowerable", err)
	}
}

func testFileShareUpdateMaxDownloadsRejectBelowDownloadCount(t *testing.T, s store.Store) {
	// Reject when newMax < current download_count.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-maxdl-belowcount@example.com")
	hash, size := putShareBlob(t, s, "below count blob")

	var maxDL int64 = 10
	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID:  p.ID,
		BlobHash:     hash,
		BlobSize:     size,
		Filename:     "count.bin",
		ContentType:  "application/octet-stream",
		PendingTTL:   cfg.PendingTTL,
		MaxDownloads: &maxDL,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}

	// Record 3 downloads.
	for i := 0; i < 3; i++ {
		if _, err := s.Meta().RecordFileShareDownload(ctx, fs.ID); err != nil {
			t.Fatalf("RecordFileShareDownload #%d: %v", i+1, err)
		}
	}

	// newMax = 2 < download_count 3: must fail.
	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 2); !errors.Is(err, store.ErrShareMaxDownloadsNotLowerable) {
		t.Fatalf("UpdateFileShareMaxDownloads(below count) = %v, want ErrShareMaxDownloadsNotLowerable", err)
	}

	// newMax = 3 == download_count: must succeed (>= is OK, only < is rejected).
	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 3); err != nil {
		t.Fatalf("UpdateFileShareMaxDownloads(equal to download_count): %v", err)
	}
}

func testFileShareUpdateMaxDownloadsUnlimitedToFinite(t *testing.T, s store.Store) {
	// When current MaxDownloads is nil (unlimited), lowering to a finite
	// cap is allowed as long as newMax >= download_count.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-maxdl-unlimited@example.com")
	hash, size := putShareBlob(t, s, "unlimited blob")

	// Create without max_downloads cap (unlimited).
	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "unlimited.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}
	if fs.MaxDownloads != nil {
		t.Fatalf("expected unlimited share, got MaxDownloads=%v", fs.MaxDownloads)
	}

	// Record 2 downloads.
	for i := 0; i < 2; i++ {
		if _, err := s.Meta().RecordFileShareDownload(ctx, fs.ID); err != nil {
			t.Fatalf("RecordFileShareDownload #%d: %v", i+1, err)
		}
	}

	// newMax = 1 < download_count 2: must fail even when coming from unlimited.
	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 1); !errors.Is(err, store.ErrShareMaxDownloadsNotLowerable) {
		t.Fatalf("UpdateFileShareMaxDownloads(unlimited->1 < count) = %v, want ErrShareMaxDownloadsNotLowerable", err)
	}

	// newMax = 5 >= download_count 2: must succeed.
	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 5); err != nil {
		t.Fatalf("UpdateFileShareMaxDownloads(unlimited->5): %v", err)
	}

	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID after cap: %v", err)
	}
	if got.MaxDownloads == nil || *got.MaxDownloads != 5 {
		t.Fatalf("MaxDownloads after cap = %v, want 5", got.MaxDownloads)
	}
}

func testFileShareUpdateMaxDownloadsWrongPrincipal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	owner := mustInsertPrincipal(t, s, "share-maxdl-owner@example.com")
	other := mustInsertPrincipal(t, s, "share-maxdl-other@example.com")
	hash, size := putShareBlob(t, s, "wrong principal maxdl blob")

	var maxDL int64 = 10
	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID:  owner.ID,
		BlobHash:     hash,
		BlobSize:     size,
		Filename:     "f.bin",
		ContentType:  "application/octet-stream",
		PendingTTL:   cfg.PendingTTL,
		MaxDownloads: &maxDL,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}

	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, other.ID, fs.ID, 5); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateFileShareMaxDownloads(wrong principal) = %v, want ErrNotFound", err)
	}
}

func testFileShareUpdateMaxDownloadsRevokedShare(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-maxdl-revoked@example.com")
	hash, size := putShareBlob(t, s, "revoked maxdl blob")

	var maxDL int64 = 10
	cfg := defaultShareConfig()
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID:  p.ID,
		BlobHash:     hash,
		BlobSize:     size,
		Filename:     "f.bin",
		ContentType:  "application/octet-stream",
		PendingTTL:   cfg.PendingTTL,
		MaxDownloads: &maxDL,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}
	if _, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{}); err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	if err := s.Meta().RevokeFileShare(ctx, p.ID, fs.ID); err != nil {
		t.Fatalf("RevokeFileShare: %v", err)
	}

	if err := s.Meta().UpdateFileShareMaxDownloads(ctx, p.ID, fs.ID, 5); !errors.Is(err, store.ErrShareNotConfirmable) {
		t.Fatalf("UpdateFileShareMaxDownloads(revoked) = %v, want ErrShareNotConfirmable", err)
	}
}

// --- REQ-SHARE-04: FileShareSource / message back-reference tests ----------

func testFileShareSourceRoundTrip(t *testing.T, s store.Store) {
	// Confirm with a full source; GetByID and ListByPrincipal must both
	// surface all three fields correctly decoded.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-src-rt@example.com")
	hash, size := putShareBlob(t, s, "source round-trip blob")
	cfg := defaultShareConfig()

	fs := mustCreateShare(t, s, p.ID, hash, size, "src.bin", "application/octet-stream")

	src := store.FileShareSource{
		MessageID:  "msg-abc-123",
		Subject:    "Important contract draft",
		Recipients: []string{"alice@example.com", "bob@example.com", "carol@example.com"},
	}
	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, src)
	if err != nil {
		t.Fatalf("ConfirmFileShare(with source): %v", err)
	}
	if active.State != store.FileShareStateActive {
		t.Fatalf("State = %q, want active", active.State)
	}
	if active.SourceMessageID != src.MessageID {
		t.Fatalf("ConfirmFileShare returned SourceMessageID = %q, want %q", active.SourceMessageID, src.MessageID)
	}
	if active.SourceSubject != src.Subject {
		t.Fatalf("ConfirmFileShare returned SourceSubject = %q, want %q", active.SourceSubject, src.Subject)
	}
	if len(active.SourceRecipients) != len(src.Recipients) {
		t.Fatalf("ConfirmFileShare returned SourceRecipients len = %d, want %d", len(active.SourceRecipients), len(src.Recipients))
	}
	for i, r := range src.Recipients {
		if active.SourceRecipients[i] != r {
			t.Fatalf("SourceRecipients[%d] = %q, want %q", i, active.SourceRecipients[i], r)
		}
	}

	// Verify persistence via GetByID.
	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.SourceMessageID != src.MessageID {
		t.Fatalf("GetByID.SourceMessageID = %q, want %q", got.SourceMessageID, src.MessageID)
	}
	if got.SourceSubject != src.Subject {
		t.Fatalf("GetByID.SourceSubject = %q, want %q", got.SourceSubject, src.Subject)
	}
	if len(got.SourceRecipients) != len(src.Recipients) {
		t.Fatalf("GetByID.SourceRecipients len = %d, want %d", len(got.SourceRecipients), len(src.Recipients))
	}
	for i, r := range src.Recipients {
		if got.SourceRecipients[i] != r {
			t.Fatalf("GetByID.SourceRecipients[%d] = %q, want %q", i, got.SourceRecipients[i], r)
		}
	}

	// Verify persistence via ListByPrincipal.
	list, err := s.Meta().ListFileSharesByPrincipal(ctx, p.ID, store.FileShareListFilter{})
	if err != nil {
		t.Fatalf("ListFileSharesByPrincipal: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListByPrincipal len = %d, want 1", len(list))
	}
	lst := list[0]
	if lst.SourceMessageID != src.MessageID {
		t.Fatalf("List.SourceMessageID = %q, want %q", lst.SourceMessageID, src.MessageID)
	}
	if lst.SourceSubject != src.Subject {
		t.Fatalf("List.SourceSubject = %q, want %q", lst.SourceSubject, src.Subject)
	}
	if len(lst.SourceRecipients) != len(src.Recipients) {
		t.Fatalf("List.SourceRecipients len = %d, want %d", len(lst.SourceRecipients), len(src.Recipients))
	}
}

func testFileShareSourceEmptyLeavesNULL(t *testing.T, s store.Store) {
	// Confirm with a zero source: all three source columns must remain
	// empty/nil in the returned row and in subsequent reads.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-src-null@example.com")
	hash, size := putShareBlob(t, s, "source null blob")
	cfg := defaultShareConfig()

	fs := mustCreateShare(t, s, p.ID, hash, size, "null.bin", "application/octet-stream")

	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare(empty source): %v", err)
	}
	if active.SourceMessageID != "" {
		t.Fatalf("SourceMessageID = %q, want empty", active.SourceMessageID)
	}
	if active.SourceSubject != "" {
		t.Fatalf("SourceSubject = %q, want empty", active.SourceSubject)
	}
	if len(active.SourceRecipients) != 0 {
		t.Fatalf("SourceRecipients = %v, want empty", active.SourceRecipients)
	}

	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.SourceMessageID != "" {
		t.Fatalf("GetByID.SourceMessageID = %q, want empty", got.SourceMessageID)
	}
	if got.SourceSubject != "" {
		t.Fatalf("GetByID.SourceSubject = %q, want empty", got.SourceSubject)
	}
	if len(got.SourceRecipients) != 0 {
		t.Fatalf("GetByID.SourceRecipients = %v, want empty", got.SourceRecipients)
	}
}

func testFileShareSourcePendingIsNULL(t *testing.T, s store.Store) {
	// A newly created (pending) share must have empty source fields.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-src-pending@example.com")
	hash, size := putShareBlob(t, s, "source pending blob")

	fs := mustCreateShare(t, s, p.ID, hash, size, "pending.bin", "application/octet-stream")

	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.SourceMessageID != "" || got.SourceSubject != "" || len(got.SourceRecipients) != 0 {
		t.Fatalf("pending share has non-empty source fields: id=%q subj=%q recip=%v",
			got.SourceMessageID, got.SourceSubject, got.SourceRecipients)
	}
}

func testFileShareSourceReconfirmUpdates(t *testing.T, s store.Store) {
	// Re-confirming an already-active share with a non-empty source MUST
	// update the source columns. Re-confirming with an empty source MUST
	// NOT overwrite existing source context.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-src-reconfirm@example.com")
	hash, size := putShareBlob(t, s, "reconfirm source blob")
	cfg := defaultShareConfig()

	fs := mustCreateShare(t, s, p.ID, hash, size, "rc.bin", "application/octet-stream")

	// First confirm: with full source.
	src := store.FileShareSource{
		MessageID:  "msg-first",
		Subject:    "First send",
		Recipients: []string{"alice@example.com"},
	}
	if _, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, src); err != nil {
		t.Fatalf("ConfirmFileShare (first): %v", err)
	}

	// Re-confirm with empty source: existing source must be preserved.
	active2, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare (re-confirm empty): %v", err)
	}
	if active2.SourceMessageID != src.MessageID {
		t.Fatalf("after empty re-confirm: SourceMessageID = %q, want %q", active2.SourceMessageID, src.MessageID)
	}

	// Verify the stored row is unchanged too.
	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.SourceMessageID != src.MessageID {
		t.Fatalf("GetByID after empty re-confirm: SourceMessageID = %q, want %q", got.SourceMessageID, src.MessageID)
	}

	// Re-confirm with a fresh non-empty source: columns must be updated.
	src2 := store.FileShareSource{
		MessageID:  "msg-second",
		Subject:    "Second send",
		Recipients: []string{"bob@example.com", "carol@example.com"},
	}
	active3, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, src2)
	if err != nil {
		t.Fatalf("ConfirmFileShare (re-confirm with new source): %v", err)
	}
	if active3.SourceMessageID != src2.MessageID {
		t.Fatalf("after new re-confirm: SourceMessageID = %q, want %q", active3.SourceMessageID, src2.MessageID)
	}
	if active3.SourceSubject != src2.Subject {
		t.Fatalf("after new re-confirm: SourceSubject = %q, want %q", active3.SourceSubject, src2.Subject)
	}
	if len(active3.SourceRecipients) != len(src2.Recipients) {
		t.Fatalf("after new re-confirm: SourceRecipients len = %d, want %d", len(active3.SourceRecipients), len(src2.Recipients))
	}
}

// -- REQ-SHARE-02, REQ-SHARE-20, REQ-SHARE-41: per-share retention (issue #290) --

func testFileShareRetentionAppliedOnConfirm(t *testing.T, s store.Store) {
	// A share created with a retention shorter than DefaultTTL confirms
	// to now + retention, not now + DefaultTTL.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-retention-applied@example.com")
	hash, size := putShareBlob(t, s, "retention applied blob")
	cfg := defaultShareConfig() // DefaultTTL=30d, MaxTTL=90d

	retention := 5 * 24 * time.Hour
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "retained.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Retention:   retention,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}
	if fs.Retention != retention {
		t.Fatalf("Retention after create = %v, want %v", fs.Retention, retention)
	}

	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	wantExpires := fs.CreatedAt.Add(retention)
	// Allow small clock slack either side.
	if diff := active.ExpiresAt.Sub(wantExpires); diff < -5*time.Second || diff > 5*time.Second {
		t.Fatalf("ExpiresAt = %v, want ~%v (created %v + retention %v)",
			active.ExpiresAt, wantExpires, fs.CreatedAt, retention)
	}
	if active.Retention != retention {
		t.Fatalf("Retention after confirm = %v, want %v", active.Retention, retention)
	}

	// GetFileShareByID must also surface the retention.
	got, err := s.Meta().GetFileShareByID(ctx, fs.ID)
	if err != nil {
		t.Fatalf("GetFileShareByID: %v", err)
	}
	if got.Retention != retention {
		t.Fatalf("GetFileShareByID Retention = %v, want %v", got.Retention, retention)
	}

	// ListFileSharesByPrincipal must also surface the retention.
	list, err := s.Meta().ListFileSharesByPrincipal(ctx, p.ID, store.FileShareListFilter{})
	if err != nil {
		t.Fatalf("ListFileSharesByPrincipal: %v", err)
	}
	found := false
	for _, row := range list {
		if row.ID == fs.ID {
			found = true
			if row.Retention != retention {
				t.Fatalf("ListFileSharesByPrincipal Retention = %v, want %v", row.Retention, retention)
			}
		}
	}
	if !found {
		t.Fatalf("share %s not found in ListFileSharesByPrincipal", fs.ID)
	}
}

func testFileShareRetentionUnsetFallsBackToDefaultTTL(t *testing.T, s store.Store) {
	// A share created without a retention (retention_us = 0, the state
	// every pre-existing row carries after the migration) confirms to
	// now + DefaultTTL: unchanged legacy behaviour.
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-retention-unset@example.com")
	hash, size := putShareBlob(t, s, "retention unset blob")
	cfg := defaultShareConfig()

	fs := mustCreateShare(t, s, p.ID, hash, size, "legacy.bin", "application/octet-stream")
	if fs.Retention != 0 {
		t.Fatalf("Retention after create with no Retention set = %v, want 0", fs.Retention)
	}

	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	wantExpires := fs.CreatedAt.Add(cfg.DefaultTTL)
	if diff := active.ExpiresAt.Sub(wantExpires); diff < -5*time.Second || diff > 5*time.Second {
		t.Fatalf("ExpiresAt = %v, want ~%v (created %v + DefaultTTL %v)",
			active.ExpiresAt, wantExpires, fs.CreatedAt, cfg.DefaultTTL)
	}
}

func testFileShareRetentionClampedToMaxTTLOnCreate(t *testing.T, s store.Store) {
	// A requested retention above MaxTTL is clamped to MaxTTL at create,
	// defensively, independent of any clamp already applied by the
	// caller (REQ-SHARE-41).
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "share-retention-clamp@example.com")
	hash, size := putShareBlob(t, s, "retention clamp blob")
	cfg := defaultShareConfig() // MaxTTL=90d

	requested := 365 * 24 * time.Hour // far above MaxTTL
	fs, err := s.Meta().CreateFileShare(ctx, store.FileShareCreate{
		PrincipalID: p.ID,
		BlobHash:    hash,
		BlobSize:    size,
		Filename:    "clamped.bin",
		ContentType: "application/octet-stream",
		PendingTTL:  cfg.PendingTTL,
		Retention:   requested,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("CreateFileShare: %v", err)
	}
	if fs.Retention != cfg.MaxTTL {
		t.Fatalf("Retention after create with over-ceiling request = %v, want clamped %v", fs.Retention, cfg.MaxTTL)
	}

	active, err := s.Meta().ConfirmFileShare(ctx, p.ID, fs.ID, cfg, store.FileShareSource{})
	if err != nil {
		t.Fatalf("ConfirmFileShare: %v", err)
	}
	wantExpires := fs.CreatedAt.Add(cfg.MaxTTL)
	if diff := active.ExpiresAt.Sub(wantExpires); diff < -5*time.Second || diff > 5*time.Second {
		t.Fatalf("ExpiresAt = %v, want ~%v (created %v + MaxTTL %v)",
			active.ExpiresAt, wantExpires, fs.CreatedAt, cfg.MaxTTL)
	}
}
