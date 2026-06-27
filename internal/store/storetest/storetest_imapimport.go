package storetest

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testIMAPImport_CreateGetRoundtrip verifies that CreateIMAPImportAccount
// followed by GetIMAPImportAccount returns a byte-identical record.
func testIMAPImport_CreateGetRoundtrip(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-cg@example.com")

	floorDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	create := store.IMAPImportAccountCreate{
		PrincipalID:       p.ID,
		AccountName:       "My Gmail",
		Host:              "imap.gmail.com",
		Port:              993,
		TLSMode:           store.IMAPImportTLSModeImplicit,
		Username:          "user@gmail.com",
		AuthMethod:        store.IMAPImportAuthMethodAppPassword,
		BackfillFloorDate: &floorDate,
		CredentialCT:      []byte("v1:sealed-app-password"),
		State:             store.IMAPImportAccountStateEnabled,
		DeletePropagates:  true,
	}
	acc, err := s.Meta().CreateIMAPImportAccount(ctx, create)
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	if acc.ID == "" {
		t.Fatal("CreateIMAPImportAccount: returned empty ID")
	}
	if acc.PrincipalID != p.ID {
		t.Errorf("PrincipalID = %d; want %d", acc.PrincipalID, p.ID)
	}
	if acc.AccountName != "My Gmail" {
		t.Errorf("AccountName = %q; want My Gmail", acc.AccountName)
	}
	if acc.Host != "imap.gmail.com" {
		t.Errorf("Host = %q; want imap.gmail.com", acc.Host)
	}
	if acc.Port != 993 {
		t.Errorf("Port = %d; want 993", acc.Port)
	}
	if acc.TLSMode != store.IMAPImportTLSModeImplicit {
		t.Errorf("TLSMode = %q; want implicit", acc.TLSMode)
	}
	if acc.AuthMethod != store.IMAPImportAuthMethodAppPassword {
		t.Errorf("AuthMethod = %q; want app_password", acc.AuthMethod)
	}
	if !bytes.Equal(acc.CredentialCT, create.CredentialCT) {
		t.Errorf("CredentialCT mismatch")
	}
	if acc.BackfillFloorDate == nil || !acc.BackfillFloorDate.Equal(floorDate) {
		t.Errorf("BackfillFloorDate = %v; want %v", acc.BackfillFloorDate, floorDate)
	}
	if acc.State != store.IMAPImportAccountStateEnabled {
		t.Errorf("State = %q; want enabled", acc.State)
	}
	if !acc.DeletePropagates {
		t.Error("DeletePropagates = false; want true")
	}
	if acc.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// Round-trip via Get.
	got, err := s.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if got.ID != acc.ID {
		t.Errorf("Get ID = %q; want %q", got.ID, acc.ID)
	}
	if !bytes.Equal(got.CredentialCT, create.CredentialCT) {
		t.Error("Get CredentialCT mismatch")
	}
}

// testIMAPImport_ListByPrincipal verifies that accounts are listed in
// ascending created_at order and scoped to the owning principal.
func testIMAPImport_ListByPrincipal(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p1 := mustInsertPrincipal(t, s, "imap-list1@example.com")
	p2 := mustInsertPrincipal(t, s, "imap-list2@example.com")

	make := func(pid store.PrincipalID, name string) store.IMAPImportAccount {
		t.Helper()
		acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
			PrincipalID:      pid,
			AccountName:      name,
			Host:             "imap.example.com",
			Port:             993,
			TLSMode:          store.IMAPImportTLSModeImplicit,
			Username:         "user",
			AuthMethod:       store.IMAPImportAuthMethodPassword,
			CredentialCT:     []byte("v1:pw"),
			State:            store.IMAPImportAccountStateEnabled,
			DeletePropagates: true,
		})
		if err != nil {
			t.Fatalf("CreateIMAPImportAccount %s: %v", name, err)
		}
		return acc
	}

	a1 := make(p1.ID, "Account A")
	a2 := make(p1.ID, "Account B")
	_ = make(p2.ID, "Account C") // different principal, must not appear

	list, err := s.Meta().ListIMAPImportAccountsByPrincipal(ctx, p1.ID)
	if err != nil {
		t.Fatalf("ListIMAPImportAccountsByPrincipal: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d accounts; want 2", len(list))
	}
	if list[0].ID != a1.ID {
		t.Errorf("list[0].ID = %q; want %q", list[0].ID, a1.ID)
	}
	if list[1].ID != a2.ID {
		t.Errorf("list[1].ID = %q; want %q", list[1].ID, a2.ID)
	}
}

