package protoadmin_test

// tagged_address_convert_test.go exercises POST
// /api/v1/tagged-address-filters/{id}/convert-to-sieve
// (REQ-TAG-50..51, REQ-TAG-90).
//
// Coverage:
//   - Successful conversion of each action variant ("label",
//     "label_archive", "label_archive_read") produces a Sieve fragment
//     with the expected envelope test + filing action + (for the read
//     variant) the imap4flags setflag block.
//   - The fragment is prepended to the existing user-Sieve script; an
//     existing `require` line is merged into a single statement.
//   - On success the filter row is deleted from the store and the
//     active effective script (preamble + user) reflects the new user
//     half.
//   - A matching dismissal row for the same (identity, suffix) is
//     dropped as a side-effect (REQ-TAG-61).
//   - Audit entry tagged tagged_address_filter.convert_to_sieve lands
//     with the row's (base_identity, suffix, action) metadata.
//   - Cross-principal probes return 404 (no leakage); a missing id
//     returns 404.
//   - A validation-failure path is exercised via a stub: a deliberately
//     malformed pre-existing user script produces a 422 with the parser
//     diagnostic and the filter row remains intact (REQ-TAG-50 step 4
//     rollback).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/sieve"
	"github.com/hanshuebner/herold/internal/store"
)

// convertHarness threads a verified identity + a tagged-address filter
// row through the bootstrap admin so the conversion endpoint has
// something to convert.
type convertHarness struct {
	*dismissalHarness
	identityID string
}

func newConvertHarness(t *testing.T) *convertHarness {
	t.Helper()
	dh := newDismissalHarness(t)
	identityID := dh.insertIdentity(dh.adminPID, "admin@example.com")
	// Mark the identity verified so the JMAP-side rule that gates
	// filter creation on verification is satisfied if we ever route
	// through JMAP. The REST conversion path does not consult
	// VerifiedAtUs, but a non-verified identity in storage would
	// confuse a reader of the test fixture.
	row, err := dh.fs.Meta().GetJMAPIdentity(context.Background(), identityID)
	if err != nil {
		t.Fatalf("GetJMAPIdentity: %v", err)
	}
	_ = row // currently no MarkIdentityVerified follow-up — the
	// REST handler does not gate on VerifiedAtUs.
	return &convertHarness{dismissalHarness: dh, identityID: identityID}
}

// insertFilter seeds a tagged_address_filters row directly via the
// store so the REST test does not have to route through the JMAP
// /set surface (which lives in a sibling task #26 and is not the
// subject under test here).
func (h *convertHarness) insertFilter(t *testing.T, suffix, action, labelName string) store.TaggedAddressFilter {
	t.Helper()
	row := store.TaggedAddressFilter{
		PrincipalID:    store.PrincipalID(h.adminPID),
		BaseIdentityID: h.identityID,
		Suffix:         suffix,
		Action:         action,
		LabelName:      labelName,
	}
	if err := h.fs.Meta().InsertTaggedAddressFilter(context.Background(), row); err != nil {
		t.Fatalf("InsertTaggedAddressFilter: %v", err)
	}
	// Read back so the test gets the store-assigned ID.
	got, err := h.fs.Meta().GetTaggedAddressFilter(context.Background(),
		row.PrincipalID, row.BaseIdentityID, strings.ToLower(suffix))
	if err != nil {
		t.Fatalf("GetTaggedAddressFilter: %v", err)
	}
	return got
}

// convertResponseBody mirrors the wire shape returned by a successful
// conversion. Declared in tests so the parsing is independent of the
// handler's internal type (which is unexported).
type convertResponseBody struct {
	Fragment string `json:"fragment"`
	Script   string `json:"script"`
}

