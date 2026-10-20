package email_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
)

// TestEmail_Get_HeadersAndHeaderForms pins the Email "headers" property
// and the dynamic "header:{name}[:{form}][:all]" properties against
// RFC 8621 sections 4.1.2 and 4.1.3: raw values keep their folding, a
// property without ":all" reads the last field of that name, ":all"
// lists every field, groups survive asGroupedAddresses, and asURLs
// returns the bracketed URLs of an RFC 2369 list.
func TestEmail_Get_HeadersAndHeaderForms(t *testing.T) {
	f := setupFixture(t)
	body := "Date: Tue, 14 Oct 2026 09:30:00 +0200\r\n" +
		"From: \"Ann Example\" <a@example.test>\r\n" +
		"To: Friends: b@example.test, c@example.test;, d@example.test\r\n" +
		"Subject: =?UTF-8?Q?gr=C3=BC=C3=9F?=\r\n folded\r\n" +
		"References: <r1@example.test>\r\n <r2@example.test>\r\n" +
		"X-Multi: one\r\n" +
		"X-Multi: two\r\n" +
		"List-Unsubscribe: <mailto:u@example.test>, <https://example.test/u>\r\n" +
		"\r\n" +
		"body"
	ref := f.putBlob(t, body)
	account := protojmap.AccountIDForPrincipal(f.pid)
	_, raw := f.invoke(t, "Email/import", map[string]any{
		"accountId": account,
		"emails": map[string]any{"n": map[string]any{
			"blobId":     ref.Hash,
			"mailboxIds": map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true},
		}},
	})
	var imported struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(raw, &imported); err != nil || imported.Created["n"].ID == "" {
		t.Fatalf("Email/import: %v: %s", err, raw)
	}

	props := []string{
		"headers",
		"header:Subject",
		"header:Subject:asText",
		"header:Subject:asText:all",
		"header:X-Multi",
		"header:x-multi:all",
		"header:To:asAddresses",
		"header:To:asGroupedAddresses",
		"header:Date:asDate",
		"header:References:asMessageIds",
		"header:List-Unsubscribe:asURLs",
		"header:X-Missing:asText",
		"header:X-Missing:all",
	}
	_, raw = f.invoke(t, "Email/get", map[string]any{
		"accountId":  account,
		"ids":        []string{imported.Created["n"].ID},
		"properties": props,
	})
	var resp struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.List) != 1 {
		t.Fatalf("Email/get: %v: %s", err, raw)
	}
	got := resp.List[0]

	var want map[string]any
	if err := json.Unmarshal([]byte(`{
		"headers": [
			{"name": "Date", "value": " Tue, 14 Oct 2026 09:30:00 +0200"},
			{"name": "From", "value": " \"Ann Example\" <a@example.test>"},
			{"name": "To", "value": " Friends: b@example.test, c@example.test;, d@example.test"},
			{"name": "Subject", "value": " =?UTF-8?Q?gr=C3=BC=C3=9F?=\r\n folded"},
			{"name": "References", "value": " <r1@example.test>\r\n <r2@example.test>"},
			{"name": "X-Multi", "value": " one"},
			{"name": "X-Multi", "value": " two"},
			{"name": "List-Unsubscribe", "value": " <mailto:u@example.test>, <https://example.test/u>"}
		],
		"header:Subject": " =?UTF-8?Q?gr=C3=BC=C3=9F?=\r\n folded",
		"header:Subject:asText": "grüß folded",
		"header:Subject:asText:all": ["grüß folded"],
		"header:X-Multi": " two",
		"header:x-multi:all": [" one", " two"],
		"header:To:asAddresses": [
			{"name": "", "email": "b@example.test"},
			{"name": "", "email": "c@example.test"},
			{"name": "", "email": "d@example.test"}
		],
		"header:To:asGroupedAddresses": [
			{"name": "Friends", "addresses": [
				{"name": "", "email": "b@example.test"},
				{"name": "", "email": "c@example.test"}
			]},
			{"name": null, "addresses": [{"name": "", "email": "d@example.test"}]}
		],
		"header:Date:asDate": "2026-10-14T07:30:00Z",
		"header:References:asMessageIds": ["r1@example.test", "r2@example.test"],
		"header:List-Unsubscribe:asURLs": ["mailto:u@example.test", "https://example.test/u"],
		"header:X-Missing:asText": null,
		"header:X-Missing:all": []
	}`), &want); err != nil {
		t.Fatalf("want: %v", err)
	}
	for _, p := range props {
		g, ok := got[p]
		if !ok {
			t.Errorf("%s missing from Email/get", p)
			continue
		}
		if !reflect.DeepEqual(g, want[p]) {
			gj, _ := json.Marshal(g)
			wj, _ := json.Marshal(want[p])
			t.Errorf("%s = %s, want %s", p, gj, wj)
		}
	}
}
