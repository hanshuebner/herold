package categorysettings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/categorise"
	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/protojmap"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/store/storetest/backends"
)

func setupOn(t *testing.T, st store.Store) (*handlerSet, store.Principal) {
	t.Helper()
	p, err := st.Meta().InsertPrincipal(context.Background(), store.Principal{
		Kind:           store.PrincipalKindUser,
		CanonicalEmail: "alice@example.test",
	})
	if err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	return &handlerSet{
		store: st,
		jobs:  categorise.NewJobRegistry(24*time.Hour, 256),
		clk:   clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	}, p
}

func putRules(t *testing.T, h *handlerSet, p store.Principal, rules []map[string]string) setResponse {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"accountId": protojmap.AccountIDForPrincipal(p.ID),
		"update":    map[string]any{singletonID: map[string]any{"senderRules": rules}},
	})
	resp, merr := (&setHandler{h: h}).executeAs(p, args)
	if merr != nil {
		t.Fatalf("CategorySettings/set: %v", merr)
	}
	return resp.(setResponse)
}

func getRules(t *testing.T, h *handlerSet, p store.Principal) []jmapSenderRule {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"accountId": protojmap.AccountIDForPrincipal(p.ID)})
	resp, merr := (&getHandler{h: h}).executeAs(p, args)
	if merr != nil {
		t.Fatalf("CategorySettings/get: %v", merr)
	}
	js, _ := json.Marshal(resp)
	var parsed struct {
		List []struct {
			SenderRules *[]jmapSenderRule `json:"senderRules"`
		} `json:"list"`
	}
	if err := json.Unmarshal(js, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.List) != 1 || parsed.List[0].SenderRules == nil {
		t.Fatalf("get: senderRules missing from %s", js)
	}
	return *parsed.List[0].SenderRules
}