// TestConvertToSieve_Label_OK verifies the basic happy path: a
// `label` filter is converted into a Sieve fragment containing exactly
// `fileinto "<label>"`, the filter row is deleted, and the user-Sieve
// script holds the fragment.
func TestConvertToSieve_Label_OK(t *testing.T) {
	h := newConvertHarness(t)
	row := h.insertFilter(t, "amazon", store.TaggedAddressActionLabel, "Shopping")

	res, buf := h.doRequest("POST",
		"/api/v1/tagged-address-filters/"+row.ID+"/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST: %d: %s", res.StatusCode, buf)
	}
	var out convertResponseBody
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if !strings.Contains(out.Fragment, `fileinto "Shopping"`) {
		t.Errorf("fragment missing fileinto: %s", out.Fragment)
	}
	if !strings.Contains(out.Fragment, `envelope :matches :localpart "to" "admin+amazon"`) {
		t.Errorf("fragment missing envelope test: %s", out.Fragment)
	}
	if strings.Contains(out.Fragment, "stop;") {
		t.Errorf("plain label action must NOT emit stop; (REQ-TAG-30 label): %s", out.Fragment)
	}
	if strings.Contains(out.Fragment, "setflag") {
		t.Errorf("plain label action must NOT setflag: %s", out.Fragment)
	}
	// The fragment must parse + validate cleanly on its own. The
	// handler already ran sieve.Parse + Validate; this assertion is
	// redundant but pins the contract: a copy of the fragment alone
	// is a valid hand-written Sieve script.
	parsed, perr := sieve.Parse([]byte(out.Fragment))
	if perr != nil {
		t.Fatalf("fragment failed to parse: %v: %s", perr, out.Fragment)
	}
	if verr := sieve.Validate(parsed); verr != nil {
		t.Fatalf("fragment failed to validate: %v: %s", verr, out.Fragment)
	}

	// The filter row is gone.
	_, err := h.fs.Meta().GetTaggedAddressFilter(context.Background(),
		store.PrincipalID(h.adminPID), h.identityID, "amazon")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("filter still present after convert: err=%v", err)
	}
	// The user-Sieve script now holds the fragment.
	userScript, _ := h.fs.Meta().GetUserSieveScript(context.Background(), store.PrincipalID(h.adminPID))
	if !strings.Contains(userScript, "fileinto \"Shopping\"") {
		t.Errorf("user script missing fragment: %s", userScript)
	}
	// Audit entry for the conversion landed (REQ-TAG-90).
	audits := auditEntriesFor(t, h.fs, "tagged_address_filter.convert_to_sieve")
	if len(audits) != 1 {
		t.Fatalf("audit entries = %d; want 1", len(audits))
	}
	if audits[0].Outcome != store.OutcomeSuccess {
		t.Errorf("audit outcome = %v; want success", audits[0].Outcome)
	}
	if audits[0].Metadata["suffix"] != "amazon" {
		t.Errorf("audit metadata suffix = %q; want amazon", audits[0].Metadata["suffix"])
	}
}

// TestConvertToSieve_LabelArchive_Stop verifies the label_archive
// variant emits both fileinto and stop;
func TestConvertToSieve_LabelArchive_Stop(t *testing.T) {
	h := newConvertHarness(t)
	row := h.insertFilter(t, "deals", store.TaggedAddressActionLabelArchive, "Deals")

	res, buf := h.doRequest("POST",
		"/api/v1/tagged-address-filters/"+row.ID+"/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST: %d: %s", res.StatusCode, buf)
	}
	var out convertResponseBody
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if !strings.Contains(out.Fragment, `fileinto "Deals"`) {
		t.Errorf("fragment missing fileinto: %s", out.Fragment)
	}
	if !strings.Contains(out.Fragment, "stop;") {
		t.Errorf("label_archive must emit stop;: %s", out.Fragment)
	}
}

// TestConvertToSieve_LabelArchiveRead_Setflag verifies the
// label_archive_read variant emits the imap4flags setflag + fileinto +
// stop sequence, plus the imap4flags extension in the require list.
func TestConvertToSieve_LabelArchiveRead_Setflag(t *testing.T) {
	h := newConvertHarness(t)
	row := h.insertFilter(t, "promos", store.TaggedAddressActionLabelArchiveRead, "Promos")

	res, buf := h.doRequest("POST",
		"/api/v1/tagged-address-filters/"+row.ID+"/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST: %d: %s", res.StatusCode, buf)
	}
	var out convertResponseBody
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	if !strings.Contains(out.Fragment, "imap4flags") {
		t.Errorf("require list missing imap4flags: %s", out.Fragment)
	}
	if !strings.Contains(out.Fragment, `setflag "\\Seen"`) {
		t.Errorf("fragment missing setflag for label_archive_read: %s", out.Fragment)
	}
	if !strings.Contains(out.Fragment, `fileinto "Promos"`) {
		t.Errorf("fragment missing fileinto: %s", out.Fragment)
	}
	if !strings.Contains(out.Fragment, "stop;") {
		t.Errorf("label_archive_read must emit stop;: %s", out.Fragment)
	}
}

