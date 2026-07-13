package protoadmin_test

// mlist_archive_test.go — admin REST surface for the Stage 4 archive
// mailbox (epic #187, docs/design/server/requirements/28-mailing-lists.md
// REQ-MLIST-70..74): enabling/disabling the archive, retention config, and
// the archive read-grant lifecycle as the roster changes. Runs on both
// SQLite and (when HEROLD_PG_DSN is set) Postgres via openSubmissionBackends,
// mirroring every other mailing-list REST test in this package.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanshuebner/herold/internal/store"
)

// archiveACLPrincipals returns the set of principal IDs holding a
// mailbox grant on archiveMailboxID, via the store directly (this file's
// tests assert the store-level grant state produced by the REST calls;
// protocol-level enforcement of what those grants actually permit is
// covered by internal/protoimap and internal/protojmap archive tests).
func archiveACLPrincipals(t *testing.T, meta store.Metadata, archiveMailboxID uint64) map[uint64]store.ACLRights {
	t.Helper()
	rows, err := meta.GetMailboxACL(context.Background(), store.MailboxID(archiveMailboxID))
	if err != nil {
		t.Fatalf("GetMailboxACL: %v", err)
	}
	out := map[uint64]store.ACLRights{}
	for _, row := range rows {
		if row.PrincipalID != nil {
			out[uint64(*row.PrincipalID)] = row.Rights
		}
	}
	return out
}

// TestMailingLists_ArchiveEnableDisable_GrantsFollowRoster is the
// REQ-MLIST-70/72 end-to-end REST scenario: enabling the archive on a
// list with an existing roster retroactively grants every active
// internal-principal member (both `each` and `nomail`) read access,
// never an external-address member; roster changes (add, suspend,
// reactivate, remove) keep the grant set in sync; disabling the archive
// revokes every grant but leaves the mailbox itself (and its content) in
// place, and re-enabling reuses the same mailbox and re-grants current
// members.
func TestMailingLists_ArchiveEnableDisable_GrantsFollowRoster(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_ArchiveEnableDisable_GrantsFollowRoster(t, be) })
	}
}

