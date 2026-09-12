package identity

// Sub-account separation JMAP surface (issue #227, REQ-SUBACCT-09/10):
// Identity/set{separated} drives store.SeparateIdentity /
// store.RunSubAccountMigration / store.RemoveSubAccount, and Identity/
// get's derived subAccountId/separation properties reflect the
// resulting state. The store-level promotion/removal mechanics
// (idempotency, crash-safety, dedup) are covered by
// internal/store/storetest/storetest_subaccounts.go; these tests
// exercise only the JMAP-facing wiring: capability gating, the
// synchronous move + background sweep, the isolation between the
// parent's and sub-account's Identity/get listings, and the reversal
// path.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
)

// newHandlersWithSubAccounts is newHandlers plus a capability registry
// that carries the sub-accounts capability (REQ-SUBACCT-11), so
// Identity/set{separated} is not rejected as capability-absent.
func newHandlersWithSubAccounts(t *testing.T) (*handlerSet, store.Store, store.Principal) {
	t.Helper()
	h, st, p := newHandlers(t)
	reg := protojmap.NewCapabilityRegistry()
	reg.RegisterCapabilityDescriptor(protojmap.CapabilitySubAccounts, struct{}{})
	h.reg = reg
	return h, st, p
}

// newHandlersWithSubAccountsPostgres is newHandlersWithSubAccounts'
// Postgres counterpart: same fixture wiring, opened against
// HEROLD_PG_DSN instead of an on-disk SQLite file. Skips the test when
// HEROLD_PG_DSN is unset or the connection cannot be established, so
// tests using it run as a no-op locally without a running Postgres and
// as a real parity check in CI's storepg leg.
func newHandlersWithSubAccountsPostgres(t *testing.T) (*handlerSet, store.Store, store.Principal) {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, nil)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	canonicalEmail := fmt.Sprintf("alice-%d@example.test", time.Now().UnixNano())
	h, st, p := newHandlersUsingStore(t, st, canonicalEmail)
	reg := protojmap.NewCapabilityRegistry()
	reg.RegisterCapabilityDescriptor(protojmap.CapabilitySubAccounts, struct{}{})
	h.reg = reg
	return h, st, p
}

// insertImportAccountWithMessages attaches an IMAP-import account to
// identityID (owned by pid) and records n tracked messages for it,
// mirroring the ingest shape storetest.newSeparationFixture uses: each
// message lands in INBOX plus the account's provenance label, with a
// matching imapimport_message_state row.
func insertImportAccountWithMessages(t *testing.T, st store.Store, pid store.PrincipalID, identityID string, n int) store.IMAPImportAccount {
	t.Helper()
	ctx := context.Background()
	inbox, err := st.Meta().GetMailboxByName(ctx, pid, "INBOX")
	if err != nil {
		inbox, err = st.Meta().InsertMailbox(ctx, store.Mailbox{
			PrincipalID: pid, Name: "INBOX", Attributes: store.MailboxAttrInbox,
		})
		if err != nil {
			t.Fatalf("InsertMailbox INBOX: %v", err)
		}
	}
	acc, err := st.Meta().CreateIMAPImportAccount(ctx, store.IMAPImportAccountCreate{
		IdentityID: identityID, PrincipalID: pid, AccountName: "ext",
		Host: "imap.external.test", Port: 993, TLSMode: store.IMAPImportTLSModeImplicit,
		Username: "ext", AuthMethod: store.IMAPImportAuthMethodAppPassword,
		CredentialCT: []byte("v1:pw"), State: store.IMAPImportAccountStateEnabled,
		DeletePropagates: true,
	})
	if err != nil {
		t.Fatalf("CreateIMAPImportAccount: %v", err)
	}
	prov, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: pid, Name: "ext label " + identityID})
	if err != nil {
		t.Fatalf("InsertMailbox label: %v", err)
	}
	if err := st.Meta().SetIMAPImportProvenanceMailbox(ctx, acc.ID, prov.ID); err != nil {
		t.Fatalf("SetIMAPImportProvenanceMailbox: %v", err)
	}
	acc.ProvenanceMailboxID = prov.ID
	for i := 0; i < n; i++ {
		blob, err := st.Blobs().Put(ctx, strings.NewReader(fmt.Sprintf("msg-%d-%d-%d", pid, i, time.Now().UnixNano())))
		if err != nil {
			t.Fatalf("Blobs.Put: %v", err)
		}
		if _, _, err := st.Meta().InsertMessage(ctx,
			store.Message{PrincipalID: pid, Blob: blob, Size: blob.Size},
			[]store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
			t.Fatalf("InsertMessage: %v", err)
		}
		msg, err := st.Meta().GetMessageByBlobHash(ctx, pid, blob.Hash)
		if err != nil {
			t.Fatalf("GetMessageByBlobHash: %v", err)
		}
		if _, _, err := st.Meta().AddMessageToMailbox(ctx, msg.ID, prov.ID); err != nil {
			t.Fatalf("AddMessageToMailbox: %v", err)
		}
		if err := st.Meta().UpsertIMAPImportMessageState(ctx, store.IMAPImportMessageState{
			AccountID: acc.ID, UpstreamFolder: "INBOX", UpstreamUID: uint32(i + 1),
			HeroldMessageID: msg.ID, HeroldMailboxID: inbox.ID,
		}); err != nil {
			t.Fatalf("UpsertIMAPImportMessageState: %v", err)
		}
	}
	return acc
}

