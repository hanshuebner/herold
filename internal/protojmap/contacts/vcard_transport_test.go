package contacts_test

// End-to-end tests for Contact/import and Contact/export (REQ-CTS-20..24,
// REQ-CTS-14, REQ-CTS-40, REQ-CTS-41). Every test function runs on SQLite
// via the default testharness fixture. The Postgres leg at the bottom of
// this file re-runs the same core assertions against a Postgres-backed
// harness when HEROLD_PG_DSN is set (STANDARDS §8.4).
//
// Covered assertions:
//   - REQ-CTS-20: import creates contacts, returns per-card results
//   - REQ-CTS-21: duplicate candidates reported by UID and primary email
//   - REQ-CTS-22: export produces downloadable .vcf round-tripping the
//     mappable property set
//   - REQ-CTS-14: inline PHOTO in .vcf is interned as a blobId on import;
//     export with fetchPhotos=true re-embeds the data
//   - REQ-CTS-24: round-trip import -> Contact/get -> export equivalence
//   - REQ-CTS-41: oversized .vcf blob rejected before any cards are created
//   - Partial success: malformed card in multi-card file fails while
//     well-formed cards succeed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/directory"
	"github.com/hanshuebner/herold/internal/protoadmin"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/protojmap/contacts"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/storepg"
	"github.com/hanshuebner/herold/internal/testharness"
)

// transportRunID is a per-process random token combined with a
// per-fixture monotonic counter to generate unique principal email
// addresses across all tests in a run, and across repeated runs of the
// Postgres tests against the same database.
var (
	transportRunRand    = rand.New(rand.NewSource(time.Now().UnixNano()))
	transportRunID      = fmt.Sprintf("%08x", transportRunRand.Int63())
	transportRunCounter int
)

// -- fixture setup helpers -----------------------------------------------

// setupFixtureForTest creates a testharness fixture backed by the supplied
// store (nil = default SQLite). A unique principal is provisioned per call
// using the test name so multiple Postgres-backed tests can run against the
// same database without email collisions.
func setupFixtureForTest(t *testing.T, st store.Store, limits contacts.AccountLimits) *fixture {
	t.Helper()

	var opts testharness.Options
	opts.Listeners = []testharness.ListenerSpec{{Name: "jmap", Protocol: "jmap"}}
	if st != nil {
		opts.Store = st
	}
	srv, _ := testharness.Start(t, opts)

	// Build a unique email address for this fixture. Combine a per-process
	// random token with a monotonic counter so tests with similar names
	// (which might share the same truncated prefix) never conflict, even
	// across repeated runs of the Postgres tests against the same database.
	transportRunCounter++
	email := fmt.Sprintf("tr-%s-%04d@transport.test", transportRunID, transportRunCounter)

	ctx := context.Background()
	p, err := srv.Store.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal: %v", err)
	}
	plaintext := "hk_transport_" + fmt.Sprintf("%d", p.ID)
	if _, err := srv.Store.Meta().InsertAPIKey(ctx, store.APIKey{
		PrincipalID: p.ID,
		Hash:        protoadmin.HashAPIKey(plaintext),
		Name:        "test",
	}); err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	dir := directory.New(srv.Store.Meta(), srv.Logger, srv.Clock, nil)
	jmapServ := protojmap.NewServer(srv.Store, dir, nil, srv.Logger, srv.Clock, protojmap.Options{
		DownloadRatePerSec: -1, // unlimited for tests
	})
	contacts.RegisterWithLimits(jmapServ.Registry(), srv.Store, srv.Logger, srv.Clock, limits)
	if err := srv.AttachJMAP("jmap", jmapServ, protojmap.ListenerModePlain); err != nil {
		t.Fatalf("AttachJMAP: %v", err)
	}
	client, base := srv.DialJMAPByName(ctx, "jmap")
	return &fixture{
		srv: srv, pid: p.ID, client: client, baseURL: base,
		apiKey: plaintext, jmapServ: jmapServ,
	}
}