// testIMAPImport_ListEnabled verifies that ListEnabledIMAPImportAccounts
// returns accounts that need a running worker — "enabled" and (for cutover
// resume, REQ-IMAP-IMP-94) "migrating" — and excludes "disabled", "errored",
// and the terminal "migrated".
func testIMAPImport_ListEnabled(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-enabled@example.com")

	make := func(name string, state store.IMAPImportAccountState) store.IMAPImportAccount {
		t.Helper()
		acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
			PrincipalID:      p.ID,
			AccountName:      name,
			Host:             "imap.example.com",
			Port:             993,
			TLSMode:          store.IMAPImportTLSModeImplicit,
			Username:         "user",
			AuthMethod:       store.IMAPImportAuthMethodPassword,
			CredentialCT:     []byte("v1:pw"),
			State:            state,
			DeletePropagates: true,
		})
		if err != nil {
			t.Fatalf("CreateIMAPImportAccount %s: %v", name, err)
		}
		return acc
	}

	enabled := make("Enabled", store.IMAPImportAccountStateEnabled)
	migrating := make("Migrating", store.IMAPImportAccountStateMigrating)
	_ = make("Disabled", store.IMAPImportAccountStateDisabled)
	_ = make("Errored", store.IMAPImportAccountStateErrored)
	migrated := make("Migrated", store.IMAPImportAccountStateMigrated)

	list, err := s.Meta().ListEnabledIMAPImportAccounts(ctx)
	if err != nil {
		t.Fatalf("ListEnabledIMAPImportAccounts: %v", err)
	}
	// There may be accounts from other subtests, but we can check ours appears.
	var foundEnabled, foundMigrating bool
	for _, a := range list {
		switch a.ID {
		case enabled.ID:
			foundEnabled = true
		case migrating.ID:
			foundMigrating = true
		case migrated.ID:
			t.Errorf("migrated account %q must not be returned by ListEnabledIMAPImportAccounts", a.ID)
		}
		switch a.State {
		case store.IMAPImportAccountStateEnabled, store.IMAPImportAccountStateMigrating:
			// expected
		default:
			t.Errorf("account %q has state %q; want enabled or migrating", a.ID, a.State)
		}
	}
	if !foundEnabled {
		t.Errorf("enabled account %q not found in ListEnabledIMAPImportAccounts", enabled.ID)
	}
	if !foundMigrating {
		t.Errorf("migrating account %q not found in ListEnabledIMAPImportAccounts", migrating.ID)
	}
}