// getSeparation extracts the "separation" object and "subAccountId" for
// wireID out of an Identity/get response's raw JSON re-decode (simpler
// than threading the unexported jmapIdentity type through assertions).
func getSeparationJSON(t *testing.T, h *handlerSet, p store.Principal, accountID, wireID string) map[string]any {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"accountId": accountID, "ids": []string{wireID}})
	resp, mErr := getHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/get: %v", mErr)
	}
	js, _ := json.Marshal(resp)
	var decoded struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(js, &decoded); err != nil {
		t.Fatalf("decode Identity/get response: %v", err)
	}
	if len(decoded.List) != 1 {
		t.Fatalf("Identity/get(%s) on %s: got %d entries, want 1: %s", wireID, accountID, len(decoded.List), js)
	}
	return decoded.List[0]
}

// TestIdentity_Get_PrecountsMessagesBeforeSeparation verifies the
// REQ-SUBACCT-09 pre-count: an identity that has never been separated
// reports separation.state == "none" and separation.messagesTotal
// equal to the distinct-message count of its IMAP-import account(s),
// so the suite can show the count before the user confirms
// (REQ-MAIL-SUB-07).
func TestIdentity_Get_PrecountsMessagesBeforeSeparation(t *testing.T) {
	h, st, p := newHandlersWithSubAccounts(t)
	created := createIdentity(t, h, p, "ext", "External", "ext@example.test")
	insertImportAccountWithMessages(t, st, p.ID, created.ID, 3)

	row := getSeparationJSON(t, h, p, string(protojmap.AccountIDForPrincipal(p.ID)), created.ID)
	if row["subAccountId"] != nil {
		t.Fatalf("subAccountId = %v; want null before separation", row["subAccountId"])
	}
	sep, ok := row["separation"].(map[string]any)
	if !ok {
		t.Fatalf("separation missing or wrong shape: %v", row["separation"])
	}
	if sep["state"] != "none" {
		t.Fatalf("separation.state = %v; want none", sep["state"])
	}
	if got := sep["messagesTotal"]; got != float64(3) {
		t.Fatalf("separation.messagesTotal = %v; want 3", got)
	}
}

// TestIdentity_Set_SeparatedTrue_RejectedWithoutCapability verifies
// REQ-SUBACCT-11: absent the sub-accounts capability, separated:true
// is rejected rather than silently promoting the identity.
func TestIdentity_Set_SeparatedTrue_RejectedWithoutCapability(t *testing.T) {
	h, _, p := newHandlers(t) // no capability registry wired
	created := createIdentity(t, h, p, "ext", "External", "ext@example.test")
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update":    map[string]any{created.ID: map[string]any{"separated": true}},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	se, ok := resp.(setResponse).NotUpdated[created.ID]
	if !ok {
		t.Fatalf("expected notUpdated; got updated: %+v", resp.(setResponse).Updated)
	}
	if se.Type != "forbidden" {
		t.Fatalf("setError.Type = %q; want forbidden", se.Type)
	}
}

// TestIdentity_Set_SeparatedTrue_RejectsDefaultIdentity verifies the
// synthesised default identity ("default") cannot be separated
// (REQ-IMAP-IMP-01: import is meaningful only for non-default,
// external-domain identities; store.SeparateIdentity itself rejects
// "default" outright).
func TestIdentity_Set_SeparatedTrue_RejectsDefaultIdentity(t *testing.T) {
	h, _, p := newHandlersWithSubAccounts(t)
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update":    map[string]any{"default": map[string]any{"separated": true}},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	se, ok := resp.(setResponse).NotUpdated["default"]
	if !ok {
		t.Fatalf("expected notUpdated[default]; got updated: %+v", resp.(setResponse).Updated)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("setError.Type = %q; want invalidProperties", se.Type)
	}
}