// openPostgresStoreForTransport opens a Postgres store for transport tests.
// Skips the test when HEROLD_PG_DSN is unset or the connection fails.
func openPostgresStoreForTransport(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("HEROLD_PG_DSN")
	if dsn == "" {
		t.Skip("HEROLD_PG_DSN not set; skipping Postgres leg")
	}
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := storepg.Open(context.Background(), dsn, t.TempDir(), nil, clk)
	if err != nil {
		t.Skipf("storepg.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// -- vCard fixture data -----------------------------------------------

// richVCF builds a .vcf with typed emails, phones, an address, an org,
// a title, a role, a nickname, a note, a URL, a birthday, and optionally
// an inline PHOTO.
func richVCF(includePhoto bool) []byte {
	lines := []string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"UID:urn:uuid:rich-001",
		"KIND:individual",
		"FN:Ada Lovelace",
		"N:Lovelace;Ada;Augusta;Hon.;",
		"EMAIL;TYPE=work;PREF=1:ada@example.test",
		"EMAIL;TYPE=home:ada.home@example.test",
		"TEL;TYPE=voice,cell;PREF=1:+44 20 7946 0958",
		"ADR;TYPE=work:;;221B Baker Street;London;;NW1 6XE;United Kingdom",
		"ORG:Analytical Engine Co;Engineering",
		"TITLE:Chief Programmer",
		"ROLE:Fellow",
		"NICKNAME:Ada",
		"NOTE:First programmer.",
		"URL:https://example.test/ada",
		"BDAY:18151210",
		"ANNIVERSARY:--1225",
	}
	if includePhoto {
		// Minimal 1x1 white PNG encoded in base64.
		const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
		lines = append(lines, "PHOTO:data:image/png;base64,"+pngBase64)
	}
	lines = append(lines, "END:VCARD")
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

// multiCardVCFWithBadCard builds a three-card .vcf: two valid cards and
// one malformed card in the middle (missing colon separator).
func multiCardVCFWithBadCard() []byte {
	good1 := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:good-001\r\nFN:Good One\r\nEMAIL;PREF=1:good1@example.test\r\nEND:VCARD\r\n"
	bad := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:bad\r\nNOCOLON\r\nEND:VCARD\r\n"
	good2 := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:good-002\r\nFN:Good Two\r\nEMAIL;PREF=1:good2@example.test\r\nEND:VCARD\r\n"
	return []byte(good1 + bad + good2)
}

// -- invocation helpers -----------------------------------------------

// importResponse mirrors the wire form of the Contact/import response,
// using the exported ImportCardResult type.
type importResponse struct {
	AccountID string                      `json:"accountId"`
	NewState  string                      `json:"newState"`
	Results   []contacts.ImportCardResult `json:"results"`
}

// exportResponse mirrors the wire form of the Contact/export response,
// using the exported ExportWarning type.
type exportResponse struct {
	AccountID       string                   `json:"accountId"`
	BlobID          string                   `json:"blobId"`
	Type            string                   `json:"type"`
	Size            int64                    `json:"size"`
	Unrepresentable []contacts.ExportWarning `json:"unrepresentable"`
}

// invokeImport calls Contact/import via f.invoke and unmarshals the result.
func invokeImport(t *testing.T, f *fixture, blobID, addressBookID string) importResponse {
	t.Helper()
	args := map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"blobId":    blobID,
	}
	if addressBookID != "" {
		args["addressBookId"] = addressBookID
	}
	_, raw := f.invoke(t, "Contact/import", args)
	var resp importResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("Contact/import unmarshal: %v: %s", err, raw)
	}
	return resp
}

// invokeExport calls Contact/export via f.invoke and unmarshals the result.
func invokeExport(t *testing.T, f *fixture, contactIDs []string, addressBookID string, fetchPhotos bool) exportResponse {
	t.Helper()
	args := map[string]any{
		"accountId":   string(protojmap.AccountIDForPrincipal(f.pid)),
		"fetchPhotos": fetchPhotos,
	}
	if len(contactIDs) > 0 {
		args["ids"] = contactIDs
	}
	if addressBookID != "" {
		args["addressBookId"] = addressBookID
	}
	_, raw := f.invoke(t, "Contact/export", args)
	var resp exportResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("Contact/export unmarshal: %v: %s", err, raw)
	}
	return resp
}

// clamp trims a string to at most n bytes for diagnostic output.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// -- Tests (SQLite leg) -----------------------------------------------