func testMailingLists_ArchiveEnableDisable_GrantsFollowRoster(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-archive@example.test")
	e.insertLocalDomain(t, "archive.example")
	listID := idOf(t, e.createList(t, "list@archive.example", "Archive List"))

	// Existing roster BEFORE the archive is enabled: one `each` internal
	// member, one `nomail` internal member, one external-address member.
	eachPID := e.h.createPrincipal(e.adminKey, "each-member@example.test")
	res, buf := e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"principal_id": eachPID})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add each member: %d: %s", res.StatusCode, buf)
	}
	nomailPID := e.h.createPrincipal(e.adminKey, "nomail-member@example.test")
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"principal_id": nomailPID, "delivery_mode": "nomail"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add nomail member: %d: %s", res.StatusCode, buf)
	}
	var nomailMember map[string]any
	_ = json.Unmarshal(buf, &nomailMember)
	nomailMemberID := idOf(t, nomailMember)

	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"external_address": "ext@example.net"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add external member: %d: %s", res.StatusCode, buf)
	}

	// Before archive_enabled, the list has no archive.
	res, buf = e.h.doRequest("GET", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get list: %d: %s", res.StatusCode, buf)
	}
	var before map[string]any
	_ = json.Unmarshal(buf, &before)
	if before["archive_enabled"] == true {
		t.Fatalf("archive_enabled = true before any PATCH: %v", before)
	}

	// Enable the archive, with a retention bound in the same PATCH.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"archive_enabled": true, "archive_retention_days": float64(90), "archive_retention_max_messages": float64(5000)})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("enable archive: %d: %s", res.StatusCode, buf)
	}
	var enabled map[string]any
	if err := json.Unmarshal(buf, &enabled); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if enabled["archive_enabled"] != true {
		t.Fatalf("archive_enabled = %v after PATCH true; want true", enabled["archive_enabled"])
	}
	archiveName, _ := enabled["archive_mailbox_name"].(string)
	if archiveName != "Lists/list@archive.example" {
		t.Errorf("archive_mailbox_name = %q; want Lists/list@archive.example", archiveName)
	}
	if enabled["archive_retention_days"] != float64(90) {
		t.Errorf("archive_retention_days = %v; want 90", enabled["archive_retention_days"])
	}
	if enabled["archive_retention_max_messages"] != float64(5000) {
		t.Errorf("archive_retention_max_messages = %v; want 5000", enabled["archive_retention_max_messages"])
	}

	// Resolve the archive mailbox id directly from the store so this
	// test can inspect grant rows (the REST DTO exposes the name, not
	// the numeric mailbox id -- IMAP/JMAP address it by name/own-id, not
	// a cross-account admin id).
	groupP, err := e.h.Store().GetPrincipalByEmail(context.Background(), "list@archive.example")
	if err != nil {
		t.Fatalf("GetPrincipalByEmail(group): %v", err)
	}
	archiveMB, err := e.h.Store().GetMailboxByName(context.Background(), groupP.ID, archiveName)
	if err != nil {
		t.Fatalf("GetMailboxByName(archive): %v", err)
	}

	// Retroactive grant: both internal members (each + nomail) hold a
	// read-only grant; the external member holds none (it has no
	// principal to grant).
	grants := archiveACLPrincipals(t, e.h.Store(), uint64(archiveMB.ID))
	if len(grants) != 2 {
		t.Fatalf("archive grants after enable = %+v; want exactly 2 (each + nomail)", grants)
	}
	wantReadOnly := store.ACLRightLookup | store.ACLRightRead
	for _, pid := range []uint64{eachPID, nomailPID} {
		rights, ok := grants[pid]
		if !ok {
			t.Errorf("principal %d missing archive grant; grants=%+v", pid, grants)
			continue
		}
		if rights != wantReadOnly {
			t.Errorf("principal %d rights = %v; want read-only (lrs) = %v", pid, rights, wantReadOnly)
		}
		if rights&(store.ACLRightWrite|store.ACLRightInsert|store.ACLRightDeleteMessage|store.ACLRightExpunge|store.ACLRightAdmin) != 0 {
			t.Errorf("principal %d rights = %v carry a write/admin bit; want strictly read-only", pid, rights)
		}
	}

	// A newly-added member after the archive is already enabled gets the
	// grant immediately (not only on the next unrelated roster edit).
	latePID := e.h.createPrincipal(e.adminKey, "late-member@example.test")
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"principal_id": latePID})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add late member: %d: %s", res.StatusCode, buf)
	}
	grants = archiveACLPrincipals(t, e.h.Store(), uint64(archiveMB.ID))
	if _, ok := grants[latePID]; !ok {
		t.Fatalf("late-added member missing archive grant: %+v", grants)
	}

	// Suspending a member revokes the grant; reactivating restores it.
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, nomailMemberID), e.adminKey,
		map[string]any{"state": "suspended"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("suspend nomail member: %d: %s", res.StatusCode, buf)
	}
	grants = archiveACLPrincipals(t, e.h.Store(), uint64(archiveMB.ID))
	if _, ok := grants[nomailPID]; ok {
		t.Fatalf("suspended member still holds an archive grant: %+v", grants)
	}
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, nomailMemberID), e.adminKey,
		map[string]any{"state": "active"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reactivate nomail member: %d: %s", res.StatusCode, buf)
	}
	grants = archiveACLPrincipals(t, e.h.Store(), uint64(archiveMB.ID))
	if _, ok := grants[nomailPID]; !ok {
		t.Fatalf("reactivated member did not regain its archive grant: %+v", grants)
	}

	// Removing a member from the roster revokes the grant.
	res, buf = e.h.doRequest("DELETE", fmt.Sprintf("/api/v1/lists/%d/members/%d", listID, nomailMemberID), e.adminKey, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove nomail member: %d: %s", res.StatusCode, buf)
	}
	grants = archiveACLPrincipals(t, e.h.Store(), uint64(archiveMB.ID))
	if _, ok := grants[nomailPID]; ok {
		t.Fatalf("removed member still holds an archive grant: %+v", grants)
	}

	// Disabling the archive revokes every remaining grant, but the
	// mailbox itself survives (still resolvable by name/id).
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"archive_enabled": false})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("disable archive: %d: %s", res.StatusCode, buf)
	}
	var disabled map[string]any
	_ = json.Unmarshal(buf, &disabled)
	if disabled["archive_enabled"] == true {
		t.Fatalf("archive_enabled = true after PATCH false; want false/absent: %v", disabled)
	}
	grants = archiveACLPrincipals(t, e.h.Store(), uint64(archiveMB.ID))
	if len(grants) != 0 {
		t.Fatalf("grants after disable = %+v; want none", grants)
	}
	if _, err := e.h.Store().GetMailboxByName(context.Background(), groupP.ID, archiveName); err != nil {
		t.Fatalf("archive mailbox gone after disable: %v", err)
	}

	// Re-enabling reuses the SAME mailbox (no duplicate / conflict) and
	// re-grants the currently-active members (each + late; nomail was
	// removed above and must not reappear).
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"archive_enabled": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("re-enable archive: %d: %s", res.StatusCode, buf)
	}
	var reenabled map[string]any
	_ = json.Unmarshal(buf, &reenabled)
	if reenabled["archive_mailbox_name"] != archiveName {
		t.Fatalf("re-enable archive_mailbox_name = %v; want the SAME name %q (reused mailbox)", reenabled["archive_mailbox_name"], archiveName)
	}
	reusedMB, err := e.h.Store().GetMailboxByName(context.Background(), groupP.ID, archiveName)
	if err != nil {
		t.Fatalf("GetMailboxByName(archive) after re-enable: %v", err)
	}
	if reusedMB.ID != archiveMB.ID {
		t.Fatalf("re-enable created a NEW mailbox (id %d), want the original (id %d) reused", reusedMB.ID, archiveMB.ID)
	}
	grants = archiveACLPrincipals(t, e.h.Store(), uint64(archiveMB.ID))
	if len(grants) != 2 {
		t.Fatalf("grants after re-enable = %+v; want exactly 2 (each + late)", grants)
	}
	if _, ok := grants[eachPID]; !ok {
		t.Errorf("each member missing grant after re-enable: %+v", grants)
	}
	if _, ok := grants[latePID]; !ok {
		t.Errorf("late member missing grant after re-enable: %+v", grants)
	}
	if _, ok := grants[nomailPID]; ok {
		t.Errorf("removed nomail member reappeared in grants after re-enable: %+v", grants)
	}
}

