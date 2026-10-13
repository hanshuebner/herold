package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Apps compliance (REQ-APP-01..13, REQ-APP-45, migration 0118). Runs
// against both storesqlite and storepg via storetest.Run.

func newTestApp(key string) store.App {
	return store.App{
		AppKey:                key,
		Origin:                "https://app.example.test",
		PendingManifestJSON:   []byte(`{"herold_app":1,"app_key":"` + key + `"}`),
		PendingManifestDigest: "digest-1",
		SecretSealed:          []byte("v1:sealed-secret"),
	}
}

func testApps_CRUD(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	meta := s.Meta()

	a, err := meta.InsertApp(ctx, newTestApp("test.crud.app"))
	if err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	if a.ID == 0 || a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		t.Fatalf("InsertApp did not assign ID/timestamps: %+v", a)
	}
	if a.State != store.AppStatePendingApproval {
		t.Fatalf("new app state = %q, want pending_approval", a.State)
	}
	if a.ManifestJSON != nil || a.ManifestDigest != "" {
		t.Fatalf("new app carries an approved manifest: %+v", a)
	}
	if string(a.PendingManifestJSON) != `{"herold_app":1,"app_key":"test.crud.app"}` {
		t.Fatalf("pending manifest = %q", a.PendingManifestJSON)
	}
	if a.AllPrincipals {
		t.Fatalf("new app has all_principals set")
	}
	if !a.LastInvokedAt.IsZero() {
		t.Fatalf("new app has last_invoked_at %v", a.LastInvokedAt)
	}

	if _, err := meta.InsertApp(ctx, newTestApp("test.crud.app")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate app_key: err = %v, want ErrConflict", err)
	}

	got, err := meta.GetApp(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.AppKey != a.AppKey || string(got.SecretSealed) != "v1:sealed-secret" {
		t.Fatalf("GetApp round trip: %+v", got)
	}
	if _, err := meta.GetApp(ctx, a.ID+1000); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetApp unknown: err = %v, want ErrNotFound", err)
	}

	got.AllPrincipals = true
	got.State = store.AppStateDisabled
	got.SecretSealed = []byte("v1:rotated")
	got.Origin = "https://other.example.test"
	upd, err := meta.UpdateApp(ctx, got)
	if err != nil {
		t.Fatalf("UpdateApp: %v", err)
	}
	if !upd.AllPrincipals || upd.State != store.AppStateDisabled ||
		string(upd.SecretSealed) != "v1:rotated" || upd.Origin != "https://other.example.test" {
		t.Fatalf("UpdateApp did not persist: %+v", upd)
	}
	got.ID = a.ID + 1000
	if _, err := meta.UpdateApp(ctx, got); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateApp unknown: err = %v, want ErrNotFound", err)
	}

	b, err := meta.InsertApp(ctx, newTestApp("test.crud.second"))
	if err != nil {
		t.Fatalf("InsertApp second: %v", err)
	}
	all, err := meta.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(all) != 2 || all[0].ID != a.ID || all[1].ID != b.ID {
		t.Fatalf("ListApps = %+v, want [%d %d]", all, a.ID, b.ID)
	}

	at := time.Date(2026, 10, 13, 8, 12, 0, 0, time.UTC)
	if err := meta.RecordAppInvoked(ctx, b.ID, at); err != nil {
		t.Fatalf("RecordAppInvoked: %v", err)
	}
	got, _ = meta.GetApp(ctx, b.ID)
	if !got.LastInvokedAt.Equal(at) {
		t.Fatalf("last_invoked_at = %v, want %v", got.LastInvokedAt, at)
	}
	if err := meta.RecordAppInvoked(ctx, b.ID+1000, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RecordAppInvoked unknown: err = %v", err)
	}

	if err := meta.DeleteApp(ctx, a.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if err := meta.DeleteApp(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("DeleteApp twice: err = %v, want ErrNotFound", err)
	}
}

func testApps_ApproveManifest(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	meta := s.Meta()

	a, err := meta.InsertApp(ctx, newTestApp("test.approve.app"))
	if err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	if _, err := meta.ApproveAppManifest(ctx, a.ID, "wrong-digest"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("approve wrong digest: err = %v, want ErrConflict", err)
	}
	cur, _ := meta.GetApp(ctx, a.ID)
	if cur.State != store.AppStatePendingApproval || cur.ManifestJSON != nil {
		t.Fatalf("rejected approval changed the row: %+v", cur)
	}

	ok, err := meta.ApproveAppManifest(ctx, a.ID, "digest-1")
	if err != nil {
		t.Fatalf("ApproveAppManifest: %v", err)
	}
	if ok.State != store.AppStateActive || ok.ManifestDigest != "digest-1" ||
		string(ok.ManifestJSON) != `{"herold_app":1,"app_key":"test.approve.app"}` {
		t.Fatalf("approved row: %+v", ok)
	}
	if ok.PendingManifestJSON != nil || ok.PendingManifestDigest != "" {
		t.Fatalf("approval left a pending manifest: %+v", ok)
	}
	if _, err := meta.ApproveAppManifest(ctx, a.ID, "digest-1"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("approve with nothing pending: err = %v, want ErrConflict", err)
	}

	// A disabled app approving a manifest update stays disabled.
	ok.State = store.AppStateDisabled
	ok.PendingManifestJSON = []byte(`{"v":2}`)
	ok.PendingManifestDigest = "digest-2"
	if _, err := meta.UpdateApp(ctx, ok); err != nil {
		t.Fatalf("UpdateApp: %v", err)
	}
	upd, err := meta.ApproveAppManifest(ctx, a.ID, "digest-2")
	if err != nil {
		t.Fatalf("ApproveAppManifest update: %v", err)
	}
	if upd.State != store.AppStateDisabled || upd.ManifestDigest != "digest-2" {
		t.Fatalf("approved update: %+v", upd)
	}
	if _, err := meta.ApproveAppManifest(ctx, a.ID+1000, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approve unknown: err = %v, want ErrNotFound", err)
	}
}