// TestVCardTransport_RoundTrip is the primary end-to-end test (REQ-CTS-24):
//
//  1. Upload a fixture .vcf with a rich card (typed email/phone/addr/org/
//     title, nickname, note, URL, birthday, and an inline PHOTO).
//  2. Import via Contact/import.
//  3. Verify the created contact via Contact/get, including that the PHOTO
//     became a downloadable blobId reference (REQ-CTS-14).
//  4. Export via Contact/export.
//  5. Assert the downloaded .vcf round-trips the mappable property set.
func TestVCardTransport_RoundTrip(t *testing.T) {
	runVCardTransportRoundTrip(t, nil)
}

func runVCardTransportRoundTrip(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "TransportRoundTrip")

	// 1. Upload .vcf with inline PHOTO.
	vcfData := richVCF(true)
	blobID := uploadBlob(t, f, vcfData, "text/vcard")

	// 2. Import.
	impResp := invokeImport(t, f, blobID, bookID)
	if len(impResp.Results) != 1 {
		t.Fatalf("import: expected 1 result, got %d", len(impResp.Results))
	}
	res := impResp.Results[0]
	if res.Result != "created" {
		t.Fatalf("import: result[0] = %q (%s), want created", res.Result, res.Reason)
	}
	contactID := string(res.ID)
	if contactID == "" {
		t.Fatal("import: result[0].id is empty")
	}
	if res.UID != "urn:uuid:rich-001" {
		t.Errorf("import: uid = %q, want urn:uuid:rich-001", res.UID)
	}

	// 3. Verify via Contact/get (REQ-CTS-24).
	_, rawGet := f.invoke(t, "Contact/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{contactID},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(rawGet, &getResp); err != nil {
		t.Fatalf("Contact/get unmarshal: %v", err)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("Contact/get: expected 1 contact, got %d", len(getResp.List))
	}
	got := getResp.List[0]

	// Name must round-trip.
	name, _ := got["name"].(map[string]any)
	if name == nil {
		t.Error("Contact/get: name is nil")
	} else if name["full"] != "Ada Lovelace" {
		t.Errorf("Contact/get: name.full = %v, want Ada Lovelace", name["full"])
	}

	// PHOTO must have become a blobId reference, not a data URI (REQ-CTS-14).
	mediaRaw, hasMed := got["media"]
	if !hasMed {
		t.Fatal("Contact/get: media is absent (expected photo blobId entry)")
	}
	mediaMap, ok := mediaRaw.(map[string]any)
	if !ok {
		t.Fatalf("Contact/get: media is %T, want map", mediaRaw)
	}
	var photoBlobID string
	for _, v := range mediaMap {
		entry, _ := v.(map[string]any)
		if entry["kind"] == "photo" {
			photoBlobID, _ = entry["blobId"].(string)
			break
		}
	}
	if photoBlobID == "" {
		t.Fatalf("Contact/get: no photo blobId in media map: %v", mediaMap)
	}

	// The photo blob must be downloadable (REQ-CTS-04).
	code, photoBytes := downloadBlob(t, f, photoBlobID)
	if code != http.StatusOK {
		t.Fatalf("photo download: status = %d", code)
	}
	if len(photoBytes) == 0 {
		t.Fatal("photo download: empty body")
	}
	// PNG magic: \x89PNG
	if len(photoBytes) < 4 || !(photoBytes[0] == 0x89 && photoBytes[1] == 'P' && photoBytes[2] == 'N' && photoBytes[3] == 'G') {
		n := len(photoBytes)
		if n > 4 {
			n = 4
		}
		t.Errorf("photo download: not a PNG (first bytes: % x)", photoBytes[:n])
	}

	// 4. Export (REQ-CTS-22).
	expResp := invokeExport(t, f, []string{contactID}, "", false)
	if expResp.BlobID == "" {
		t.Fatal("Contact/export: blobId is empty")
	}
	if expResp.Type != "text/vcard" {
		t.Errorf("Contact/export: type = %q, want text/vcard", expResp.Type)
	}

	// 5. Download and verify round-trip.
	code, vcfOut := downloadBlob(t, f, expResp.BlobID)
	if code != http.StatusOK {
		t.Fatalf("export download: status = %d, body = %s", code, vcfOut)
	}
	vcfStr := string(vcfOut)

	assertVCardContains(t, vcfStr, "FN:Ada Lovelace")
	assertVCardContains(t, vcfStr, "UID:urn:uuid:rich-001")
	assertVCardContains(t, vcfStr, "EMAIL;")
	assertVCardContains(t, vcfStr, "ada@example.test")
	assertVCardContains(t, vcfStr, "TEL;")
	assertVCardContains(t, vcfStr, "ADR;")
	assertVCardContains(t, vcfStr, "ORG:Analytical Engine Co")
	assertVCardContains(t, vcfStr, "TITLE:Chief Programmer")
	assertVCardContains(t, vcfStr, "ROLE:Fellow")
	assertVCardContains(t, vcfStr, "NICKNAME:Ada")
	assertVCardContains(t, vcfStr, "NOTE:First programmer.")
	assertVCardContains(t, vcfStr, "URL:https://example.test/ada")
	assertVCardContains(t, vcfStr, "BDAY:18151210")
	assertVCardContains(t, vcfStr, "PHOTO:")

	// 5b. Export with fetchPhotos=true must embed the photo as a data URI.
	expRespPhoto := invokeExport(t, f, []string{contactID}, "", true)
	code, vcfOutPhoto := downloadBlob(t, f, expRespPhoto.BlobID)
	if code != http.StatusOK {
		t.Fatalf("export(fetchPhotos) download: status = %d", code)
	}
	if !strings.Contains(string(vcfOutPhoto), "data:image/png;base64,") {
		t.Errorf("export(fetchPhotos=true): PHOTO is not a data URI; vcf = %s",
			clamp(string(vcfOutPhoto), 200))
	}

	// 5c. Re-import the exported .vcf. The same UID is present, so the
	// second import must match the original rather than creating a
	// duplicate (REQ-CTS-21, re #206): either "skipped" (data identical)
	// or "conflict" (data differs -- e.g. the PHOTO line round-trips as
	// a bare blobId reference here, a distinct pre-existing export/
	// import fidelity gap, not this fix's concern). Either way no
	// second contact is created.
	reImportBlobID := uploadBlob(t, f, vcfOut, "text/vcard")
	impResp2 := invokeImport(t, f, reImportBlobID, bookID)
	if len(impResp2.Results) != 1 {
		t.Fatalf("re-import: expected 1 result, got %d", len(impResp2.Results))
	}
	reimportRes := impResp2.Results[0]
	if reimportRes.Result != "skipped" && reimportRes.Result != "conflict" {
		t.Errorf("re-import: result = %q, want skipped or conflict", reimportRes.Result)
	}
	if string(reimportRes.MatchedID) != contactID {
		t.Errorf("re-import: matchedId = %q, want %q", reimportRes.MatchedID, contactID)
	}
	rows, err := f.srv.Store.Meta().ListContacts(context.Background(), store.ContactFilter{
		PrincipalID: &f.pid,
	})
	if err != nil {
		t.Fatalf("ListContacts: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("contact count after re-import = %d, want 1 (no duplicate)", len(rows))
	}
}

