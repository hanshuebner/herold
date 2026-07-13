package admin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// TestCLIList_CRUD_And_Members exercises the `herold list ...` tree
// end to end against an in-process protoadmin server (epic #183,
// REQ-MLIST-41): create, list, show, rename, set, member-add (by address
// and by known-principal email), members, member-remove, delete.
func TestCLIList_CRUD_And_Members(t *testing.T) {
	env := newCLITestEnv(t, nil)
	if err := env.store.Meta().InsertDomain(context.Background(), store.Domain{
		Name:    "lists.test",
		IsLocal: true,
	}); err != nil {
		t.Fatalf("InsertDomain: %v", err)
	}
	seedPrincipal(t, env, "internal-member@lists.test")

	out, _, err := env.run("list", "create", "announce@lists.test", "Announce", "--json")
	if err != nil {
		t.Fatalf("list create: %v", err)
	}
	if !strings.Contains(out, "announce@lists.test") {
		t.Fatalf("expected posting_address in create output: %s", out)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("unmarshal create output: %v", err)
	}
	listID := itoa(uint64(created["id"].(float64)))

	out, _, err = env.run("list", "list", "--json")
	if err != nil {
		t.Fatalf("list list: %v", err)
	}
	if !strings.Contains(out, "announce@lists.test") {
		t.Fatalf("expected list in listing: %s", out)
	}

	out, _, err = env.run("list", "show", listID, "--json")
	if err != nil {
		t.Fatalf("list show: %v", err)
	}
	if !strings.Contains(out, "Announce") {
		t.Fatalf("expected display_name in show output: %s", out)
	}

	out, _, err = env.run("list", "rename", listID, "ann@lists.test", "--json")
	if err != nil {
		t.Fatalf("list rename: %v", err)
	}
	if !strings.Contains(out, "ann@lists.test") {
		t.Fatalf("expected renamed posting_address: %s", out)
	}

	out, _, err = env.run("list", "set", listID, "--subject-tag", "ANN", "--json")
	if err != nil {
		t.Fatalf("list set: %v", err)
	}
	if !strings.Contains(out, "\"subject_tag\":\"ANN\"") && !strings.Contains(out, "ANN") {
		t.Fatalf("expected subject_tag in set output: %s", out)
	}

	// member-add: an external address and a known local principal (by
	// email, resolved client-side the same way `alias add` resolves
	// targets).
	out, _, err = env.run("list", "member-add", listID, "ext@example.net", "--json")
	if err != nil {
		t.Fatalf("member-add external: %v", err)
	}
	if !strings.Contains(out, "ext@example.net") {
		t.Fatalf("expected external_address in member-add output: %s", out)
	}
	out, _, err = env.run("list", "member-add", listID, "internal-member@lists.test", "--json")
	if err != nil {
		t.Fatalf("member-add principal: %v", err)
	}
	var addedPrincipalMember map[string]any
	if err := json.Unmarshal([]byte(out), &addedPrincipalMember); err != nil {
		t.Fatalf("unmarshal member-add principal output: %v", err)
	}
	if addedPrincipalMember["principal_id"] == nil {
		t.Fatalf("expected principal_id in member-add output: %s", out)
	}

	out, _, err = env.run("list", "members", listID, "--json")
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	var rosterPage struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &rosterPage); err != nil {
		t.Fatalf("unmarshal roster: %v", err)
	}
	if len(rosterPage.Items) != 2 {
		t.Fatalf("roster items = %d; want 2 (out=%s)", len(rosterPage.Items), out)
	}
	var extMemberID string
	for _, it := range rosterPage.Items {
		if it["external_address"] == "ext@example.net" {
			extMemberID = itoa(uint64(it["id"].(float64)))
		}
	}
	if extMemberID == "" {
		t.Fatalf("could not find external member id in roster: %s", out)
	}

	// Bulk export round trip: export to a file, then import it into the
	// same list (via --import) and confirm the previously exported
	// members are recognised (they will surface as "conflict", not
	// "invalid", proving the shape round-trips).
	exportPath := filepath.Join(t.TempDir(), "roster.json")
	_, _, err = env.run("list", "members", listID, "--export", "--out", exportPath)
	if err != nil {
		t.Fatalf("members --export: %v", err)
	}
	out, _, err = env.run("list", "members", listID, "--import", exportPath, "--json")
	if err != nil {
		t.Fatalf("members --import: %v", err)
	}
	var importResp struct {
		Added   int `json:"added"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(out), &importResp); err != nil {
		t.Fatalf("unmarshal import response: %v", err)
	}
	if importResp.Added != 0 || importResp.Skipped != 2 {
		t.Fatalf("re-importing the export = added=%d skipped=%d; want 0/2 (out=%s)",
			importResp.Added, importResp.Skipped, out)
	}

	if _, _, err := env.run("list", "member-remove", listID, extMemberID); err != nil {
		t.Fatalf("member-remove: %v", err)
	}
	out, _, err = env.run("list", "members", listID, "--json")
	if err != nil {
		t.Fatalf("list members after remove: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &rosterPage); err != nil {
		t.Fatalf("unmarshal roster after remove: %v", err)
	}
	if len(rosterPage.Items) != 1 {
		t.Fatalf("roster items after remove = %d; want 1 (out=%s)", len(rosterPage.Items), out)
	}

	if _, _, err := env.run("list", "delete", listID); err != nil {
		t.Fatalf("list delete: %v", err)
	}
	if _, _, err := env.run("list", "show", listID); err == nil {
		t.Fatalf("expected error showing a deleted list")
	}
}

// TestCLIList_Create_RequiresLocalDomain: the posting address's domain
// must already be a locally hosted domain (mirrors the alias CLI's
// unknown-target guard).
func TestCLIList_Create_RequiresLocalDomain(t *testing.T) {
	env := newCLITestEnv(t, nil)
	_, _, err := env.run("list", "create", "x@unknown.test", "X")
	if err == nil {
		t.Fatalf("expected error creating a list on an unknown domain")
	}
}