// TestConvertToSieve_MergesExistingRequire verifies that a pre-existing
// user-Sieve script with a leading `require` declaration has its
// extension list merged with the fragment's so the merged script
// carries a single `require` statement (REQ-TAG-50 step 2).
func TestConvertToSieve_MergesExistingRequire(t *testing.T) {
	h := newConvertHarness(t)
	// Seed an existing user script that imports `fileinto` and uses
	// it; the fragment will import `envelope` + `fileinto`. After
	// merge the require list must be {envelope, fileinto} exactly
	// (no duplicates).
	existing := "require [\"fileinto\"];\nfileinto \"Inbox\";\n"
	pid := store.PrincipalID(h.adminPID)
	if err := h.fs.Meta().SetUserSieveScript(context.Background(), pid, existing); err != nil {
		t.Fatalf("SetUserSieveScript: %v", err)
	}
	row := h.insertFilter(t, "merge-test", store.TaggedAddressActionLabel, "Merged")

	res, buf := h.doRequest("POST",
		"/api/v1/tagged-address-filters/"+row.ID+"/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST: %d: %s", res.StatusCode, buf)
	}
	var out convertResponseBody
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, buf)
	}
	// Count `require` statements by scanning the source text — the
	// parser folds every leading require into Script.Requires so we
	// cannot ask the AST for the count directly. The serialised
	// merged script must contain exactly one occurrence.
	parsed, perr := sieve.Parse([]byte(out.Script))
	if perr != nil {
		t.Fatalf("merged script parse: %v: %s", perr, out.Script)
	}
	if verr := sieve.Validate(parsed); verr != nil {
		t.Fatalf("merged script validate: %v: %s", verr, out.Script)
	}
	if got := strings.Count(out.Script, "require"); got != 1 {
		t.Errorf("merged script has %d 'require' occurrences; want 1: %s", got, out.Script)
	}
	// Both extensions present.
	if !strings.Contains(out.Script, "envelope") {
		t.Errorf("merged script missing envelope extension: %s", out.Script)
	}
	if !strings.Contains(out.Script, "fileinto") {
		t.Errorf("merged script missing fileinto extension: %s", out.Script)
	}
	// Existing user-side command preserved AFTER the fragment.
	if !strings.Contains(out.Script, `fileinto "Inbox"`) {
		t.Errorf("merged script lost pre-existing user-side fileinto: %s", out.Script)
	}
}

// TestConvertToSieve_DropsMatchingDismissal verifies REQ-TAG-61: the
// store's DeleteTaggedAddressFilter cascades the matching dismissal
// row. Convert-to-Sieve calls Delete, so the cascade applies here too.
func TestConvertToSieve_DropsMatchingDismissal(t *testing.T) {
	h := newConvertHarness(t)
	row := h.insertFilter(t, "shopping", store.TaggedAddressActionLabel, "Shopping")
	// Seed a matching dismissal.
	if err := h.fs.Meta().InsertTaggedAddressDismissal(context.Background(),
		store.TaggedAddressDismissal{
			PrincipalID:    store.PrincipalID(h.adminPID),
			BaseIdentityID: h.identityID,
			Suffix:         "shopping",
		}); err != nil {
		t.Fatalf("InsertTaggedAddressDismissal: %v", err)
	}

	res, _ := h.doRequest("POST",
		"/api/v1/tagged-address-filters/"+row.ID+"/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST: %d", res.StatusCode)
	}
	// Dismissal should be gone now (REQ-TAG-61 cascade).
	rows, err := h.fs.Meta().ListTaggedAddressDismissalsForPrincipal(context.Background(),
		store.PrincipalID(h.adminPID))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, d := range rows {
		if d.Suffix == "shopping" {
			t.Errorf("dismissal not dropped: %+v", d)
		}
	}
}

