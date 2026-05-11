package storetest

import (
	"errors"
	"strconv"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// REQ-TAG-10..11, REQ-TAG-30..32 — tagged-address store compliance.

// insertTaggedIdentity inserts a verified jmap_identities row that
// tagged-address filters can reference. The base_identity_id FK is
// enforced at the DB level so every test that exercises a filter row
// needs a real identity to point at.
func insertTaggedIdentity(t *testing.T, s store.Store, pid store.PrincipalID, idSuffix string) string {
	t.Helper()
	ctx := ctxT(t)
	id := "tag-id-" + idSuffix
	row := store.JMAPIdentity{
		ID:          id,
		PrincipalID: pid,
		Email:       idSuffix + "@example.com",
		MayDelete:   true,
	}
	if err := s.Meta().InsertJMAPIdentity(ctx, row); err != nil {
		t.Fatalf("InsertJMAPIdentity(%s): %v", id, err)
	}
	return id
}

func testTaggedAddressFilterInsertGetRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-rt@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "rt")

	f := store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "amazon",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Shopping",
	}
	if err := s.Meta().InsertTaggedAddressFilter(ctx, f); err != nil {
		t.Fatalf("InsertTaggedAddressFilter: %v", err)
	}

	got, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, "amazon")
	if err != nil {
		t.Fatalf("GetTaggedAddressFilter: %v", err)
	}
	if got.ID == "" {
		t.Fatalf("Get returned empty ID, want store-allocated opaque id")
	}
	if got.PrincipalID != p.ID {
		t.Fatalf("PrincipalID = %d, want %d", got.PrincipalID, p.ID)
	}
	if got.BaseIdentityID != identityID {
		t.Fatalf("BaseIdentityID = %q, want %q", got.BaseIdentityID, identityID)
	}
	if got.Suffix != "amazon" {
		t.Fatalf("Suffix = %q, want %q", got.Suffix, "amazon")
	}
	if got.Action != store.TaggedAddressActionLabel {
		t.Fatalf("Action = %q, want %q", got.Action, store.TaggedAddressActionLabel)
	}
	if got.LabelName != "Shopping" {
		t.Fatalf("LabelName = %q, want Shopping", got.LabelName)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt not stamped")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt not stamped")
	}

	// Missing tuple -> ErrNotFound.
	if _, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}
}

func testTaggedAddressFilterCaseInsensitiveSuffix(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-case@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "case")

	// Insert with mixed-case suffix; store must normalise to lower.
	if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "NewsLetters",
		Action:         store.TaggedAddressActionLabelArchive,
		LabelName:      "Newsletters",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Read with various cases — all hit the same row.
	for _, q := range []string{"newsletters", "NEWSLETTERS", "Newsletters"} {
		got, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, q)
		if err != nil {
			t.Fatalf("Get(%q): %v", q, err)
		}
		if got.Suffix != "newsletters" {
			t.Fatalf("Get(%q).Suffix = %q, want lowercase", q, got.Suffix)
		}
	}

	// Duplicate insert with different casing must collide.
	err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "NEWSLETTERS",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Other",
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate insert (different case) = %v, want ErrConflict", err)
	}
}

func testTaggedAddressFilterUpdate(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-upd@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "upd")

	if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "github",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Code",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	pre, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, "github")
	if err != nil {
		t.Fatalf("Get pre: %v", err)
	}

	if err := s.Meta().UpdateTaggedAddressFilter(ctx, pre.ID,
		store.TaggedAddressActionLabelArchiveRead, "Code/Notifications"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	post, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, "github")
	if err != nil {
		t.Fatalf("Get post: %v", err)
	}
	if post.Action != store.TaggedAddressActionLabelArchiveRead {
		t.Fatalf("Action after Update = %q, want label_archive_read", post.Action)
	}
	if post.LabelName != "Code/Notifications" {
		t.Fatalf("LabelName after Update = %q, want Code/Notifications", post.LabelName)
	}
	// Identity columns immutable.
	if post.Suffix != pre.Suffix || post.BaseIdentityID != pre.BaseIdentityID {
		t.Fatalf("Update mutated identity columns: pre=%+v post=%+v", pre, post)
	}
	if post.UpdatedAt.Before(pre.UpdatedAt) {
		// Fake clocks may not advance between sub-microsecond writes,
		// so the check is "did not regress" rather than strict-after.
		t.Fatalf("UpdatedAt regressed: pre=%v post=%v", pre.UpdatedAt, post.UpdatedAt)
	}

	// Missing id -> ErrNotFound.
	err = s.Meta().UpdateTaggedAddressFilter(ctx, "no-such-id",
		store.TaggedAddressActionLabel, "X")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update missing = %v, want ErrNotFound", err)
	}

	// Invalid action -> ErrInvalidArgument.
	err = s.Meta().UpdateTaggedAddressFilter(ctx, pre.ID, "bogus", "X")
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Update bogus action = %v, want ErrInvalidArgument", err)
	}
}