// TestIdentity_Set_SeparatedTrue_RejectsWhileMigrationRunning
// simulates an in-flight sweep (by inserting the SubAccountMigration
// row directly, bypassing the background goroutine's timing) and
// verifies a second separated:true is rejected rather than starting a
// concurrent sweep.
func TestIdentity_Set_SeparatedTrue_RejectsWhileMigrationRunning(t *testing.T) {
	h, st, p := newHandlersWithSubAccounts(t)
	created := createIdentity(t, h, p, "ext", "External", "ext@example.test")
	sub, err := st.Meta().InsertSubPrincipal(context.Background(), p.ID, store.Principal{CanonicalEmail: "ext@example.test"})
	if err != nil {
		t.Fatalf("InsertSubPrincipal: %v", err)
	}
	if err := st.Meta().RebindJMAPIdentityPrincipal(context.Background(), created.ID, sub.ID); err != nil {
		t.Fatalf("RebindJMAPIdentityPrincipal: %v", err)
	}
	if _, err := st.Meta().InsertSubAccountMigration(context.Background(), store.SubAccountMigration{
		ParentPrincipalID: p.ID, SubPrincipalID: sub.ID, IdentityID: created.ID,
		Status: store.SubAccountMigrationStatusRunning,
	}); err != nil {
		t.Fatalf("InsertSubAccountMigration: %v", err)
	}

	args, _ := json.Marshal(map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(sub.ID)),
		"update":    map[string]any{created.ID: map[string]any{"separated": true}},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set: %v", mErr)
	}
	se, ok := resp.(setResponse).NotUpdated[created.ID]
	if !ok {
		t.Fatalf("expected notUpdated; got updated: %+v", resp.(setResponse).Updated)
	}
	if se.Type != "invalidProperties" {
		t.Fatalf("setError.Type = %q; want invalidProperties", se.Type)
	}
}

// TestIdentity_Set_Separation_FullRoundTrip drives the whole
// REQ-SUBACCT-09/10 lifecycle through the JMAP surface: separated:true
// moves the identity into a sub-account and starts the background
// sweep; once the sweep completes the identity is listed under the
// sub-account (with separation.state "separated" and non-zero counts)
// and absent from the parent's Identity/get; separated:false with the
// default keepMail returns it to the parent, un-separated.
func TestIdentity_Set_Separation_FullRoundTrip(t *testing.T) {
	h, st, p := newHandlersWithSubAccounts(t)
	created := createIdentity(t, h, p, "ext", "External", "ext@example.test")
	insertImportAccountWithMessages(t, st, p.ID, created.ID, 2)

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update":    map[string]any{created.ID: map[string]any{"separated": true}},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set separated:true: %v", mErr)
	}
	updated, ok := resp.(setResponse).Updated[created.ID]
	if !ok {
		t.Fatalf("expected updated; got notUpdated: %+v", resp.(setResponse).NotUpdated)
	}
	if updated.SubAccountId == nil {
		t.Fatal("subAccountId is nil immediately after separated:true")
	}
	subAccountID := *updated.SubAccountId

	// The identity has already moved: it is gone from the parent's
	// Identity/get and present under the sub-account's.
	parentArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID), "ids": []string{created.ID}})
	parentResp, mErr := getHandler{h: h}.executeAs(p, parentArgs)
	if mErr != nil {
		t.Fatalf("Identity/get(parent): %v", mErr)
	}
	if len(parentResp.(getResponse).List) != 0 {
		t.Fatalf("identity still listed under parent after separation: %+v", parentResp.(getResponse).List)
	}
	if len(parentResp.(getResponse).NotFound) != 1 {
		t.Fatalf("expected notFound[%s] under parent: %+v", created.ID, parentResp.(getResponse).NotFound)
	}

	// Wait for the background sweep to finish (RunSubAccountMigration
	// re-derives its work list from imapimport_message_state, so
	// polling its own idempotent re-run is a safe, side-effect-free way
	// to observe completion without a sleep-based race).
	deadline := time.Now().Add(5 * time.Second)
	var mig store.SubAccountMigration
	for {
		var err error
		mig, err = st.Meta().GetSubAccountMigrationByIdentity(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("GetSubAccountMigrationByIdentity: %v", err)
		}
		if mig.Status == store.SubAccountMigrationStatusDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migration did not complete within deadline: %+v", mig)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if mig.MessagesMoved != 2 {
		t.Fatalf("MessagesMoved = %d; want 2", mig.MessagesMoved)
	}

	row := getSeparationJSON(t, h, p, subAccountID, created.ID)
	sep := row["separation"].(map[string]any)
	if sep["state"] != "separated" {
		t.Fatalf("separation.state = %v; want separated", sep["state"])
	}
	if got := sep["messagesMoved"]; got != float64(2) {
		t.Fatalf("separation.messagesMoved = %v; want 2", got)
	}

	// separated:false (default keepMail:true) reverses it, addressed
	// via the sub-account's own accountId.
	revertArgs, _ := json.Marshal(map[string]any{
		"accountId": subAccountID,
		"update":    map[string]any{created.ID: map[string]any{"separated": false}},
	})
	revertResp, mErr := setHandler{h: h}.executeAs(p, revertArgs)
	if mErr != nil {
		t.Fatalf("Identity/set separated:false: %v", mErr)
	}
	reverted, ok := revertResp.(setResponse).Updated[created.ID]
	if !ok {
		t.Fatalf("expected updated after reversal; got notUpdated: %+v", revertResp.(setResponse).NotUpdated)
	}
	if reverted.SubAccountId != nil {
		t.Fatalf("subAccountId = %v; want null after reversal", *reverted.SubAccountId)
	}
	if reverted.Separation.State != separationStateNone {
		t.Fatalf("separation.state after reversal = %q; want none", reverted.Separation.State)
	}

	// The identity is listed under the parent again.
	backArgs, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID), "ids": []string{created.ID}})
	backResp, mErr := getHandler{h: h}.executeAs(p, backArgs)
	if mErr != nil {
		t.Fatalf("Identity/get(parent, after reversal): %v", mErr)
	}
	if len(backResp.(getResponse).List) != 1 {
		t.Fatalf("identity not listed under parent after reversal: %+v", backResp.(getResponse))
	}
}