func insertMsg(t *testing.T, st store.Store, p store.Principal, mb store.MailboxID, from string, keywords ...string) {
	t.Helper()
	ctx := context.Background()
	body := "From: " + from + "\r\nSubject: hi\r\n\r\nbody\r\n"
	ref, err := st.Blobs().Put(ctx, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Meta().InsertMessage(ctx, store.Message{
		PrincipalID:  p.ID,
		Size:         int64(len(body)),
		Blob:         ref,
		ReceivedAt:   time.Now(),
		InternalDate: time.Now(),
		Envelope:     store.Envelope{From: from, Subject: "hi"},
	}, []store.MessageMailbox{{MailboxID: mb, Keywords: keywords}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
}

func categoryByFrom(t *testing.T, st store.Store, mb store.MailboxID) map[string]string {
	t.Helper()
	msgs, err := st.Meta().ListMessages(context.Background(), mb, store.MessageFilter{Limit: 100, WithEnvelope: true})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range msgs {
		out[m.Envelope.From] = categorise.CategoryFromKeywords(m.Keywords)
	}
	return out
}

func TestCategorySettings_SenderRules_Lifecycle(t *testing.T) {
	backends.Each(t, func(t *testing.T, st store.Store) {
		h, p := setupOn(t, st)
		ctx := context.Background()
		inbox, err := st.Meta().InsertMailbox(ctx, store.Mailbox{PrincipalID: p.ID, Name: "INBOX", Attributes: store.MailboxAttrInbox})
		if err != nil {
			t.Fatal(err)
		}
		insertMsg(t, st, p, inbox.ID, "news@shop.example", "$category-updates")
		insertMsg(t, st, p, inbox.ID, "friend@home.example", "$category-primary")

		if got := getRules(t, h, p); len(got) != 0 {
			t.Fatalf("fresh account rules = %v, want empty", got)
		}

		// Create: sender is returned lower-cased, matching stored mail is
		// recategorised, other senders are untouched.
		resp := putRules(t, h, p, []map[string]string{{"sender": "News@Shop.Example", "category": "promotions"}})
		if len(resp.NotUpdated) != 0 {
			t.Fatalf("create rejected: %+v", resp.NotUpdated)
		}
		h.bg.Wait()
		rules := getRules(t, h, p)
		if len(rules) != 1 || rules[0].Sender != "news@shop.example" || rules[0].Category != "promotions" {
			t.Fatalf("rules after create = %v", rules)
		}
		got := categoryByFrom(t, st, inbox.ID)
		if got["news@shop.example"] != "promotions" || got["friend@home.example"] != "primary" {
			t.Fatalf("categories after create = %v", got)
		}

		// Replace: same sender, new category, stored mail follows.
		putRules(t, h, p, []map[string]string{{"sender": "news@shop.example", "category": "social"}})
		h.bg.Wait()
		if got := categoryByFrom(t, st, inbox.ID); got["news@shop.example"] != "social" || got["friend@home.example"] != "primary" {
			t.Fatalf("categories after replace = %v", got)
		}
		if rules := getRules(t, h, p); len(rules) != 1 || rules[0].Category != "social" {
			t.Fatalf("rules after replace = %v", rules)
		}

		// Delete: the rule goes, stored mail keeps its category.
		putRules(t, h, p, []map[string]string{})
		h.bg.Wait()
		if rules := getRules(t, h, p); len(rules) != 0 {
			t.Fatalf("rules after delete = %v", rules)
		}
		if got := categoryByFrom(t, st, inbox.ID); got["news@shop.example"] != "social" {
			t.Fatalf("categories after delete = %v, want the message left as social", got)
		}
	})
}

func TestCategorySettings_SenderRules_Rejections(t *testing.T) {
	backends.Each(t, func(t *testing.T, st store.Store) {
		h, p := setupOn(t, st)
		cases := map[string][]map[string]string{
			"unknown category": {{"sender": "a@b.example", "category": "no-such-category"}},
			"empty category":   {{"sender": "a@b.example", "category": ""}},
			"not an address":   {{"sender": "not an address", "category": "social"}},
			"display name":     {{"sender": "Bob <a@b.example>", "category": "social"}},
			"empty sender":     {{"sender": "", "category": "social"}},
			"duplicate sender": {{"sender": "a@b.example", "category": "social"}, {"sender": "A@B.example", "category": "forums"}},
		}
		for name, rules := range cases {
			resp := putRules(t, h, p, rules)
			e, ok := resp.NotUpdated[singletonID]
			if !ok || e.Type != "invalidProperties" {
				t.Errorf("%s: NotUpdated = %+v, want invalidProperties", name, resp.NotUpdated)
			}
			if len(resp.Updated) != 0 {
				t.Errorf("%s: Updated = %v, want none", name, resp.Updated)
			}
		}
		if got := getRules(t, h, p); len(got) != 0 {
			t.Fatalf("rejected sets left rules behind: %v", got)
		}
	})
}

// A derived category (from the classifier) is accepted alongside the
// configured vocabulary.
func TestCategorySettings_SenderRules_AcceptsDerivedCategory(t *testing.T) {
	backends.Each(t, func(t *testing.T, st store.Store) {
		h, p := setupOn(t, st)
		ctx := context.Background()
		cfg, err := st.Meta().GetCategorisationConfig(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Meta().SetDerivedCategories(ctx, p.ID, []string{"primary", "travel"}, cfg.DerivedCategoriesEpoch); err != nil {
			t.Fatal(err)
		}
		resp := putRules(t, h, p, []map[string]string{{"sender": "air@fly.example", "category": "travel"}})
		if len(resp.NotUpdated) != 0 {
			t.Fatalf("derived category rejected: %+v", resp.NotUpdated)
		}
		h.bg.Wait()
	})
}

// A prompt-only update keeps the stored rules.
func TestCategorySettings_SenderRules_SurvivePromptUpdate(t *testing.T) {
	backends.Each(t, func(t *testing.T, st store.Store) {
		h, p := setupOn(t, st)
		putRules(t, h, p, []map[string]string{{"sender": "a@b.example", "category": "social"}})
		h.bg.Wait()
		args, _ := json.Marshal(map[string]any{
			"accountId": protojmap.AccountIDForPrincipal(p.ID),
			"update":    map[string]any{singletonID: map[string]any{"prompt": "classify things"}},
		})
		if _, merr := (&setHandler{h: h}).executeAs(p, args); merr != nil {
			t.Fatal(merr)
		}
		if got := getRules(t, h, p); len(got) != 1 {
			t.Fatalf("rules after prompt update = %v", got)
		}
	})
}
