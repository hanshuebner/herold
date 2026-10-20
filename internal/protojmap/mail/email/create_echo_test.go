package email_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
)

// The Email object in the created map of Email/set and Email/import
// carries the mailboxIds and keywords the email was stored with, the
// same values Email/get returns for it.
func TestEmail_Create_EchoesStoredMailboxIdsAndKeywords(t *testing.T) {
	f := setupFixture(t)
	archive, err := f.srv.Store.Meta().InsertMailbox(context.Background(), store.Mailbox{PrincipalID: f.pid, Name: "Archive"})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	account := protojmap.AccountIDForPrincipal(f.pid)
	mailboxIDs := map[string]bool{fmt.Sprintf("%d", f.inbox.ID): true, fmt.Sprintf("%d", archive.ID): true}
	keywords := map[string]bool{"$seen": true, "$flagged": true, "project-x": true}

	type created struct {
		ID         string          `json:"id"`
		MailboxIDs map[string]bool `json:"mailboxIds"`
		Keywords   map[string]bool `json:"keywords"`
	}
	check := func(method, key string, raw json.RawMessage) {
		t.Helper()
		var resp struct {
			Created    map[string]created `json:"created"`
			NotCreated map[string]any     `json:"notCreated"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("%s: unmarshal: %v: %s", method, err, raw)
		}
		c, ok := resp.Created[key]
		if !ok {
			t.Fatalf("%s: not created: %s", method, raw)
		}
		if !reflect.DeepEqual(c.MailboxIDs, mailboxIDs) || !reflect.DeepEqual(c.Keywords, keywords) {
			t.Errorf("%s created mailboxIds = %v, keywords = %v; want %v, %v",
				method, c.MailboxIDs, c.Keywords, mailboxIDs, keywords)
		}
		_, raw = f.invoke(t, "Email/get", map[string]any{
			"accountId": account, "ids": []string{c.ID}, "properties": []string{"mailboxIds", "keywords"},
		})
		var get struct {
			List []created `json:"list"`
		}
		if err := json.Unmarshal(raw, &get); err != nil || len(get.List) != 1 {
			t.Fatalf("%s: Email/get: %v: %s", method, err, raw)
		}
		if !reflect.DeepEqual(get.List[0].MailboxIDs, c.MailboxIDs) || !reflect.DeepEqual(get.List[0].Keywords, c.Keywords) {
			t.Errorf("%s: Email/get = %+v, created = %+v", method, get.List[0], c)
		}
	}

	ref := f.putBlob(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: set\r\nMessage-ID: <set@example.test>\r\n\r\nbody")
	_, raw := f.invoke(t, "Email/set", map[string]any{
		"accountId": account,
		"create": map[string]any{"s": map[string]any{
			"blobId": ref.Hash, "mailboxIds": mailboxIDs, "keywords": keywords,
		}},
	})
	check("Email/set", "s", raw)

	ref = f.putBlob(t, "From: a@example.test\r\nTo: b@example.test\r\nSubject: import\r\nMessage-ID: <import@example.test>\r\n\r\nbody")
	_, raw = f.invoke(t, "Email/import", map[string]any{
		"accountId": account,
		"emails": map[string]any{"i": map[string]any{
			"blobId": ref.Hash, "mailboxIds": mailboxIDs, "keywords": keywords,
		}},
	})
	check("Email/import", "i", raw)
}