// testIMAPImport_Update verifies that UpdateIMAPImportAccount replaces
// mutable fields and re-validates the credential.
func testIMAPImport_Update(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-update@example.com")

	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      "Before",
		Host:             "imap.old.com",
		Port:             143,
		TLSMode:          store.IMAPImportTLSModeSTARTTLS,
		Username:         "olduser",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     []byte("v1:old-pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: false,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	updated, err := s.Meta().UpdateIMAPImportAccount(ctx, store.IMAPImportAccountUpdate{
		ID:               acc.ID,
		PrincipalID:      p.ID,
		AccountName:      "After",
		Host:             "imap.new.com",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "newuser",
		AuthMethod:       store.IMAPImportAuthMethodAppPassword,
		CredentialCT:     []byte("v1:new-pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("UpdateIMAPImportAccount: %v", err)
	}
	if updated.AccountName != "After" {
		t.Errorf("AccountName = %q; want After", updated.AccountName)
	}
	if updated.Host != "imap.new.com" {
		t.Errorf("Host = %q; want imap.new.com", updated.Host)
	}
	if updated.Port != 993 {
		t.Errorf("Port = %d; want 993", updated.Port)
	}
	if updated.TLSMode != store.IMAPImportTLSModeImplicit {
		t.Errorf("TLSMode = %q; want implicit", updated.TLSMode)
	}
	if !bytes.Equal(updated.CredentialCT, []byte("v1:new-pw")) {
		t.Error("CredentialCT not updated")
	}
	if !updated.DeletePropagates {
		t.Error("DeletePropagates = false; want true")
	}
}

// testIMAPImport_Delete verifies that DeleteIMAPImportAccount removes
// the account row and cascades to child rows.
func testIMAPImport_Delete(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-del@example.com")

	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      "ToDelete",
		Host:             "imap.example.com",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "user",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     []byte("v1:pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	// Insert child rows to verify cascade.
	if err := s.Meta().SetIMAPImportFolderMap(ctx, acc.ID, []store.IMAPImportFolderMapEntry{
		{AccountID: acc.ID, UpstreamFolder: "INBOX", HeroldMailboxName: "INBOX"},
	}); err != nil {
		t.Fatalf("SetIMAPImportFolderMap: %v", err)
	}
	if err := s.Meta().UpsertIMAPImportFolderCursor(ctx, store.IMAPImportFolderCursor{
		AccountID:      acc.ID,
		UpstreamFolder: "INBOX",
		UIDValidity:    42,
		UIDNext:        100,
	}); err != nil {
		t.Fatalf("UpsertIMAPImportFolderCursor: %v", err)
	}
	if err := s.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
		AccountID:       acc.ID,
		UpstreamFolder:  "INBOX",
		UpstreamUID:     1,
		HeroldMessageID: 999,
		HeroldMailboxID: 1,
		LastSyncedFlags: store.IMAPImportFlagSeen,
	}); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState: %v", err)
	}

	// Delete the account.
	if err := s.Meta().DeleteIMAPImportAccount(ctx, p.ID, acc.ID); err != nil {
		t.Fatalf("DeleteIMAPImportAccount: %v", err)
	}

	// Account row is gone.
	_, err = s.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetIMAPImportAccount after delete: want ErrNotFound, got %v", err)
	}

	// Folder map is gone (cascade).
	fm, err := s.Meta().GetIMAPImportFolderMap(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportFolderMap after delete: %v", err)
	}
	if len(fm) != 0 {
		t.Errorf("folder_map rows remain after cascade: %d", len(fm))
	}

	// Folder cursor is gone.
	_, found, err := s.Meta().GetIMAPImportFolderCursor(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor after delete: %v", err)
	}
	if found {
		t.Error("folder_cursor row remains after cascade")
	}

	// Message state is gone.
	_, found, err = s.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", 1)
	if err != nil {
		t.Fatalf("GetIMAPImportMessageState after delete: %v", err)
	}
	if found {
		t.Error("message_state row remains after cascade")
	}

	// Second delete returns ErrNotFound.
	if err := s.Meta().DeleteIMAPImportAccount(ctx, p.ID, acc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
}

// testIMAPImport_IdentityScope verifies the per-identity re-scope (decision
// 10, REQ-IMAP-IMP-01/02): an account carries its owning IdentityID, the
// store enforces 0-or-1 account per identity, and deleting the owning
// Identity cascades to the import config.
func testIMAPImport_IdentityScope(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-ident@example.com")

	identID := "imap-ident-1"
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: identID, PrincipalID: p.ID, Email: "alice@external.test", MayDelete: true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity: %v", err)
	}

	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		IdentityID:       identID,
		PrincipalID:      p.ID,
		AccountName:      "External",
		Host:             "imap.external.test",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "alice",
		AuthMethod:       store.IMAPImportAuthMethodAppPassword,
		CredentialCT:     []byte("v1:pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	if acc.IdentityID != identID {
		t.Errorf("IdentityID = %q; want %q", acc.IdentityID, identID)
	}

	// Round-trip carries identity_id.
	got, err := s.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if got.IdentityID != identID {
		t.Errorf("Get IdentityID = %q; want %q", got.IdentityID, identID)
	}

	// 0-or-1 per identity: a second account on the same identity conflicts.
	_, err = s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		IdentityID:       identID,
		PrincipalID:      p.ID,
		AccountName:      "Second",
		Host:             "imap.external.test",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "alice",
		AuthMethod:       store.IMAPImportAuthMethodAppPassword,
		CredentialCT:     []byte("v1:pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("second account on same identity: want ErrConflict, got %v", err)
	}

	// A different identity on the same principal is allowed.
	identID2 := "imap-ident-2"
	if err := s.Meta().InsertJMAPIdentity(ctx, store.JMAPIdentity{
		ID: identID2, PrincipalID: p.ID, Email: "bob@external.test", MayDelete: true,
	}); err != nil {
		t.Fatalf("InsertJMAPIdentity 2: %v", err)
	}
	acc2, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		IdentityID:       identID2,
		PrincipalID:      p.ID,
		AccountName:      "Other",
		Host:             "imap.external.test",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "bob",
		AuthMethod:       store.IMAPImportAuthMethodAppPassword,
		CredentialCT:     []byte("v1:pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount (second identity): %v", err)
	}

	// Deleting the owning Identity cascades to its import config.
	if err := s.Meta().DeleteJMAPIdentity(ctx, identID); err != nil {
		t.Fatalf("DeleteJMAPIdentity: %v", err)
	}
	if _, err := s.Meta().GetIMAPImportAccount(ctx, acc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after identity delete: want account gone (ErrNotFound), got %v", err)
	}
	// The other identity's account is untouched.
	if _, err := s.Meta().GetIMAPImportAccount(ctx, acc2.ID); err != nil {
		t.Errorf("unrelated account removed by cascade: %v", err)
	}
}

