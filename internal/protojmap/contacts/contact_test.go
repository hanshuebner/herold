package contacts_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// makeBook creates an address book and returns its wire id. Tests reach
// directly through the Store rather than going through AddressBook/set
// for setup so the contact-side behaviour is the test's focus.
func makeBook(t *testing.T, f *fixture, name string) string {
	t.Helper()
	id, err := f.srv.Store.Meta().InsertAddressBook(context.Background(), store.AddressBook{
		PrincipalID:  f.pid,
		Name:         name,
		IsSubscribed: true,
		IsDefault:    true,
	})
	if err != nil {
		t.Fatalf("InsertAddressBook: %v", err)
	}
	// store ids are uint64; format as decimal to mirror the JMAP wire form.
	return decimal(uint64(id))
}

func decimal(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func TestContact_Get_RendersFullCard(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Personal")
	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Ada Lovelace"},
				"emails": map[string]any{
					"e1": map[string]any{"address": "ada@example.test", "pref": 1},
				},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("not created: %+v", setResp.NotCreated)
	}
	id := setResp.Created["c1"]["id"].(string)

	_, raw = f.invoke(t, "Contact/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{id},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &getResp); err != nil {
		t.Fatalf("unmarshal get: %v: %s", err, raw)
	}
	if len(getResp.List) != 1 {
		t.Fatalf("list = %+v (raw=%s)", getResp.List, raw)
	}
	got := getResp.List[0]
	if got["id"] != id {
		t.Errorf("id = %v, want %s", got["id"], id)
	}
	if got["addressBookId"] != bookID {
		t.Errorf("addressBookId = %v, want %s", got["addressBookId"], bookID)
	}
	if got["version"] != "1.0" {
		t.Errorf("version = %v, want 1.0", got["version"])
	}
	if got["uid"] == "" {
		t.Errorf("uid not minted: %+v", got)
	}
	name, _ := got["name"].(map[string]any)
	if name == nil || name["full"] != "Ada Lovelace" {
		t.Errorf("name = %+v, want full=Ada Lovelace", name)
	}
}

func TestContact_Set_Create_PopulatesDenormalisedColumns(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Personal")
	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name": map[string]any{
					"components": []any{
						map[string]any{"kind": "given", "value": "Ada"},
						map[string]any{"kind": "surname", "value": "Lovelace"},
					},
				},
				"emails":        map[string]any{"e": map[string]any{"address": "ada@example.test", "pref": 1}},
				"organizations": map[string]any{"o": map[string]any{"name": "Analytical Engine Co."}},
			},
		},
	})
	var setResp struct {
		Created    map[string]map[string]any `json:"created"`
		NotCreated map[string]any            `json:"notCreated"`
	}
	if err := json.Unmarshal(raw, &setResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(setResp.Created) != 1 {
		t.Fatalf("not created: %+v", setResp.NotCreated)
	}
	idStr := setResp.Created["c1"]["id"].(string)

	// Reach through the store to assert denormalised columns.
	var cid store.ContactID
	if _, err := parseUint64(&cid, idStr); err != nil {
		t.Fatalf("parse id: %v", err)
	}
	c, err := f.srv.Store.Meta().GetContact(context.Background(), cid)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.GivenName != "Ada" {
		t.Errorf("GivenName = %q", c.GivenName)
	}
	if c.Surname != "Lovelace" {
		t.Errorf("Surname = %q", c.Surname)
	}
	if c.PrimaryEmail != "ada@example.test" {
		t.Errorf("PrimaryEmail = %q", c.PrimaryEmail)
	}
	if c.OrgName != "Analytical Engine Co." {
		t.Errorf("OrgName = %q", c.OrgName)
	}
	if c.DisplayName == "" {
		t.Errorf("DisplayName empty: %+v", c)
	}
}

// parseUint64 helps tests parse a JMAP id back into a numeric store id.
func parseUint64(out *store.ContactID, s string) (int, error) {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	*out = store.ContactID(n)
	return len(s), nil
}