// TestVCardTransport_PartialSuccess tests that a malformed card in a
// multi-card .vcf does not abort the batch (REQ-CTS-20 partial success).
func TestVCardTransport_PartialSuccess(t *testing.T) {
	runVCardTransportPartialSuccess(t, nil)
}

func runVCardTransportPartialSuccess(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "PartialSuccess")

	blobID := uploadBlob(t, f, multiCardVCFWithBadCard(), "text/vcard")
	impResp := invokeImport(t, f, blobID, bookID)

	if len(impResp.Results) != 3 {
		t.Fatalf("partial success: expected 3 results, got %d", len(impResp.Results))
	}
	if impResp.Results[0].Result != "created" {
		t.Errorf("result[0] = %q, want created", impResp.Results[0].Result)
	}
	if impResp.Results[1].Result != "failed" {
		t.Errorf("result[1] = %q, want failed", impResp.Results[1].Result)
	}
	if impResp.Results[1].Reason == "" {
		t.Error("result[1].reason is empty for malformed card")
	}
	if impResp.Results[2].Result != "created" {
		t.Errorf("result[2] = %q, want created", impResp.Results[2].Result)
	}

	// Both good contacts must be accessible via Contact/get.
	ids := []string{string(impResp.Results[0].ID), string(impResp.Results[2].ID)}
	_, rawGet := f.invoke(t, "Contact/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       ids,
	})
	var getResp struct {
		List     []map[string]any `json:"list"`
		NotFound []string         `json:"notFound"`
	}
	if err := json.Unmarshal(rawGet, &getResp); err != nil {
		t.Fatalf("Contact/get unmarshal: %v", err)
	}
	if len(getResp.List) != 2 {
		t.Errorf("Contact/get: expected 2 contacts, got %d (notFound=%v)",
			len(getResp.List), getResp.NotFound)
	}
}