// blobHashes returns the distinct blob hashes referenced by the principal's
// messages — a proxy for blob refcounts (refcounted per messages row).
func blobHashes(t *testing.T, s store.Store, pid store.PrincipalID) []string {
	t.Helper()
	hs, err := s.Meta().ListPrincipalBlobHashes(ctxT(t), pid)
	if err != nil {
		t.Fatalf("ListPrincipalBlobHashes: %v", err)
	}
	return hs
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// testIMAPImport_ProvenanceAndRemoval exercises the provenance-label
// removal flow (REQ-IMAP-IMP-100..105): keep leaves everything, purge of a
// single-source message destroys it (decrementing the blob refcount), and
// purge of a message shared with a second import account removes only this
// account's provenance label and keeps the message.
func testIMAPImport_ProvenanceAndRemoval(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-prov@example.com")
	inbox := mustInsertMailbox(t, s, p.ID, "INBOX")

	// insertMsg inserts a message into INBOX and returns its id and blob hash.
	insertMsg := func(body string) (store.MessageID, string) {
		t.Helper()
		ref := putBlob(t, s, body)
		if _, _, err := s.Meta().InsertMessage(ctx,
			store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size},
			[]store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		m, err := s.Meta().GetMessageByBlobHash(ctx, p.ID, ref.Hash)
		if err != nil {
			t.Fatalf("GetMessageByBlobHash: %v", err)
		}
		return m.ID, ref.Hash
	}
	addLabel := func(msgID store.MessageID, mbID store.MailboxID) {
		t.Helper()
		if _, _, err := s.Meta().AddMessageToMailbox(ctx, msgID, mbID); err != nil {
			t.Fatalf("AddMessageToMailbox: %v", err)
		}
	}
	recordState := func(accID string, msgID store.MessageID, uid uint32) {
		t.Helper()
		if err := s.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
			AccountID: accID, UpstreamFolder: "INBOX", UpstreamUID: uid,
			HeroldMessageID: msgID, HeroldMailboxID: inbox.ID,
		}); err != nil {
			t.Fatalf("UpsertIMAPImportMessageState: %v", err)
		}
	}
	mkAccount := func(name string, provName string) store.IMAPImportAccount {
		t.Helper()
		acc := mustCreateIMAPImportAccount(t, s, p.ID, name)
		prov := mustInsertMailbox(t, s, p.ID, provName)
		if err := s.Meta().SetIMAPImportProvenanceMailbox(ctx, acc.ID, prov.ID); err != nil {
			t.Fatalf("SetIMAPImportProvenanceMailbox: %v", err)
		}
		acc.ProvenanceMailboxID = prov.ID
		return acc
	}
	memberCount := func(msgID store.MessageID) int {
		t.Helper()
		m, err := s.Meta().GetMessage(ctx, msgID)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		return len(m.Mailboxes)
	}
	isMember := func(msgID store.MessageID, mbID store.MailboxID) bool {
		t.Helper()
		m, err := s.Meta().GetMessage(ctx, msgID)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		for _, mm := range m.Mailboxes {
			if mm.MailboxID == mbID {
				return true
			}
		}
		return false
	}

	// -- KEEP: removal with delete_imported_mail=false leaves everything. --
	keepAcc := mkAccount("Keep", "Label Keep")
	keepMsg, _ := insertMsg("keep-body-unique")
	addLabel(keepMsg, keepAcc.ProvenanceMailboxID)
	recordState(keepAcc.ID, keepMsg, 1)

	res, err := store.RemoveIMAPImportAccount(ctx, s, p.ID, keepAcc.ID, false)
	if err != nil {
		t.Fatalf("RemoveIMAPImportAccount (keep): %v", err)
	}
	if res.MessagesDestroyed != 0 || res.LabelsDetached != 0 {
		t.Errorf("keep result = %+v; want zero", res)
	}
	if _, err := s.Meta().GetIMAPImportAccount(ctx, keepAcc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("keep: account should be gone, got %v", err)
	}
	if _, err := s.Meta().GetMessage(ctx, keepMsg); err != nil {
		t.Errorf("keep: message should survive, got %v", err)
	}
	if !isMember(keepMsg, keepAcc.ProvenanceMailboxID) {
		t.Error("keep: provenance label should be retained as an ordinary label")
	}

	// -- PURGE single-source: destroy the message + decrement blob refcount. --
	soloAcc := mkAccount("Solo", "Label Solo")
	soloMsg, soloHash := insertMsg("solo-body-unique")
	addLabel(soloMsg, soloAcc.ProvenanceMailboxID)
	recordState(soloAcc.ID, soloMsg, 1)

	// The blob is referenced before purge (one message holds it). Blobs are
	// refcounted per messages row, so the principal's blob-hash set is a
	// faithful proxy: when the message is destroyed and the last reference
	// dropped, the hash leaves the set.
	if !containsStr(blobHashes(t, s, p.ID), soloHash) {
		t.Fatalf("solo blob hash should be referenced before purge")
	}

	res, err = store.RemoveIMAPImportAccount(ctx, s, p.ID, soloAcc.ID, true)
	if err != nil {
		t.Fatalf("RemoveIMAPImportAccount (purge solo): %v", err)
	}
	if res.MessagesDestroyed != 1 || res.LabelsDetached != 0 {
		t.Errorf("purge solo result = %+v; want {1,0}", res)
	}
	if _, err := s.Meta().GetMessage(ctx, soloMsg); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("purge solo: message should be destroyed, got %v", err)
	}
	// Blob reference dropped (refcount decremented to zero).
	if containsStr(blobHashes(t, s, p.ID), soloHash) {
		t.Errorf("purge solo: blob hash still referenced; refcount was not decremented")
	}

	// -- PURGE shared: two accounts claim one message; purge one keeps it. --
	accA := mkAccount("Shared A", "Label A")
	accB := mkAccount("Shared B", "Label B")
	sharedMsg, _ := insertMsg("shared-body-unique")
	addLabel(sharedMsg, accA.ProvenanceMailboxID)
	addLabel(sharedMsg, accB.ProvenanceMailboxID)
	recordState(accA.ID, sharedMsg, 1)
	recordState(accB.ID, sharedMsg, 1)
	beforeCount := memberCount(sharedMsg) // INBOX + Label A + Label B = 3

	res, err = store.RemoveIMAPImportAccount(ctx, s, p.ID, accA.ID, true)
	if err != nil {
		t.Fatalf("RemoveIMAPImportAccount (purge shared A): %v", err)
	}
	if res.MessagesDestroyed != 0 || res.LabelsDetached != 1 {
		t.Errorf("purge shared result = %+v; want {0,1}", res)
	}
	if _, err := s.Meta().GetMessage(ctx, sharedMsg); err != nil {
		t.Errorf("purge shared: message should survive (claimed by B), got %v", err)
	}
	if isMember(sharedMsg, accA.ProvenanceMailboxID) {
		t.Error("purge shared: account A's label should be removed")
	}
	if !isMember(sharedMsg, accB.ProvenanceMailboxID) {
		t.Error("purge shared: account B's label should be retained")
	}
	if got := memberCount(sharedMsg); got != beforeCount-1 {
		t.Errorf("purge shared: membership count = %d; want %d (only A's label removed)", got, beforeCount-1)
	}
}

