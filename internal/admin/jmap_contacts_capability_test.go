package admin

// jmap_contacts_capability_test.go is a regression test for the bug
// introduced when jmapcontacts.Register was absent from composeAdminAndUI:
// CapabilityJMAPContacts ("urn:ietf:params:jmap:contacts") was silently
// missing from the JMAP session descriptor, causing suite contacts actions
// to fail with "Contacts unavailable".
//
// Commit fa2d153 ("server: register JMAP Contacts capability at startup")
// added the two lines that fix this:
//
//   import jmapcontacts "github.com/hanshuebner/herold/internal/protojmap/contacts"
//   jmapcontacts.Register(jmapSrv.Registry(), st, logger.With("subsystem", "jmap-contacts"), clk)
//
// This test catches a silent regression if those lines are removed.
//
// REQ-PROTO-55.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestContactsCapabilityInSessionDescriptor verifies that the JMAP session
// descriptor returned by GET /.well-known/jmap:
//  1. Contains "urn:ietf:params:jmap:contacts" in the top-level capabilities
//     map (so clients know the server supports JMAP Contacts).
//  2. Contains "urn:ietf:params:jmap:contacts" in primaryAccounts (so clients
//     obtain the account id to use for AddressBook/* and Contact/* calls).
//  3. The per-account capability descriptor includes the binding-draft
//     tunables (maxAddressBooksPerAccount, maxContactsPerAddressBook,
//     maxSizePerContactBlob).
//
// The test boots a full server via startTestServer + bootstrap, then hits
// the same /.well-known/jmap endpoint the suite SPA uses at startup.
func TestContactsCapabilityInSessionDescriptor(t *testing.T) {
	_, addrs, done, cancel := startTestServer(t)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("server did not shut down within grace window")
		}
	})

	publicAddr := addrs["public"]
	adminAddr := addrs["admin"]
	if publicAddr == "" {
		t.Fatalf("public listener not bound; addrs=%+v", addrs)
	}
	if adminAddr == "" {
		t.Fatalf("admin listener not bound; addrs=%+v", addrs)
	}

	// Bootstrap a principal so we can authenticate against the session endpoint.
	b, _ := json.Marshal(map[string]any{
		"email":        "contacts-cap-test@example.com",
		"display_name": "Contacts Cap Test",
	})
	resp, err := http.Post("http://"+adminAddr+"/api/v1/bootstrap",
		"application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("bootstrap POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap: status=%d body=%s", resp.StatusCode, raw)
	}
	var boot struct {
		InitialAPIKey string `json:"initial_api_key"`
	}
	if err := json.Unmarshal(raw, &boot); err != nil {
		t.Fatalf("bootstrap unmarshal: %v body=%s", err, raw)
	}
	if boot.InitialAPIKey == "" {
		t.Fatalf("bootstrap returned empty initial_api_key; body=%s", raw)
	}
	apiKey := boot.InitialAPIKey

	// Fetch the JMAP session descriptor with the full raw structure so we can
	// inspect all three axes: capabilities, primaryAccounts, and the per-account
	// capability descriptor nested inside accounts.<id>.accountCapabilities.
	req, _ := http.NewRequest(http.MethodGet, "http://"+publicAddr+"/.well-known/jmap", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	sessResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /.well-known/jmap: %v", err)
	}
	defer sessResp.Body.Close()
	sessRaw, _ := io.ReadAll(sessResp.Body)
	if sessResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /.well-known/jmap: status=%d body=%s", sessResp.StatusCode, sessRaw)
	}

	// Parse into a flexible shape that mirrors the RFC 8620 §2 session object
	// without importing internal packages (keeping the test black-box).
	var sess struct {
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		PrimaryAccounts map[string]string          `json:"primaryAccounts"`
		Accounts        map[string]struct {
			AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(sessRaw, &sess); err != nil {
		t.Fatalf("session descriptor unmarshal: %v body=%s", err, sessRaw)
	}

	const capContacts = "urn:ietf:params:jmap:contacts"

	// 1. The top-level capabilities map must contain the contacts capability.
	if _, ok := sess.Capabilities[capContacts]; !ok {
		t.Errorf("session capabilities missing %q; capabilities keys: %v; full body: %s",
			capContacts, capabilityKeys(sess.Capabilities), sessRaw)
	}

	// 2. primaryAccounts must have an entry for the contacts capability.
	accountID, ok := sess.PrimaryAccounts[capContacts]
	if !ok || accountID == "" {
		t.Errorf("primaryAccounts missing entry for %q; primaryAccounts=%v; full body: %s",
			capContacts, sess.PrimaryAccounts, sessRaw)
	}

	// 3. The per-account capability descriptor must include the three binding-draft
	// limits advertised by contacts.DefaultLimits(): maxAddressBooksPerAccount,
	// maxContactsPerAddressBook, maxSizePerContactBlob.
	if accountID != "" {
		acct, ok := sess.Accounts[accountID]
		if !ok {
			t.Errorf("accounts map has no entry for id %q derived from primaryAccounts[%q]",
				accountID, capContacts)
		} else {
			acctCap, ok := acct.AccountCapabilities[capContacts]
			if !ok {
				t.Errorf("accounts[%q].accountCapabilities missing %q", accountID, capContacts)
			} else {
				var limits struct {
					MaxAddressBooksPerAccount int `json:"maxAddressBooksPerAccount"`
					MaxContactsPerAddressBook int `json:"maxContactsPerAddressBook"`
					MaxSizePerContactBlob     int `json:"maxSizePerContactBlob"`
				}
				if err := json.Unmarshal(acctCap, &limits); err != nil {
					t.Errorf("unmarshal contacts account capability: %v; raw=%s", err, acctCap)
				} else {
					if limits.MaxAddressBooksPerAccount <= 0 {
						t.Errorf("maxAddressBooksPerAccount = %d, want > 0", limits.MaxAddressBooksPerAccount)
					}
					if limits.MaxContactsPerAddressBook <= 0 {
						t.Errorf("maxContactsPerAddressBook = %d, want > 0", limits.MaxContactsPerAddressBook)
					}
					if limits.MaxSizePerContactBlob <= 0 {
						t.Errorf("maxSizePerContactBlob = %d, want > 0", limits.MaxSizePerContactBlob)
					}
				}
			}
		}
	}
}

// capabilityKeys extracts the keys from a capability map for use in
// diagnostic messages. Kept local to avoid a dependency on sort.
func capabilityKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