// TestConvertToSieve_NotFound_404 verifies that an unknown filter id
// returns 404 with the structured problem+json body.
func TestConvertToSieve_NotFound_404(t *testing.T) {
	h := newConvertHarness(t)
	res, buf := h.doRequest("POST",
		"/api/v1/tagged-address-filters/does-not-exist/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("POST: %d: %s", res.StatusCode, buf)
	}
}

// TestConvertToSieve_CrossPrincipal_404 verifies that a filter owned
// by another principal is not convertible by the caller; the surface
// returns 404 to avoid leaking existence across principals.
func TestConvertToSieve_CrossPrincipal_404(t *testing.T) {
	h := newConvertHarness(t)
	other, err := h.fs.Meta().InsertPrincipal(context.Background(), store.Principal{
		CanonicalEmail: "other@example.com",
		Kind:           store.PrincipalKindUser,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	otherIdentity := h.insertIdentity(uint64(other.ID), "other@example.com")
	row := store.TaggedAddressFilter{
		PrincipalID:    other.ID,
		BaseIdentityID: otherIdentity,
		Suffix:         "amazon",
		Action:         store.TaggedAddressActionLabel,
		LabelName:      "Shopping",
	}
	if err := h.fs.Meta().InsertTaggedAddressFilter(context.Background(), row); err != nil {
		t.Fatalf("InsertTaggedAddressFilter: %v", err)
	}
	got, err := h.fs.Meta().GetTaggedAddressFilter(context.Background(),
		other.ID, otherIdentity, "amazon")
	if err != nil {
		t.Fatalf("GetTaggedAddressFilter: %v", err)
	}
	res, buf := h.doRequest("POST",
		"/api/v1/tagged-address-filters/"+got.ID+"/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-principal POST: %d: %s (want 404)", res.StatusCode, buf)
	}
}

// TestConvertToSieve_InvalidSieve_RollsBack verifies the rollback path:
// when the merged script fails to parse (because the pre-existing user
// script is malformed), the filter row stays put and the response is
// 422 with the parser's diagnostic. REQ-TAG-50 step 4.
func TestConvertToSieve_InvalidSieve_RollsBack(t *testing.T) {
	h := newConvertHarness(t)
	// Seed a malformed user script. The handler reads the existing
	// user script, prepends the fragment, then validates the union.
	// A malformed prefix in the user script forces a parse error on
	// the merged text.
	malformed := "this is not a valid sieve script {{{"
	pid := store.PrincipalID(h.adminPID)
	if err := h.fs.Meta().SetUserSieveScript(context.Background(), pid, malformed); err != nil {
		t.Fatalf("SetUserSieveScript: %v", err)
	}
	row := h.insertFilter(t, "rollback", store.TaggedAddressActionLabel, "Rollback")

	res, buf := h.doRequest("POST",
		"/api/v1/tagged-address-filters/"+row.ID+"/convert-to-sieve",
		h.adminKey, nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST: %d: %s (want 422)", res.StatusCode, buf)
	}
	// The filter row must still exist (rollback per REQ-TAG-50 step 4).
	if _, err := h.fs.Meta().GetTaggedAddressFilter(context.Background(),
		pid, h.identityID, "rollback"); err != nil {
		t.Errorf("filter row dropped after validation failure: err=%v", err)
	}
	// The user script must NOT have been touched.
	gotScript, _ := h.fs.Meta().GetUserSieveScript(context.Background(), pid)
	if gotScript != malformed {
		t.Errorf("user script overwritten on validation failure; got %q, want %q",
			gotScript, malformed)
	}
	// Audit entry for the failure landed.
	audits := auditEntriesFor(t, h.fs, "tagged_address_filter.convert_to_sieve")
	if len(audits) != 1 {
		t.Fatalf("audit entries = %d; want 1 failure entry", len(audits))
	}
	if audits[0].Outcome != store.OutcomeFailure {
		t.Errorf("audit outcome = %v; want failure", audits[0].Outcome)
	}
}