// assertSubAccountListsPromotedIdentityOnce is the shared body of
// TestIdentity_Get_SubAccount_NoDuplicateDefault: it verifies issue
// #337 -- once an identity has been separated, a full Identity/get (no
// ids filter) against the sub-account lists that identity exactly once.
// The synthesised "default" (whose email is always the account's
// CanonicalEmail, set to the promoted identity's address by
// store.SeparateIdentity) must not also appear; the promoted row
// stands in for the default and carries isDefault and mayDelete=false.
func assertSubAccountListsPromotedIdentityOnce(t *testing.T, h *handlerSet, st store.Store, p store.Principal, wantEmail string) {
	t.Helper()
	created := createIdentity(t, h, p, "ext", "External", wantEmail)

	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update":    map[string]any{created.ID: map[string]any{"separated": true}},
	})
	resp, mErr := setHandler{h: h}.executeAs(p, args)
	if mErr != nil {
		t.Fatalf("Identity/set separated:true: %v", mErr)
	}
	updated, ok := resp.(setResponse).Updated[created.ID]
	if !ok || updated.SubAccountId == nil {
		t.Fatalf("expected updated with subAccountId; got %+v / notUpdated %+v", updated, resp.(setResponse).NotUpdated)
	}
	subAccountID := *updated.SubAccountId

	deadline := time.Now().Add(5 * time.Second)
	for {
		mig, err := st.Meta().GetSubAccountMigrationByIdentity(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("GetSubAccountMigrationByIdentity: %v", err)
		}
		if mig.Status == store.SubAccountMigrationStatusDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("migration did not complete within deadline: %+v", mig)
		}
		time.Sleep(10 * time.Millisecond)
	}

	listArgs, _ := json.Marshal(map[string]any{"accountId": subAccountID})
	listResp, mErr := getHandler{h: h}.executeAs(p, listArgs)
	if mErr != nil {
		t.Fatalf("Identity/get(sub-account): %v", mErr)
	}
	list := listResp.(getResponse).List
	if len(list) != 1 {
		t.Fatalf("sub-account Identity/get list len = %d; want 1: %+v", len(list), list)
	}
	got := list[0]
	if string(got.ID) != created.ID {
		t.Fatalf("sub-account identity id = %q; want %q", got.ID, created.ID)
	}
	if got.Email != wantEmail {
		t.Fatalf("sub-account identity email = %q; want %q", got.Email, wantEmail)
	}
	if !got.IsDefault {
		t.Fatalf("sub-account identity IsDefault = false; want true (it is the account's only identity)")
	}
	if got.MayDelete {
		t.Fatalf("sub-account identity MayDelete = true; want false (it is the account's only identity for its own address)")
	}
}

func TestIdentity_Get_SubAccount_NoDuplicateDefault(t *testing.T) {
	h, st, p := newHandlersWithSubAccounts(t)
	assertSubAccountListsPromotedIdentityOnce(t, h, st, p, "ext@example.test")
}

// TestIdentity_Get_SubAccount_NoDuplicateDefault_Postgres is the
// Postgres-backed parity leg for issue #337 (STANDARDS.md SS8: every
// integration test runs on both backends). Skips when HEROLD_PG_DSN is
// unset.
func TestIdentity_Get_SubAccount_NoDuplicateDefault_Postgres(t *testing.T) {
	h, st, p := newHandlersWithSubAccountsPostgres(t)
	wantEmail := fmt.Sprintf("ext-%d@example.test", time.Now().UnixNano())
	assertSubAccountListsPromotedIdentityOnce(t, h, st, p, wantEmail)
}