// testIMAPImport_SetAccountState verifies SetIMAPImportAccountState
// updates state/last_error and only advances last_success_at when
// non-nil is passed.
func testIMAPImport_SetAccountState(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-state@example.com")

	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      "StateTest",
		Host:             "imap.example.com",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "user",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     []byte("v1:pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}

	// Transition to errored.
	if err := s.Meta().SetIMAPImportAccountState(ctx, acc.ID,
		store.IMAPImportAccountStateErrored, "auth failed", nil); err != nil {
		t.Fatalf("SetIMAPImportAccountState errored: %v", err)
	}
	got, err := s.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if got.State != store.IMAPImportAccountStateErrored {
		t.Errorf("State = %q; want errored", got.State)
	}
	if got.LastError != "auth failed" {
		t.Errorf("LastError = %q; want auth failed", got.LastError)
	}
	if got.LastSuccessAt != nil {
		t.Error("LastSuccessAt should remain nil")
	}

	// Transition back to enabled with a success timestamp.
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.Meta().SetIMAPImportAccountState(ctx, acc.ID,
		store.IMAPImportAccountStateEnabled, "", &now); err != nil {
		t.Fatalf("SetIMAPImportAccountState enabled: %v", err)
	}
	got2, err := s.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount after re-enable: %v", err)
	}
	if got2.State != store.IMAPImportAccountStateEnabled {
		t.Errorf("State = %q; want enabled", got2.State)
	}
	if got2.LastError != "" {
		t.Errorf("LastError = %q; want empty", got2.LastError)
	}
	if got2.LastSuccessAt == nil || !got2.LastSuccessAt.Equal(now) {
		t.Errorf("LastSuccessAt = %v; want %v", got2.LastSuccessAt, now)
	}

	// SetIMAPImportAccountState on a non-existent id returns ErrNotFound.
	if err := s.Meta().SetIMAPImportAccountState(ctx, "nonexistent",
		store.IMAPImportAccountStateEnabled, "", nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetIMAPImportAccountState non-existent: want ErrNotFound, got %v", err)
	}
}