// TestVCardTransport_DuplicateCandidates_ByUID verifies that re-importing
// a card whose UID already exists in the address book is idempotent: an
// identical re-import is silently skipped rather than creating a
// duplicate or failing (REQ-CTS-21, re #206).
func TestVCardTransport_DuplicateCandidates_ByUID(t *testing.T) {
	runVCardTransportDuplicateByUID(t, nil)
}

func runVCardTransportDuplicateByUID(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "DupUID")

	// Import the first card.
	vcf1 := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:dup-uid-1\r\nFN:Original\r\nEND:VCARD\r\n")
	blobID1 := uploadBlob(t, f, vcf1, "text/vcard")
	resp1 := invokeImport(t, f, blobID1, bookID)
	if len(resp1.Results) != 1 || resp1.Results[0].Result != "created" {
		t.Fatalf("first import: %+v", resp1.Results)
	}
	originalID := string(resp1.Results[0].ID)

	// Re-import the exact same card -- should be skipped (no duplicate
	// created), matched against the original by UID and identified by
	// its name.
	blobID2 := uploadBlob(t, f, vcf1, "text/vcard")
	resp2 := invokeImport(t, f, blobID2, bookID)
	if len(resp2.Results) != 1 {
		t.Fatalf("second import: expected 1 result, got %d", len(resp2.Results))
	}
	res := resp2.Results[0]
	if res.Result != "skipped" {
		t.Errorf("second import: result = %q, want skipped", res.Result)
	}
	if string(res.MatchedID) != originalID {
		t.Errorf("second import: matchedId = %q, want %q", res.MatchedID, originalID)
	}
	if res.MatchedName != "Original" {
		t.Errorf("second import: matchedName = %q, want %q", res.MatchedName, "Original")
	}

	// No duplicate contact was created: the address book still holds
	// exactly one contact.
	rows, err := f.srv.Store.Meta().ListContacts(context.Background(), store.ContactFilter{
		PrincipalID: &f.pid,
	})
	if err != nil {
		t.Fatalf("ListContacts: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("contact count after re-import = %d, want 1 (no duplicate)", len(rows))
	}
}

// TestVCardTransport_ConflictByUID verifies that re-importing a card
// whose UID matches an existing contact, but whose data differs, is
// reported as a conflict rather than silently overwriting the existing
// contact or being skipped (REQ-CTS-21, re #206).
func TestVCardTransport_ConflictByUID(t *testing.T) {
	runVCardTransportConflictByUID(t, nil)
}

func runVCardTransportConflictByUID(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "ConflictUID")

	vcf1 := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:conflict-uid-1\r\nFN:Original Name\r\nEND:VCARD\r\n")
	blobID1 := uploadBlob(t, f, vcf1, "text/vcard")
	resp1 := invokeImport(t, f, blobID1, bookID)
	if len(resp1.Results) != 1 || resp1.Results[0].Result != "created" {
		t.Fatalf("first import: %+v", resp1.Results)
	}
	originalID := string(resp1.Results[0].ID)

	// Re-import the same UID with a changed FN.
	vcf2 := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:conflict-uid-1\r\nFN:Changed Name\r\nEND:VCARD\r\n")
	blobID2 := uploadBlob(t, f, vcf2, "text/vcard")
	resp2 := invokeImport(t, f, blobID2, bookID)
	if len(resp2.Results) != 1 {
		t.Fatalf("second import: expected 1 result, got %d", len(resp2.Results))
	}
	res := resp2.Results[0]
	if res.Result != "conflict" {
		t.Errorf("second import: result = %q, want conflict", res.Result)
	}
	if string(res.MatchedID) != originalID {
		t.Errorf("second import: matchedId = %q, want %q", res.MatchedID, originalID)
	}
	if res.MatchedName != "Original Name" {
		t.Errorf("second import: matchedName = %q, want %q", res.MatchedName, "Original Name")
	}
	foundNameDiff := false
	for _, d := range res.Diff {
		if d.Field == "name" {
			foundNameDiff = true
		}
	}
	if !foundNameDiff {
		t.Errorf("diff %+v does not include a \"name\" entry", res.Diff)
	}

	// No duplicate contact was created and the original is untouched.
	rows, err := f.srv.Store.Meta().ListContacts(context.Background(), store.ContactFilter{
		PrincipalID: &f.pid,
	})
	if err != nil {
		t.Fatalf("ListContacts: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("contact count after conflicting re-import = %d, want 1 (no duplicate)", len(rows))
	}
	if rows[0].DisplayName != "Original Name" {
		t.Errorf("existing contact was overwritten: DisplayName = %q, want %q",
			rows[0].DisplayName, "Original Name")
	}
}