func testTaggedAddressFilterListOrder(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-list@example.com")
	identA := insertTaggedIdentity(t, s, p.ID, "lA")
	identB := insertTaggedIdentity(t, s, p.ID, "lB")

	// Insert with deliberately unsorted (identity, suffix) so the
	// store ORDER BY does the work, not insertion order.
	inserts := []store.TaggedAddressFilter{
		{PrincipalID: p.ID, BaseIdentityID: identB, Suffix: "zee", Action: store.TaggedAddressActionLabel, LabelName: "Z"},
		{PrincipalID: p.ID, BaseIdentityID: identA, Suffix: "beta", Action: store.TaggedAddressActionLabel, LabelName: "B"},
		{PrincipalID: p.ID, BaseIdentityID: identA, Suffix: "alpha", Action: store.TaggedAddressActionLabel, LabelName: "A"},
		{PrincipalID: p.ID, BaseIdentityID: identB, Suffix: "aaa", Action: store.TaggedAddressActionLabel, LabelName: "AA"},
	}
	for i, f := range inserts {
		if err := s.Meta().InsertTaggedAddressFilter(ctx, f); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	got, err := s.Meta().ListTaggedAddressFiltersForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("List len = %d, want 4", len(got))
	}
	// Expected order: identA/alpha, identA/beta, identB/aaa, identB/zee.
	want := []struct{ identity, suffix string }{
		{identA, "alpha"},
		{identA, "beta"},
		{identB, "aaa"},
		{identB, "zee"},
	}
	for i, w := range want {
		if got[i].BaseIdentityID != w.identity || got[i].Suffix != w.suffix {
			t.Fatalf("List[%d] = (%q,%q), want (%q,%q)",
				i, got[i].BaseIdentityID, got[i].Suffix, w.identity, w.suffix)
		}
	}

	// Empty principal returns empty (no error).
	other := mustInsertPrincipal(t, s, "tag-list-empty@example.com")
	emptyList, err := s.Meta().ListTaggedAddressFiltersForPrincipal(ctx, other.ID)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(emptyList) != 0 {
		t.Fatalf("List empty len = %d, want 0", len(emptyList))
	}
}

func testTaggedAddressFilterDuplicateConflict(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-dup@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "dup")

	f := store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "stuff",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Stuff",
	}
	if err := s.Meta().InsertTaggedAddressFilter(ctx, f); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	err := s.Meta().InsertTaggedAddressFilter(ctx, f)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate Insert = %v, want ErrConflict", err)
	}
}

func testTaggedAddressFilterInvalidAction(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-invalid-action@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "inv")

	err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "anything",
		Action:         "delete_forever", // not in the allowed set
		LabelName:      "x",
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Insert bogus action = %v, want ErrInvalidArgument", err)
	}
}

func testTaggedAddressFilterCap(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-cap@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "cap")

	// Insert exactly MaxTaggedAddressFiltersPerPrincipal rows; each
	// must succeed.
	for i := 0; i < store.MaxTaggedAddressFiltersPerPrincipal; i++ {
		if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
			PrincipalID:    p.ID,
			BaseIdentityID: identityID,
			Suffix:         "cap-" + strconv.Itoa(i),
			Action:         store.TaggedAddressActionLabel,
			LabelName:      "L" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
	}
	// The next insert must trip the cap sentinel.
	err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "over-cap",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Z",
	})
	if !errors.Is(err, store.ErrTooManyFilters) {
		t.Fatalf("Insert at cap = %v, want ErrTooManyFilters", err)
	}
}

func testTaggedAddressDismissalRoundTrip(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-dism-rt@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "dismrt")

	d := store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "Promo",
	}
	if err := s.Meta().InsertTaggedAddressDismissal(ctx, d); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List len = %d, want 1", len(got))
	}
	if got[0].Suffix != "promo" {
		t.Fatalf("Suffix = %q, want lower-cased %q", got[0].Suffix, "promo")
	}
	if got[0].BaseIdentityID != identityID {
		t.Fatalf("BaseIdentityID = %q, want %q", got[0].BaseIdentityID, identityID)
	}
	if got[0].DismissedAt.IsZero() {
		t.Fatalf("DismissedAt not stamped")
	}
}

