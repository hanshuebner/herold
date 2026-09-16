package mailbox_test

// Category label mailbox wire surface (issue #406): the store-level
// adoption/creation mechanics are covered by
// internal/store/storetest/storetest_categorylabelmailbox.go; this file
// exercises the JMAP-visible result -- after a (simulated) first
// categorisation, Mailbox/get reports each derived category as a
// pinned, densely-prioritised Mailbox, and a user-set disposition
// survives a later recompute that renames or drops the category from
// the derived set.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/mail/categorysettings"
	"github.com/hanshuebner/herold/internal/storepg"
)

// TestMailbox_CategoryLabel_Acceptance runs the #406 acceptance scenario
// against the default SQLite-backed fixture.
func TestMailbox_CategoryLabel_Acceptance(t *testing.T) {
	runCategoryLabelAcceptance(t, setupFixture(t))
}

// TestMailbox_CategoryLabel_Acceptance_Postgres is the same scenario
// against a Postgres-backed store, skipping when HEROLD_PG_DSN is unset
// or unreachable.
func TestMailbox_CategoryLabel_Acceptance_Postgres(t *testing.T) {
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, nil)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runCategoryLabelAcceptance(t, setupFixtureWithStore(t, st))
}

func runCategoryLabelAcceptance(t *testing.T, f *fixture) {
	ctx := context.Background()

	// CategorySettings/* is not wired by setupFixture (mailbox-only
	// fixture); register it on the same registry so this test can read
	// derivedCategories back over JMAP too.
	categorysettings.Register(f.jmapServ.Registry(), f.srv.Store, nil,
		categorise.NewJobRegistry(24*time.Hour, 256), f.srv.Logger, f.srv.Clock)

	// Simulate the categoriser's first successful classification: the
	// derived category set is (re)computed for the principal via the
	// same store.Metadata.SetDerivedCategories call the categoriser
	// makes after classifying a message (internal/categorise).
	seed, err := f.srv.Store.Meta().GetCategorisationConfig(ctx, f.pid)
	if err != nil {
		t.Fatalf("GetCategorisationConfig: %v", err)
	}
	cats := []string{"primary", "social", "promotions", "updates", "forums"}
	ok, err := f.srv.Store.Meta().SetDerivedCategories(ctx, f.pid, cats, seed.DerivedCategoriesEpoch)
	if err != nil {
		t.Fatalf("SetDerivedCategories: %v", err)
	}
	if !ok {
		t.Fatalf("SetDerivedCategories: expected hit, got miss")
	}

	// -- CategorySettings/get still lists derivedCategories. --
	_, csRaw := f.invoke(t, "CategorySettings/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
	}, protojmap.CapabilityCore, protojmap.CapabilityJMAPCategorise)
	var csResp struct {
		List []struct {
			DerivedCategories []string `json:"derivedCategories"`
		} `json:"list"`
	}
	if err := json.Unmarshal(csRaw, &csResp); err != nil {
		t.Fatalf("unmarshal CategorySettings/get: %v", err)
	}
	if len(csResp.List) != 1 || len(csResp.List[0].DerivedCategories) != len(cats) {
		t.Fatalf("CategorySettings/get derivedCategories = %+v, want %v (raw=%s)", csResp.List, cats, csRaw)
	}

	// -- each derived category is backed by a Mailbox of the same
	// name, pinned, with a dense priority matching cats order. --
	ids := make(map[string]string, len(cats))
	for i, name := range cats {
		mb, err := f.srv.Store.Meta().GetMailboxByName(ctx, f.pid, name)
		if err != nil {
			t.Fatalf("GetMailboxByName(%q): %v", name, err)
		}
		idStr := fmt.Sprintf("%d", mb.ID)
		ids[name] = idStr

		_, getRaw := f.invoke(t, "Mailbox/get", map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(f.pid),
			"ids":       []string{idStr},
		})
		var getResp struct {
			List []map[string]any `json:"list"`
		}
		if err := json.Unmarshal(getRaw, &getResp); err != nil {
			t.Fatalf("unmarshal Mailbox/get(%q): %v", name, err)
		}
		if len(getResp.List) != 1 {
			t.Fatalf("Mailbox/get(%q) returned %d mailboxes, want 1 (raw=%s)", name, len(getResp.List), getRaw)
		}
		obj := getResp.List[0]
		if got := obj["name"]; got != name {
			t.Errorf("Mailbox/get(%q) name = %v, want %q", name, got, name)
		}
		if got := obj["disposition"]; got != "pinned" {
			t.Errorf("Mailbox/get(%q) disposition = %v, want pinned (raw=%s)", name, got, getRaw)
		}
		if got, ok := obj["priority"].(float64); !ok || int(got) != i {
			t.Errorf("Mailbox/get(%q) priority = %v, want %d (raw=%s)", name, obj["priority"], i, getRaw)
		}
	}

	// -- a user-set disposition on one of the labels survives a
	// recompute that renames the category set (some categories dropped,
	// one still present). --
	updatesID := ids["updates"]
	_, updRaw := f.invoke(t, "Mailbox/set", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"update":    map[string]any{updatesID: map[string]any{"disposition": "bundled"}},
	})
	var updResp struct {
		NotUpdated map[string]map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(updRaw, &updResp); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if len(updResp.NotUpdated) != 0 {
		t.Fatalf("user disposition update rejected: %+v (raw=%s)", updResp.NotUpdated, updRaw)
	}

	cfg, err := f.srv.Store.Meta().GetCategorisationConfig(ctx, f.pid)
	if err != nil {
		t.Fatalf("GetCategorisationConfig (re-read): %v", err)
	}
	// A later recompute renames the set (a "primary"/"updates"-only
	// remainder standing in for a renamed vocabulary) and drops
	// "social"/"promotions"/"forums" entirely.
	renamed := []string{"primary", "updates"}
	if _, err := f.srv.Store.Meta().SetDerivedCategories(ctx, f.pid, renamed, cfg.DerivedCategoriesEpoch); err != nil {
		t.Fatalf("SetDerivedCategories (recompute): %v", err)
	}

	_, getRaw2 := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{updatesID},
	})
	var getResp2 struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(getRaw2, &getResp2); err != nil {
		t.Fatalf("unmarshal post-recompute get: %v", err)
	}
	if len(getResp2.List) != 1 {
		t.Fatalf("post-recompute Mailbox/get returned %d, want 1 (raw=%s)", len(getResp2.List), getRaw2)
	}
	if got := getResp2.List[0]["disposition"]; got != "bundled" {
		t.Errorf("updates disposition after recompute = %v, want the user-set bundled (raw=%s)", got, getRaw2)
	}

	// The dropped categories' labels and dispositions are untouched --
	// "social" is still pinned exactly as the first recompute left it.
	socialID := ids["social"]
	_, getRaw3 := f.invoke(t, "Mailbox/get", map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(f.pid),
		"ids":       []string{socialID},
	})
	var getResp3 struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(getRaw3, &getResp3); err != nil {
		t.Fatalf("unmarshal social get: %v", err)
	}
	if len(getResp3.List) != 1 || getResp3.List[0]["disposition"] != "pinned" {
		t.Errorf("social after recompute = %+v, want still pinned (raw=%s)", getResp3.List, getRaw3)
	}
}