func testApps_PrincipalsAndOffering(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	meta := s.Meta()

	alice := mustInsertPrincipal(t, s, "apps-alice@example.test").ID
	bob := mustInsertPrincipal(t, s, "apps-bob@example.test").ID

	a, err := meta.InsertApp(ctx, newTestApp("test.offer.app"))
	if err != nil {
		t.Fatalf("InsertApp: %v", err)
	}
	if err := meta.SetAppPrincipals(ctx, a.ID, []store.PrincipalID{bob, alice}); err != nil {
		t.Fatalf("SetAppPrincipals: %v", err)
	}
	got, err := meta.ListAppPrincipals(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListAppPrincipals: %v", err)
	}
	lo, hi := alice, bob
	if lo > hi {
		lo, hi = hi, lo
	}
	if len(got) != 2 || got[0] != lo || got[1] != hi {
		t.Fatalf("ListAppPrincipals = %v, want [%d %d]", got, lo, hi)
	}

	// Pending apps are offered to nobody.
	if apps, _ := meta.ListAppsForPrincipal(ctx, alice); len(apps) != 0 {
		t.Fatalf("pending app offered: %+v", apps)
	}
	if _, err := meta.ApproveAppManifest(ctx, a.ID, "digest-1"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if apps, _ := meta.ListAppsForPrincipal(ctx, alice); len(apps) != 1 || apps[0].ID != a.ID {
		t.Fatalf("active enabled app not offered to alice: %+v", apps)
	}

	if err := meta.SetAppPrincipals(ctx, a.ID, []store.PrincipalID{bob}); err != nil {
		t.Fatalf("SetAppPrincipals replace: %v", err)
	}
	if apps, _ := meta.ListAppsForPrincipal(ctx, alice); len(apps) != 0 {
		t.Fatalf("app still offered after removing alice: %+v", apps)
	}

	// Unknown principal: rejected, previous set untouched.
	if err := meta.SetAppPrincipals(ctx, a.ID, []store.PrincipalID{alice, 999999}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetAppPrincipals unknown principal: err = %v, want ErrNotFound", err)
	}
	if got, _ := meta.ListAppPrincipals(ctx, a.ID); len(got) != 1 || got[0] != bob {
		t.Fatalf("failed SetAppPrincipals changed the set: %v", got)
	}
	if err := meta.SetAppPrincipals(ctx, a.ID+1000, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetAppPrincipals unknown app: err = %v, want ErrNotFound", err)
	}

	cur, _ := meta.GetApp(ctx, a.ID)
	cur.AllPrincipals = true
	if _, err := meta.UpdateApp(ctx, cur); err != nil {
		t.Fatalf("UpdateApp all_principals: %v", err)
	}
	if apps, _ := meta.ListAppsForPrincipal(ctx, alice); len(apps) != 1 {
		t.Fatalf("all_principals app not offered to alice: %+v", apps)
	}
	cur.State = store.AppStateDisabled
	if _, err := meta.UpdateApp(ctx, cur); err != nil {
		t.Fatalf("UpdateApp disable: %v", err)
	}
	if apps, _ := meta.ListAppsForPrincipal(ctx, bob); len(apps) != 0 {
		t.Fatalf("disabled app offered: %+v", apps)
	}

	// Deleting a principal drops its membership; deleting the app drops
	// the rest.
	if err := meta.DeletePrincipal(ctx, bob); err != nil {
		t.Fatalf("DeletePrincipal: %v", err)
	}
	if got, _ := meta.ListAppPrincipals(ctx, a.ID); len(got) != 0 {
		t.Fatalf("membership survived principal delete: %v", got)
	}
	if err := meta.SetAppPrincipals(ctx, a.ID, []store.PrincipalID{alice}); err != nil {
		t.Fatalf("SetAppPrincipals: %v", err)
	}
	if err := meta.DeleteApp(ctx, a.ID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if got, _ := meta.ListAppPrincipals(ctx, a.ID); len(got) != 0 {
		t.Fatalf("membership survived app delete: %v", got)
	}
}