// TestMailingLists_NomailExternalMember_RejectedAtCreate is the
// REQ-MLIST-04/71 boundary check at POST-time (the PATCH-time rejection
// is already covered by TestMailingLists_Roster_CRUD_And_BulkImportExport):
// `nomail` requires an internal principal, so external_address +
// delivery_mode=nomail on the initial POST is rejected, never silently
// downgraded to `each` or accepted with no delivery.
func TestMailingLists_NomailExternalMember_RejectedAtCreate(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_NomailExternalMember_RejectedAtCreate(t, be) })
	}
}

func testMailingLists_NomailExternalMember_RejectedAtCreate(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-nomail@example.test")
	e.insertLocalDomain(t, "nomail.example")
	listID := idOf(t, e.createList(t, "list@nomail.example", "Nomail"))

	res, buf := e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"external_address": "ext@example.net", "delivery_mode": "nomail"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST nomail+external_address: status=%d, want 400: %s", res.StatusCode, buf)
	}

	// The equivalent principal_id + nomail request succeeds.
	pid := e.h.createPrincipal(e.adminKey, "internal@example.test")
	res, buf = e.h.doRequest("POST", fmt.Sprintf("/api/v1/lists/%d/members", listID), e.adminKey,
		map[string]any{"principal_id": pid, "delivery_mode": "nomail"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST nomail+principal_id: status=%d, want 201: %s", res.StatusCode, buf)
	}
}

// TestMailingLists_ArchiveRetention_RejectsNegative exercises the
// REQ-MLIST-74 validation gate on both the create and patch surfaces.
func TestMailingLists_ArchiveRetention_RejectsNegative(t *testing.T) {
	for _, be := range openSubmissionBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) { testMailingLists_ArchiveRetention_RejectsNegative(t, be) })
	}
}

func testMailingLists_ArchiveRetention_RejectsNegative(t *testing.T, be submissionBackend) {
	e := newMlistTestEnv(t, be, "sa-archive-neg@example.test")
	e.insertLocalDomain(t, "neg.example")

	res, buf := e.h.doRequest("POST", "/api/v1/lists", e.adminKey, map[string]any{
		"posting_address":        "neg@neg.example",
		"display_name":           "Neg",
		"archive_retention_days": float64(-1),
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with negative retention: %d: %s", res.StatusCode, buf)
	}

	listID := idOf(t, e.createList(t, "ok@neg.example", "OK"))
	res, buf = e.h.doRequest("PATCH", fmt.Sprintf("/api/v1/lists/%d", listID), e.adminKey,
		map[string]any{"archive_retention_max_messages": float64(-5)})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch with negative retention: %d: %s", res.StatusCode, buf)
	}
}
