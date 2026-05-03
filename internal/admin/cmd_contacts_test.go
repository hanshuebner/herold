package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// openContactTestStore opens the SQLite store written by
// minimalConfigFixture so tests can seed data before invoking the CLI.
func openContactTestStore(t *testing.T, cfgPath string) store.Store {
	t.Helper()
	cfg, err := sysconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("sysconfig.Load: %v", err)
	}
	ctx := context.Background()
	clk := clock.NewReal()
	st, err := openStore(ctx, cfg, discardLogger(), clk)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// seedContactFixture inserts a principal and a default address book,
// returning both.
func seedContactFixture(t *testing.T, st store.Store, email string) (store.Principal, store.AddressBook) {
	t.Helper()
	ctx := context.Background()
	p, err := st.Meta().InsertPrincipal(ctx, store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: email,
	})
	if err != nil {
		t.Fatalf("InsertPrincipal(%s): %v", email, err)
	}
	abid, err := st.Meta().InsertAddressBook(ctx, store.AddressBook{
		PrincipalID:  p.ID,
		Name:         "default",
		IsDefault:    true,
		IsSubscribed: true,
	})
	if err != nil {
		t.Fatalf("InsertAddressBook: %v", err)
	}
	ab, err := st.Meta().GetAddressBook(ctx, abid)
	if err != nil {
		t.Fatalf("GetAddressBook: %v", err)
	}
	return p, ab
}

// insertContact inserts a single contact into the store. A minimal
// JSContact JSON blob is required by the NOT NULL constraint on the
// real SQLite/Postgres backend.
func insertContact(t *testing.T, st store.Store, p store.Principal, ab store.AddressBook, uid, displayName, email string) store.ContactID {
	t.Helper()
	ctx := context.Background()
	jscontact := []byte(fmt.Sprintf(`{"@type":"Card","uid":%q,"fullName":{"value":%q}}`, uid, displayName))
	id, err := st.Meta().InsertContact(ctx, store.Contact{
		AddressBookID: ab.ID,
		PrincipalID:   p.ID,
		UID:           uid,
		DisplayName:   displayName,
		PrimaryEmail:  email,
		SearchBlob:    strings.ToLower(displayName + " " + email),
		JSContactJSON: jscontact,
	})
	if err != nil {
		t.Fatalf("InsertContact(%s): %v", uid, err)
	}
	return id
}

// runContacts runs the cobra root against the given system config path
// and returns stdout + the execution error.
func runContacts(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errBuf)
	all := append([]string{"--system-config", cfgPath}, args...)
	root.SetArgs(all)
	root.SetContext(context.Background())
	err := root.Execute()
	return out.String(), err
}

// TestCLIContactsList_TwoContacts is the happy-path test: two contacts
// exist for a principal and both appear in the JSON output.
func TestCLIContactsList_TwoContacts(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openContactTestStore(t, cfgPath)
	p, ab := seedContactFixture(t, st, "alice@test.local")
	insertContact(t, st, p, ab, "uid-1", "Alice Example", "alice@example.com")
	insertContact(t, st, p, ab, "uid-2", "Bob Builder", "bob@example.com")

	out, err := runContacts(t, cfgPath, "contacts", "list", "alice@test.local", "--json")
	if err != nil {
		t.Fatalf("contacts list: %v", err)
	}

	var contacts []contactJSON
	if decErr := json.Unmarshal([]byte(out), &contacts); decErr != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", decErr, out)
	}
	if len(contacts) != 2 {
		t.Fatalf("expected 2 contacts, got %d: %s", len(contacts), out)
	}
	names := map[string]bool{}
	for _, c := range contacts {
		names[c.DisplayName] = true
	}
	if !names["Alice Example"] || !names["Bob Builder"] {
		t.Fatalf("expected Alice and Bob in output: %v", names)
	}
}

// TestCLIContactsList_Empty verifies an empty JSON array for a
// principal that has an address book but no contacts.
func TestCLIContactsList_Empty(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openContactTestStore(t, cfgPath)
	seedContactFixture(t, st, "empty@test.local")

	out, err := runContacts(t, cfgPath, "contacts", "list", "empty@test.local", "--json")
	if err != nil {
		t.Fatalf("contacts list (empty): %v", err)
	}

	var contacts []contactJSON
	if decErr := json.Unmarshal([]byte(out), &contacts); decErr != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", decErr, out)
	}
	if len(contacts) != 0 {
		t.Fatalf("expected empty slice, got %d contacts", len(contacts))
	}
}