func testTaggedAddressDismissalIdempotent(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-dism-idemp@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "didemp")

	d := store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "again",
	}
	for i := 0; i < 3; i++ {
		if err := s.Meta().InsertTaggedAddressDismissal(ctx, d); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
	}
	got, err := s.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("idempotent Insert produced %d rows, want 1", len(got))
	}
}

func testTaggedAddressDismissalDelete(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-dism-del@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "ddel")

	if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "byebye",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Delete with different-case suffix — store lower-cases on read too.
	if err := s.Meta().DeleteTaggedAddressDismissal(ctx, p.ID, identityID, "BYEBYE"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Delete left %d rows: %+v", len(got), got)
	}
	// Idempotent: second Delete is a no-op (no error).
	if err := s.Meta().DeleteTaggedAddressDismissal(ctx, p.ID, identityID, "byebye"); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
}

func testTaggedAddressDismissalCap(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-dism-cap@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "dcap")

	for i := 0; i < store.MaxTaggedAddressDismissalsPerPrincipal; i++ {
		if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
			PrincipalID:    p.ID,
			BaseIdentityID: identityID,
			Suffix:         "d-" + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("Insert #%d: %v", i, err)
		}
	}
	err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "over-cap",
	})
	if !errors.Is(err, store.ErrTooManyDismissals) {
		t.Fatalf("Insert at cap = %v, want ErrTooManyDismissals", err)
	}
	// Idempotent re-insert of an EXISTING row stays a no-op even at the
	// cap (no new row, so no cap check triggers).
	if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "d-0",
	}); err != nil {
		t.Fatalf("re-insert existing at cap = %v, want nil", err)
	}
}

func testTaggedAddressDeleteFilterCascadesDismissal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-delcascade@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "delcasc")

	// Dismiss FIRST, then add a filter for the same suffix — exactly
	// the "user changed their mind" path REQ-TAG-61 calls out.
	if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "shopping",
	}); err != nil {
		t.Fatalf("Insert dismissal: %v", err)
	}
	if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "shopping",
		Action:         store.TaggedAddressActionLabelArchive,
		LabelName:      "Shopping",
	}); err != nil {
		t.Fatalf("Insert filter: %v", err)
	}
	pre, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, "shopping")
	if err != nil {
		t.Fatalf("Get filter: %v", err)
	}

	// Delete the filter. The store MUST atomically delete the matching
	// dismissal so the SPA banner re-appears on next mail.
	suffix, baseID, principalID, err := s.Meta().DeleteTaggedAddressFilter(ctx, pre.ID)
	if err != nil {
		t.Fatalf("DeleteTaggedAddressFilter: %v", err)
	}
	if suffix != "shopping" || baseID != identityID || principalID != p.ID {
		t.Fatalf("Delete return triple = (%q, %q, %d), want (%q, %q, %d)",
			suffix, baseID, principalID, "shopping", identityID, p.ID)
	}

	// Filter gone.
	if _, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, "shopping"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get post-delete = %v, want ErrNotFound", err)
	}
	// Dismissal also gone (REQ-TAG-61).
	dismissals, err := s.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List dismissals: %v", err)
	}
	for _, d := range dismissals {
		if d.BaseIdentityID == identityID && d.Suffix == "shopping" {
			t.Fatalf("dismissal not cascaded: %+v", d)
		}
	}

	// Delete on missing id -> ErrNotFound; no dismissal cascade fires.
	if _, _, _, err := s.Meta().DeleteTaggedAddressFilter(ctx, "no-such-filter"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete missing = %v, want ErrNotFound", err)
	}
}

func testTaggedAddressDeleteFilterNoDismissalIsOK(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-delnodism@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "delnodism")

	if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "solo",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Solo",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	pre, err := s.Meta().GetTaggedAddressFilter(ctx, p.ID, identityID, "solo")
	if err != nil {
		t.Fatalf("Get pre: %v", err)
	}
	if _, _, _, err := s.Meta().DeleteTaggedAddressFilter(ctx, pre.ID); err != nil {
		t.Fatalf("Delete (no dismissal exists): %v", err)
	}
}