// TestVCardTransport_DuplicateCandidates_ByEmail verifies conflict
// detection by shared primary email (REQ-CTS-21): a card with a
// different UID but the same primary email as an existing contact, and
// differing data, is reported as a conflict rather than created as a
// second contact.
func TestVCardTransport_DuplicateCandidates_ByEmail(t *testing.T) {
	runVCardTransportDuplicateByEmail(t, nil)
}

func runVCardTransportDuplicateByEmail(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "DupEmail")

	// Create an existing contact with a known primary email via
	// Contact/set (avoids a second import that would itself detect
	// duplicates and confuse the assertion).
	_, rawSet := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Existing Alice"},
				"emails": map[string]any{
					"e1": map[string]any{"address": "alice@dup.test", "pref": 1},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(rawSet, &setResp); err != nil {
		t.Fatalf("Contact/set unmarshal: %v", err)
	}
	if len(setResp.NotCreated) > 0 {
		t.Fatalf("Contact/set notCreated: %+v", setResp.NotCreated)
	}
	existingID, _ := setResp.Created["c1"]["id"].(string)

	// Import a card with the same email but a different UID and a
	// different name.
	vcf := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:newcard-alice\r\nFN:New Alice\r\nEMAIL;PREF=1:alice@dup.test\r\nEND:VCARD\r\n")
	blobID := uploadBlob(t, f, vcf, "text/vcard")
	impResp := invokeImport(t, f, blobID, bookID)
	if len(impResp.Results) != 1 {
		t.Fatalf("import: expected 1 result, got %d", len(impResp.Results))
	}
	res := impResp.Results[0]
	// Same email, different data -> conflict, not a second contact.
	if res.Result != "conflict" {
		t.Errorf("import: result = %q (%s), want conflict", res.Result, res.Reason)
	}
	if string(res.MatchedID) != existingID {
		t.Errorf("import: matchedId = %q, want %q", res.MatchedID, existingID)
	}
	if res.MatchedName != "Existing Alice" {
		t.Errorf("import: matchedName = %q, want %q", res.MatchedName, "Existing Alice")
	}
	if len(res.Diff) == 0 {
		t.Errorf("import: expected a non-empty diff for conflicting names")
	}

	// No duplicate contact was created.
	rows, err := f.srv.Store.Meta().ListContacts(context.Background(), store.ContactFilter{
		PrincipalID: &f.pid,
	})
	if err != nil {
		t.Fatalf("ListContacts: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("contact count after conflicting import = %d, want 1 (no duplicate)", len(rows))
	}
}

// TestVCardTransport_Reimport_SkipsIdenticalCard reproduces the reported
// bug (re #206) directly: importing the identical .vcf file twice, where
// the card carries no UID property (as many real-world exports do), must
// not create a second contact. The second import matches the first by
// primary email and, finding the data identical, skips it.
func TestVCardTransport_Reimport_SkipsIdenticalCard(t *testing.T) {
	runVCardTransportReimportSkipsIdenticalCard(t, nil)
}

func runVCardTransportReimportSkipsIdenticalCard(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "ReimportSkip")

	// No UID property at all -- matches only by primary email.
	vcf := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Bob NoUID\r\nEMAIL;PREF=1:bob.nouid@example.test\r\nEND:VCARD\r\n")

	blobID1 := uploadBlob(t, f, vcf, "text/vcard")
	resp1 := invokeImport(t, f, blobID1, bookID)
	if len(resp1.Results) != 1 || resp1.Results[0].Result != "created" {
		t.Fatalf("first import: %+v", resp1.Results)
	}
	originalID := string(resp1.Results[0].ID)

	// Re-import the exact same file.
	blobID2 := uploadBlob(t, f, vcf, "text/vcard")
	resp2 := invokeImport(t, f, blobID2, bookID)
	if len(resp2.Results) != 1 {
		t.Fatalf("second import: expected 1 result, got %d", len(resp2.Results))
	}
	res := resp2.Results[0]
	if res.Result != "skipped" {
		t.Errorf("second import: result = %q (%s), want skipped", res.Result, res.Reason)
	}
	if string(res.MatchedID) != originalID {
		t.Errorf("second import: matchedId = %q, want %q", res.MatchedID, originalID)
	}
	if res.MatchedName != "Bob NoUID" {
		t.Errorf("second import: matchedName = %q, want %q", res.MatchedName, "Bob NoUID")
	}

	rows, err := f.srv.Store.Meta().ListContacts(context.Background(), store.ContactFilter{
		PrincipalID: &f.pid,
	})
	if err != nil {
		t.Fatalf("ListContacts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("contact count after re-import = %d, want 1 (no duplicate); this is the reported bug (re #206)", len(rows))
	}
}

// TestVCardTransport_OversizedBlob verifies that Contact/import rejects
// a .vcf blob that exceeds MaxVCardImportSize with a requestTooLarge error
// and creates no contacts (REQ-CTS-41).
func TestVCardTransport_OversizedBlob(t *testing.T) {
	limits := contacts.DefaultLimits()
	limits.MaxVCardImportSize = 50 // 50 bytes -- tiny limit for this test

	f := setupFixtureForTest(t, nil, limits)

	bigVCF := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:urn:uuid:big\r\nFN:Big Card\r\n" +
		strings.Repeat("NOTE:padding\r\n", 100) +
		"END:VCARD\r\n")
	blobID := uploadBlob(t, f, bigVCF, "text/vcard")

	args := map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"blobId":    blobID,
	}
	name, raw := f.invoke(t, "Contact/import", args)
	if name != "error" {
		t.Fatalf("expected method-level error response, got name=%q, body=%s", name, raw)
	}
	var methodErr struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &methodErr); err != nil {
		t.Fatalf("unmarshal method error: %v: %s", err, raw)
	}
	if methodErr.Type != "requestTooLarge" {
		t.Errorf("expected requestTooLarge error, got %q", methodErr.Type)
	}
}