func TestContact_Set_Update_MergePatch_PreservesUntouchedFields(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Personal")
	// Initial create.
	_, raw := f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c1": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Ada Lovelace"},
				"emails":        map[string]any{"e": map[string]any{"address": "ada@example.test"}},
			},
		},
	})
	var setResp struct {
		Created map[string]map[string]any `json:"created"`
	}
	_ = json.Unmarshal(raw, &setResp)
	id := setResp.Created["c1"]["id"].(string)

	// Update with only "phones"; the existing emails / name should
	// survive the merge patch.
	_, raw = f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"update": map[string]any{
			id: map[string]any{
				"phones": map[string]any{"p": map[string]any{"number": "+1 555 1234"}},
			},
		},
	})
	var upResp struct {
		Updated    map[string]any `json:"updated"`
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(raw, &upResp); err != nil {
		t.Fatalf("unmarshal update: %v: %s", err, raw)
	}
	if _, ok := upResp.Updated[id]; !ok {
		t.Fatalf("update failed: %+v", upResp.NotUpdated)
	}

	_, raw = f.invoke(t, "Contact/get", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"ids":       []string{id},
	})
	var getResp struct {
		List []map[string]any `json:"list"`
	}
	_ = json.Unmarshal(raw, &getResp)
	got := getResp.List[0]
	emails, _ := got["emails"].(map[string]any)
	if emails["e"] == nil {
		t.Errorf("untouched emails lost: %+v", got)
	}
	phones, _ := got["phones"].(map[string]any)
	if phones["p"] == nil {
		t.Errorf("phones not added: %+v", got)
	}
	name, _ := got["name"].(map[string]any)
	if name["full"] != "Ada Lovelace" {
		t.Errorf("untouched name lost: %+v", got)
	}
}

func TestContact_Query_TextSubstring(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Personal")
	for i, name := range []string{"Ada Lovelace", "Bob Smith", "Carol Jones"} {
		key := "c" + string(rune('0'+i))
		_, raw := f.invoke(t, "Contact/set", map[string]any{
			"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
			"create": map[string]any{
				key: map[string]any{
					"version":       "1.0",
					"addressBookId": bookID,
					"name":          map[string]any{"full": name},
				},
			},
		})
		var sr struct {
			Created    map[string]map[string]any `json:"created"`
			NotCreated map[string]any            `json:"notCreated"`
		}
		if err := json.Unmarshal(raw, &sr); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if len(sr.Created) != 1 {
			t.Fatalf("create %s failed: %+v", name, sr.NotCreated)
		}
	}

	_, raw := f.invoke(t, "Contact/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"text": "lovelace"},
	})
	var qr struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(raw, &qr); err != nil {
		t.Fatalf("unmarshal query: %v: %s", err, raw)
	}
	if len(qr.IDs) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(qr.IDs), qr.IDs)
	}
}

func TestContact_Query_HasEmail(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "Personal")
	cases := []struct {
		key  string
		body map[string]any
	}{
		{
			key: "with",
			body: map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "With Email"},
				"emails":        map[string]any{"e": map[string]any{"address": "x@example.test"}},
			},
		},
		{
			key: "without",
			body: map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "No Email"},
			},
		},
	}
	for _, c := range cases {
		_, raw := f.invoke(t, "Contact/set", map[string]any{
			"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
			"create":    map[string]any{c.key: c.body},
		})
		var sr struct {
			Created    map[string]map[string]any `json:"created"`
			NotCreated map[string]any            `json:"notCreated"`
		}
		if err := json.Unmarshal(raw, &sr); err != nil {
			t.Fatalf("create %s: %v", c.key, err)
		}
		if len(sr.Created) != 1 {
			t.Fatalf("create %s failed: %+v", c.key, sr.NotCreated)
		}
	}

	_, raw := f.invoke(t, "Contact/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"hasEmail": true},
	})
	var qr struct {
		IDs []string `json:"ids"`
	}
	_ = json.Unmarshal(raw, &qr)
	if len(qr.IDs) != 1 {
		t.Fatalf("hasEmail=true returned %d, want 1: %+v", len(qr.IDs), qr.IDs)
	}

	_, raw = f.invoke(t, "Contact/query", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"filter":    map[string]any{"hasEmail": false},
	})
	_ = json.Unmarshal(raw, &qr)
	if len(qr.IDs) != 1 {
		t.Fatalf("hasEmail=false returned %d, want 1: %+v", len(qr.IDs), qr.IDs)
	}
}

func TestContact_Changes_FromState(t *testing.T) {
	f := setupFixture(t)
	bookID := makeBook(t, f, "P")
	_, _ = f.invoke(t, "Contact/set", map[string]any{
		"accountId": string(protojmap.AccountIDForPrincipal(f.pid)),
		"create": map[string]any{
			"c": map[string]any{
				"version":       "1.0",
				"addressBookId": bookID,
				"name":          map[string]any{"full": "Carol"},
			},
		},
	})
	_, raw := f.invoke(t, "Contact/changes", map[string]any{
		"accountId":  string(protojmap.AccountIDForPrincipal(f.pid)),
		"sinceState": "0",
	})
	var ch struct {
		NewState string   `json:"newState"`
		Created  []string `json:"created"`
	}
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, raw)
	}
	if len(ch.Created) != 1 {
		t.Fatalf("created = %+v (raw=%s)", ch.Created, raw)
	}
	if ch.NewState == "0" {
		t.Errorf("newState should advance after a create, got %q", ch.NewState)
	}
}