func testTaggedAddressHasFilterOrDismissal(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-has@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "has")

	// Neither filter nor dismissal yet.
	hasF, hasD, err := s.Meta().HasTaggedAddressFilterOrDismissal(ctx, p.ID, identityID, "unknown")
	if err != nil {
		t.Fatalf("Has(neither): %v", err)
	}
	if hasF || hasD {
		t.Fatalf("Has(neither) = (%v,%v), want (false,false)", hasF, hasD)
	}

	// Only dismissal present.
	if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "Skip",
	}); err != nil {
		t.Fatalf("Insert dismissal: %v", err)
	}
	hasF, hasD, err = s.Meta().HasTaggedAddressFilterOrDismissal(ctx, p.ID, identityID, "skip")
	if err != nil {
		t.Fatalf("Has(dismiss-only): %v", err)
	}
	if hasF || !hasD {
		t.Fatalf("Has(dismiss-only) = (%v,%v), want (false,true)", hasF, hasD)
	}
	// Case-insensitive match.
	hasF, hasD, err = s.Meta().HasTaggedAddressFilterOrDismissal(ctx, p.ID, identityID, "SKIP")
	if err != nil {
		t.Fatalf("Has(SKIP): %v", err)
	}
	if hasF || !hasD {
		t.Fatalf("Has(SKIP case-fold) = (%v,%v), want (false,true)", hasF, hasD)
	}

	// Only filter present (different suffix).
	if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "GitHub",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Code",
	}); err != nil {
		t.Fatalf("Insert filter: %v", err)
	}
	hasF, hasD, err = s.Meta().HasTaggedAddressFilterOrDismissal(ctx, p.ID, identityID, "github")
	if err != nil {
		t.Fatalf("Has(filter-only): %v", err)
	}
	if !hasF || hasD {
		t.Fatalf("Has(filter-only) = (%v,%v), want (true,false)", hasF, hasD)
	}

	// Both present: insert dismissal for the github suffix too. The
	// store happily holds both rows; only DeleteTaggedAddressFilter
	// cascades the dismissal (REQ-TAG-61).
	if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "github",
	}); err != nil {
		t.Fatalf("Insert second dismissal: %v", err)
	}
	hasF, hasD, err = s.Meta().HasTaggedAddressFilterOrDismissal(ctx, p.ID, identityID, "github")
	if err != nil {
		t.Fatalf("Has(both): %v", err)
	}
	if !hasF || !hasD {
		t.Fatalf("Has(both) = (%v,%v), want (true,true)", hasF, hasD)
	}
}

func testTaggedAddressIdentityCascade(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-cascade@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "cascadeI")

	if err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "a",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "A",
	}); err != nil {
		t.Fatalf("Insert filter: %v", err)
	}
	if err := s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "b",
	}); err != nil {
		t.Fatalf("Insert dismissal: %v", err)
	}

	// Drop the Identity row. Both child rows must vanish via ON DELETE
	// CASCADE per REQ-TAG-10.
	if err := s.Meta().DeleteJMAPIdentity(ctx, identityID); err != nil {
		t.Fatalf("DeleteJMAPIdentity: %v", err)
	}
	filters, err := s.Meta().ListTaggedAddressFiltersForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List filters: %v", err)
	}
	if len(filters) != 0 {
		t.Fatalf("filters survived Identity delete: %+v", filters)
	}
	dismissals, err := s.Meta().ListTaggedAddressDismissalsForPrincipal(ctx, p.ID)
	if err != nil {
		t.Fatalf("List dismissals: %v", err)
	}
	if len(dismissals) != 0 {
		t.Fatalf("dismissals survived Identity delete: %+v", dismissals)
	}
}

func testTaggedAddressInvalidInputs(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "tag-bad@example.com")
	identityID := insertTaggedIdentity(t, s, p.ID, "bad")

	// Empty base identity.
	err := s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID: p.ID,
		Suffix:      "anything",
		Action:      store.TaggedAddressActionLabel,
		LabelName:   "X",
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Insert(empty base) = %v, want ErrInvalidArgument", err)
	}

	// Empty label.
	err = s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Suffix:         "x",
		Action:         store.TaggedAddressActionLabel,
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Insert(empty label) = %v, want ErrInvalidArgument", err)
	}

	// Empty suffix.
	err = s.Meta().InsertTaggedAddressFilter(ctx, store.TaggedAddressFilter{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "X",
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Insert(empty suffix) = %v, want ErrInvalidArgument", err)
	}

	// Dismissal: empty suffix.
	err = s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID:    p.ID,
		BaseIdentityID: identityID,
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertDismissal(empty suffix) = %v, want ErrInvalidArgument", err)
	}

	// Dismissal: empty base identity.
	err = s.Meta().InsertTaggedAddressDismissal(ctx, store.TaggedAddressDismissal{
		PrincipalID: p.ID,
		Suffix:      "x",
	})
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("InsertDismissal(empty base) = %v, want ErrInvalidArgument", err)
	}
}