// testIMAPImport_FolderMapReplace verifies SetIMAPImportFolderMap
// replaces the whole mapping in one transaction.
func testIMAPImport_FolderMapReplace(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-fmap@example.com")
	acc := mustCreateIMAPImportAccount(t, s, p.ID, "FolderMap")

	// First write.
	e1 := []store.IMAPImportFolderMapEntry{
		{AccountID: acc.ID, UpstreamFolder: "INBOX", HeroldMailboxName: "INBOX"},
		{AccountID: acc.ID, UpstreamFolder: "Sent Mail", HeroldMailboxName: "Sent"},
	}
	if err := s.Meta().SetIMAPImportFolderMap(ctx, acc.ID, e1); err != nil {
		t.Fatalf("SetIMAPImportFolderMap (first): %v", err)
	}
	got, err := s.Meta().GetIMAPImportFolderMap(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportFolderMap: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries; want 2", len(got))
	}

	// Replace with a single entry.
	e2 := []store.IMAPImportFolderMapEntry{
		{AccountID: acc.ID, UpstreamFolder: "All Mail", HeroldMailboxName: "All Mail"},
	}
	if err := s.Meta().SetIMAPImportFolderMap(ctx, acc.ID, e2); err != nil {
		t.Fatalf("SetIMAPImportFolderMap (replace): %v", err)
	}
	got2, err := s.Meta().GetIMAPImportFolderMap(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportFolderMap after replace: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("after replace: got %d entries; want 1", len(got2))
	}
	if got2[0].UpstreamFolder != "All Mail" {
		t.Errorf("UpstreamFolder = %q; want All Mail", got2[0].UpstreamFolder)
	}

	// Clear with empty slice.
	if err := s.Meta().SetIMAPImportFolderMap(ctx, acc.ID, nil); err != nil {
		t.Fatalf("SetIMAPImportFolderMap (clear): %v", err)
	}
	got3, err := s.Meta().GetIMAPImportFolderMap(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportFolderMap after clear: %v", err)
	}
	if len(got3) != 0 {
		t.Errorf("after clear: got %d entries; want 0", len(got3))
	}
}