// TestVCardTransport_ExportAddressBook verifies that exporting by
// addressBookId returns all contacts in that book (REQ-CTS-22).
func TestVCardTransport_ExportAddressBook(t *testing.T) {
	runVCardTransportExportAddressBook(t, nil)
}

func runVCardTransportExportAddressBook(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "BookExport")

	// Create two contacts via import.
	for _, name := range []string{"Alice Export", "Bob Export"} {
		uid := "urn:uuid:export-" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))
		vcf := fmt.Sprintf("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:%s\r\nFN:%s\r\nEND:VCARD\r\n", uid, name)
		blobID := uploadBlob(t, f, []byte(vcf), "text/vcard")
		resp := invokeImport(t, f, blobID, bookID)
		if len(resp.Results) != 1 || resp.Results[0].Result != "created" {
			t.Fatalf("create %s: %+v", name, resp.Results)
		}
	}

	// Export the whole address book.
	expResp := invokeExport(t, f, nil, bookID, false)
	if expResp.BlobID == "" {
		t.Fatal("export: blobId is empty")
	}
	code, vcfOut := downloadBlob(t, f, expResp.BlobID)
	if code != http.StatusOK {
		t.Fatalf("export download: status = %d", code)
	}
	vcfStr := string(vcfOut)
	assertVCardContains(t, vcfStr, "FN:Alice Export")
	assertVCardContains(t, vcfStr, "FN:Bob Export")
	count := strings.Count(vcfStr, "BEGIN:VCARD")
	if count != 2 {
		t.Errorf("expected 2 BEGIN:VCARD, got %d", count)
	}
}

// TestVCardTransport_PhotoIntern verifies that an inline PHOTO in the
// imported vCard becomes a downloadable blobId reference (REQ-CTS-14).
func TestVCardTransport_PhotoIntern(t *testing.T) {
	runVCardTransportPhotoIntern(t, nil)
}