// TestCLIContactsList_BookFilter verifies that --book narrows the
// result to the named address book.
func TestCLIContactsList_BookFilter(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openContactTestStore(t, cfgPath)
	p, ab1 := seedContactFixture(t, st, "bookuser@test.local")

	// Add a second address book.
	ctx := context.Background()
	ab2id, err := st.Meta().InsertAddressBook(ctx, store.AddressBook{
		PrincipalID:  p.ID,
		Name:         "work",
		IsSubscribed: true,
	})
	if err != nil {
		t.Fatalf("InsertAddressBook(work): %v", err)
	}
	ab2, err := st.Meta().GetAddressBook(ctx, ab2id)
	if err != nil {
		t.Fatalf("GetAddressBook: %v", err)
	}

	insertContact(t, st, p, ab1, "personal-1", "Personal Contact", "personal@example.com")
	insertContact(t, st, p, ab2, "work-1", "Work Contact", "work@example.com")

	// --book by name: only the work contact.
	out, err := runContacts(t, cfgPath, "contacts", "list", "bookuser@test.local", "--book", "work", "--json")
	if err != nil {
		t.Fatalf("contacts list --book name: %v", err)
	}
	var c1 []contactJSON
	if decErr := json.Unmarshal([]byte(out), &c1); decErr != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", decErr, out)
	}
	if len(c1) != 1 || c1[0].DisplayName != "Work Contact" {
		t.Fatalf("--book by name: got %v: %s", c1, out)
	}

	// --book by numeric ID: same result.
	bookIDStr := fmt.Sprintf("%d", ab2.ID)
	out, err = runContacts(t, cfgPath, "contacts", "list", "bookuser@test.local", "--book", bookIDStr, "--json")
	if err != nil {
		t.Fatalf("contacts list --book id: %v", err)
	}
	var c2 []contactJSON
	if decErr := json.Unmarshal([]byte(out), &c2); decErr != nil {
		t.Fatalf("parse JSON (id): %v\noutput: %s", decErr, out)
	}
	if len(c2) != 1 || c2[0].DisplayName != "Work Contact" {
		t.Fatalf("--book by id: got %v: %s", c2, out)
	}
}

// TestCLIContactsList_JSONFields checks that the JSON output carries
// the expected fields and that jscontact_json is absent by default.
func TestCLIContactsList_JSONFields(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)
	st := openContactTestStore(t, cfgPath)
	p, ab := seedContactFixture(t, st, "jsonuser@test.local")
	insertContact(t, st, p, ab, "uid-j1", "JSON Test", "jtest@example.com")

	out, err := runContacts(t, cfgPath, "contacts", "list", "jsonuser@test.local", "--json")
	if err != nil {
		t.Fatalf("contacts list --json: %v", err)
	}

	var raw []map[string]any
	if decErr := json.Unmarshal([]byte(out), &raw); decErr != nil {
		t.Fatalf("parse JSON: %v\noutput: %s", decErr, out)
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(raw))
	}
	c := raw[0]
	for _, key := range []string{"id", "address_book_id", "uid", "display_name", "primary_email", "updated_at_us"} {
		if _, ok := c[key]; !ok {
			t.Errorf("missing key %q in JSON output", key)
		}
	}
	// jscontact_json must not appear in the default view.
	if _, ok := c["jscontact_json"]; ok {
		t.Errorf("jscontact_json should be absent in default --json view")
	}
	if c["display_name"] != "JSON Test" {
		t.Errorf("display_name: got %v, want JSON Test", c["display_name"])
	}
}

// TestCLIContactsList_UnknownPrincipal checks that an unknown principal
// reference returns a clear error.
func TestCLIContactsList_UnknownPrincipal(t *testing.T) {
	t.Parallel()
	cfgPath, _ := minimalConfigFixture(t)

	_, err := runContacts(t, cfgPath, "contacts", "list", "ghost@test.local")
	if err == nil {
		t.Fatal("expected error for unknown principal, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got: %v", err)
	}
}