// testIMAPImport_FolderCursorUpsertGet verifies UpsertIMAPImportFolderCursor
// inserts on first call and updates on subsequent calls.
func testIMAPImport_FolderCursorUpsertGet(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-cursor@example.com")
	acc := mustCreateIMAPImportAccount(t, s, p.ID, "Cursor")

	// Not found before first upsert.
	_, found, err := s.Meta().GetIMAPImportFolderCursor(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor (initial): %v", err)
	}
	if found {
		t.Error("cursor should not exist before first upsert")
	}

	cursor := store.IMAPImportFolderCursor{
		AccountID:      acc.ID,
		UpstreamFolder: "INBOX",
		UIDValidity:    12345,
		UIDNext:        100,
		LowWaterUID:    10,
		HighWaterUID:   99,
		HighestModSeq:  42,
	}
	if err := s.Meta().UpsertIMAPImportFolderCursor(ctx, cursor); err != nil {
		t.Fatalf("UpsertIMAPImportFolderCursor (insert): %v", err)
	}

	got, found, err := s.Meta().GetIMAPImportFolderCursor(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor (after insert): %v", err)
	}
	if !found {
		t.Fatal("cursor not found after upsert")
	}
	if got.UIDValidity != 12345 {
		t.Errorf("UIDValidity = %d; want 12345", got.UIDValidity)
	}
	if got.HighWaterUID != 99 {
		t.Errorf("HighWaterUID = %d; want 99", got.HighWaterUID)
	}
	if got.HighestModSeq != 42 {
		t.Errorf("HighestModSeq = %d; want 42", got.HighestModSeq)
	}

	// Update: advance high-water mark.
	cursor.HighWaterUID = 200
	cursor.HighestModSeq = 99
	if err := s.Meta().UpsertIMAPImportFolderCursor(ctx, cursor); err != nil {
		t.Fatalf("UpsertIMAPImportFolderCursor (update): %v", err)
	}
	got2, found, err := s.Meta().GetIMAPImportFolderCursor(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatalf("GetIMAPImportFolderCursor (after update): %v", err)
	}
	if !found {
		t.Fatal("cursor not found after update")
	}
	if got2.HighWaterUID != 200 {
		t.Errorf("HighWaterUID after update = %d; want 200", got2.HighWaterUID)
	}
	if got2.HighestModSeq != 99 {
		t.Errorf("HighestModSeq after update = %d; want 99", got2.HighestModSeq)
	}
}

// testIMAPImport_MessageStateUpsertAndLookups verifies UpsertIMAPImportMessageState
// and both lookup paths (by UID and by herold message id).
func testIMAPImport_MessageStateUpsertAndLookups(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-msgstate@example.com")
	acc := mustCreateIMAPImportAccount(t, s, p.ID, "MsgState")

	// Not found before first upsert.
	_, found, err := s.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", 42)
	if err != nil {
		t.Fatalf("GetIMAPImportMessageState (initial): %v", err)
	}
	if found {
		t.Error("message state should not exist before upsert")
	}

	ms := store.IMAPImportMessageState{
		AccountID:       acc.ID,
		UpstreamFolder:  "INBOX",
		UpstreamUID:     42,
		HeroldMessageID: 1001,
		HeroldMailboxID: 2001,
		LastSyncedFlags: store.IMAPImportFlagSeen | store.IMAPImportFlagFlagged,
	}
	if err := s.Meta().UpsertIMAPImportMessageState(ctx, ms); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState: %v", err)
	}

	// Lookup by UID.
	got, found, err := s.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", 42)
	if err != nil {
		t.Fatalf("GetIMAPImportMessageState: %v", err)
	}
	if !found {
		t.Fatal("message state not found after upsert")
	}
	if got.HeroldMessageID != 1001 {
		t.Errorf("HeroldMessageID = %d; want 1001", got.HeroldMessageID)
	}
	if got.HeroldMailboxID != 2001 {
		t.Errorf("HeroldMailboxID = %d; want 2001", got.HeroldMailboxID)
	}
	if got.LastSyncedFlags != (store.IMAPImportFlagSeen | store.IMAPImportFlagFlagged) {
		t.Errorf("LastSyncedFlags = %d; want Seen|Flagged", got.LastSyncedFlags)
	}

	// Lookup by herold message id.
	got2, found, err := s.Meta().GetIMAPImportMessageStateByMessage(ctx, acc.ID, 1001)
	if err != nil {
		t.Fatalf("GetIMAPImportMessageStateByMessage: %v", err)
	}
	if !found {
		t.Fatal("message state by message not found")
	}
	if got2.UpstreamUID != 42 {
		t.Errorf("UpstreamUID = %d; want 42", got2.UpstreamUID)
	}

	// Update: clear \Flagged.
	ms.LastSyncedFlags = store.IMAPImportFlagSeen
	if err := s.Meta().UpsertIMAPImportMessageState(ctx, ms); err != nil {
		t.Fatalf("UpsertIMAPImportMessageState (update): %v", err)
	}
	got3, _, _ := s.Meta().GetIMAPImportMessageState(ctx, acc.ID, "INBOX", 42)
	if got3.LastSyncedFlags != store.IMAPImportFlagSeen {
		t.Errorf("LastSyncedFlags after update = %d; want Seen only", got3.LastSyncedFlags)
	}

	// GetIMAPImportMessageStateByMessage with non-existent message id.
	_, found, err = s.Meta().GetIMAPImportMessageStateByMessage(ctx, acc.ID, 9999)
	if err != nil {
		t.Fatalf("GetIMAPImportMessageStateByMessage (not found): %v", err)
	}
	if found {
		t.Error("found message state for non-existent herold message id")
	}
}