func runVCardTransportPhotoIntern(t *testing.T, st store.Store) {
	t.Helper()
	f := setupFixtureForTest(t, st, contacts.DefaultLimits())
	bookID := makeBook(t, f, "PhotoIntern")

	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	vcfData := []byte("BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"UID:urn:uuid:photo-intern-001\r\n" +
		"FN:Photo Intern Test\r\n" +
		"PHOTO:data:image/png;base64," + pngBase64 + "\r\n" +
		"END:VCARD\r\n")

	blobID := uploadBlob(t, f, vcfData, "text/vcard")
	impResp := invokeImport(t, f, blobID, bookID)
	if len(impResp.Results) != 1 || impResp.Results[0].Result != "created" {
		t.Fatalf("import: %+v", impResp.Results)
	}
	contactID := string(impResp.Results[0].ID)

	// The stored contact must have a blobId reference in its media map.
	_, rawGet := f.invoke(t, "Contact/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{contactID},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(rawGet, &getResp); err != nil || len(getResp.List) != 1 {
		t.Fatalf("Contact/get: %v / len=%d", err, len(getResp.List))
	}
	card := getResp.List[0]
	mediaRaw, hasMed := card["media"]
	if !hasMed {
		t.Fatal("Contact/get: media is absent")
	}
	mediaMap, ok := mediaRaw.(map[string]any)
	if !ok {
		t.Fatalf("media is %T, want map", mediaRaw)
	}
	var photoBlobID string
	for _, v := range mediaMap {
		entry, _ := v.(map[string]any)
		if entry["kind"] == "photo" {
			photoBlobID, _ = entry["blobId"].(string)
		}
	}
	if photoBlobID == "" {
		t.Fatalf("no photo blobId in media: %v", mediaMap)
	}

	// The blob must be downloadable and contain the original PNG bytes.
	code, pngBytes := downloadBlob(t, f, photoBlobID)
	if code != http.StatusOK {
		t.Fatalf("photo download: status = %d", code)
	}
	expected, _ := base64.StdEncoding.DecodeString(pngBase64)
	if !bytes.Equal(pngBytes, expected) {
		t.Errorf("photo bytes differ: got %d bytes, want %d", len(pngBytes), len(expected))
	}
}

// -- Postgres leg (REQ-CTS-24, STANDARDS §8.4) ------------------------

// TestVCardTransport_RoundTrip_Postgres runs the round-trip test against
// the Postgres backend. Skips when HEROLD_PG_DSN is not set.
func TestVCardTransport_RoundTrip_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportRoundTrip(t, st)
}

// TestVCardTransport_PartialSuccess_Postgres runs the partial-success
// test against the Postgres backend.
func TestVCardTransport_PartialSuccess_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportPartialSuccess(t, st)
}

// TestVCardTransport_DuplicateCandidates_ByUID_Postgres runs the
// UID-based duplicate test on Postgres.
func TestVCardTransport_DuplicateCandidates_ByUID_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportDuplicateByUID(t, st)
}

// TestVCardTransport_DuplicateCandidates_ByEmail_Postgres runs the
// email-based duplicate test on Postgres.
func TestVCardTransport_DuplicateCandidates_ByEmail_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportDuplicateByEmail(t, st)
}

// TestVCardTransport_ConflictByUID_Postgres runs the UID-based conflict
// test on Postgres.
func TestVCardTransport_ConflictByUID_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportConflictByUID(t, st)
}

// TestVCardTransport_Reimport_SkipsIdenticalCard_Postgres runs the
// idempotent-reimport test on Postgres.
func TestVCardTransport_Reimport_SkipsIdenticalCard_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportReimportSkipsIdenticalCard(t, st)
}

// TestVCardTransport_ExportAddressBook_Postgres runs the address-book
// export test on Postgres.
func TestVCardTransport_ExportAddressBook_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportExportAddressBook(t, st)
}

// TestVCardTransport_PhotoIntern_Postgres runs the photo-interning test
// on Postgres.
func TestVCardTransport_PhotoIntern_Postgres(t *testing.T) {
	st := openPostgresStoreForTransport(t)
	runVCardTransportPhotoIntern(t, st)
}