// testIMAPImport_CredentialCTValidation verifies that CreateIMAPImportAccount
// rejects a non-v1: credential (REQ-IMAP-IMP-70).
func testIMAPImport_CredentialCTValidation(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-ct@example.com")

	_, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:      p.ID,
		AccountName:      "BadCT",
		Host:             "imap.example.com",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "user",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     []byte("plaintext-password"), // no v1: prefix
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err == nil {
		t.Fatal("CreateIMAPImportAccount with bare credential: expected rejection, got nil")
	}
	if !errors.Is(err, store.ErrInvalidArgument) {
		t.Errorf("want ErrInvalidArgument, got %v", err)
	}
}

// testIMAPImport_NullBackfillFloor verifies that a nil BackfillFloorDate
// round-trips as nil (meaning "all" / no floor, REQ-IMAP-IMP-15).
func testIMAPImport_NullBackfillFloor(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "imap-nofloar@example.com")

	acc, err := s.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		PrincipalID:       p.ID,
		AccountName:       "NoFloor",
		Host:              "imap.example.com",
		Port:              993,
		TLSMode:           store.IMAPImportTLSModeImplicit,
		Username:          "user",
		AuthMethod:        store.IMAPImportAuthMethodPassword,
		BackfillFloorDate: nil,
		CredentialCT:      []byte("v1:pw"),
		State:             store.IMAPImportAccountStateEnabled,
		DeletePropagates:  true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	if acc.BackfillFloorDate != nil {
		t.Errorf("BackfillFloorDate = %v; want nil", acc.BackfillFloorDate)
	}
	got, err := s.Meta().GetIMAPImportAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetIMAPImportAccount: %v", err)
	}
	if got.BackfillFloorDate != nil {
		t.Errorf("Get BackfillFloorDate = %v; want nil", got.BackfillFloorDate)
	}
}

// testIMAPImport_DeleteNotFound verifies DeleteIMAPImportAccount returns
// ErrNotFound when the account does not belong to the supplied principal.
func testIMAPImport_DeleteNotFound(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	p1 := mustInsertPrincipal(t, s, "imap-delnf1@example.com")
	p2 := mustInsertPrincipal(t, s, "imap-delnf2@example.com")
	acc := mustCreateIMAPImportAccount(t, s, p1.ID, "WrongOwner")

	// Delete using wrong principal.
	err := s.Meta().DeleteIMAPImportAccount(ctx, p2.ID, acc.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteIMAPImportAccount wrong principal: want ErrNotFound, got %v", err)
	}
}

// mustCreateIMAPImportAccount is a test helper that creates an account
// and fails the test on error.
func mustCreateIMAPImportAccount(t *testing.T, s store.Store, pid store.PrincipalID, name string) store.IMAPImportAccount {
	t.Helper()
	acc, err := s.Meta().CreateIMAPImportAccount(ctxT(t), store.IMAPImportAccountCreate{
		PrincipalID:      pid,
		AccountName:      name,
		Host:             "imap.example.com",
		Port:             993,
		TLSMode:          store.IMAPImportTLSModeImplicit,
		Username:         "user",
		AuthMethod:       store.IMAPImportAuthMethodPassword,
		CredentialCT:     []byte("v1:test-pw"),
		State:            store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("mustCreateIMAPImportAccount %q: %v", name, err)
	}
	return acc
}
