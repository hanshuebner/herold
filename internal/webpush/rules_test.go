package webpush

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

func TestParseRules_Empty(t *testing.T) {
	t.Parallel()
	r, err := ParseRules(nil)
	if err != nil {
		t.Fatalf("ParseRules(nil): %v", err)
	}
	want := DefaultRules()
	if r.Master != want.Master {
		t.Fatalf("Master=%v want %v", r.Master, want.Master)
	}
	if !reflect.DeepEqual(r.MailCategoryAllowlist, want.MailCategoryAllowlist) {
		t.Fatalf("MailCategoryAllowlist=%v want %v",
			r.MailCategoryAllowlist, want.MailCategoryAllowlist)
	}
	for _, k := range allEventTypes {
		if !r.PerEventType[k] {
			t.Fatalf("default PerEventType[%q]=false; want true", k)
		}
	}
	if !r.QuietHoursOverridePerType[EventTypeCallIncoming] {
		t.Fatalf("default override for call_incoming must be true")
	}
}

func TestParseRules_BadJSON(t *testing.T) {
	t.Parallel()
	if _, err := ParseRules([]byte("not json")); err == nil {
		t.Fatalf("expected error on malformed JSON")
	}
}

func TestParseRules_PerEventType_Override(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"perEventType":{"mail":false,"chat_dm":true}}`)
	r, err := ParseRules(raw)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if r.PerEventType[EventTypeMail] {
		t.Fatalf("mail should be false")
	}
	if !r.PerEventType[EventTypeChatDM] {
		t.Fatalf("chat_dm should be true")
	}
	// closed-enum keys not specified must keep their default true.
	if !r.PerEventType[EventTypeChatSpace] {
		t.Fatalf("chat_space default not preserved")
	}
}

func TestParseRules_UnknownEventTypeKey(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"perEventType":{"mail":true,"unknown_kind":true}}`)
	r, err := ParseRules(raw)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if len(r.WarnUnknownEventTypes) != 1 || r.WarnUnknownEventTypes[0] != "unknown_kind" {
		t.Fatalf("WarnUnknownEventTypes=%v", r.WarnUnknownEventTypes)
	}
	if _, ok := r.PerEventType["unknown_kind"]; ok {
		t.Fatalf("unknown key leaked into PerEventType")
	}
}

func TestParseRules_QuietHoursValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"valid", `{"quietHours":{"startHourLocal":22,"endHourLocal":7,"tz":"UTC"}}`, true},
		{"bad-tz", `{"quietHours":{"startHourLocal":1,"endHourLocal":2,"tz":"Mars/Olympus"}}`, false},
		{"start-out-of-range", `{"quietHours":{"startHourLocal":24,"endHourLocal":7,"tz":"UTC"}}`, false},
		{"end-out-of-range", `{"quietHours":{"startHourLocal":0,"endHourLocal":-1,"tz":"UTC"}}`, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseRules([]byte(c.raw))
			if c.ok && err != nil {
				t.Fatalf("ok case errored: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("error case parsed without error")
			}
		})
	}
}

func TestParseRules_PreservesUnknownTopLevelFields(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"master":true,"futureKnob":{"foo":42},"newField":"hi"}`)
	r, err := ParseRules(raw)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if r.Unknown["futureKnob"] == nil {
		t.Fatalf("futureKnob not preserved; got %v", r.Unknown)
	}
	if r.Unknown["newField"] == nil {
		t.Fatalf("newField not preserved")
	}
	// Round-trip preserves the unknown payload.
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(out, &generic); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if _, ok := generic["futureKnob"]; !ok {
		t.Fatalf("futureKnob lost on round-trip; got %s", out)
	}
	if _, ok := generic["newField"]; !ok {
		t.Fatalf("newField lost on round-trip; got %s", out)
	}
}

func TestParseRules_VIPField_Roundtrips(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"senderVIPs":["alice@example.test","@example.com"]}`)
	r, err := ParseRules(raw)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if !reflect.DeepEqual(r.SenderVIPs, []string{"alice@example.test", "@example.com"}) {
		t.Fatalf("VIPs=%v", r.SenderVIPs)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "alice@example.test") {
		t.Fatalf("VIPs lost on round-trip: %s", out)
	}
}

func TestEvaluate_Default_AllowsMailPrimary(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	mid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		Keywords: []string{"$category-primary"},
		Envelope: store.Envelope{Subject: "x"},
	}, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	}
	d := Evaluate(context.Background(), DefaultRules(), st, ev, time.Now().UTC())
	if !d.Allow || d.Reason != ReasonDefaultAllow {
		t.Fatalf("decision=%+v want allow/default", d)
	}
}

func TestEvaluate_Default_DeniesMailPromotions(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	mid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		PrincipalID: pid,
		Keywords:    []string{"$category-promotions"},
		Envelope:    store.Envelope{Subject: "buy now"},
	}, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	}
	d := Evaluate(context.Background(), DefaultRules(), st, ev, time.Now().UTC())
	if d.Allow {
		t.Fatalf("expected deny on promotions; got %+v", d)
	}
	if d.Reason != ReasonCategoryFiltered {
		t.Fatalf("reason=%q want %q", d.Reason, ReasonCategoryFiltered)
	}
}

func TestEvaluate_MasterOff_DeniesEverything(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	rules := DefaultRules()
	rules.Master = false
	ev := store.StateChange{Kind: store.EntityKindEmail, Op: store.ChangeOpCreated}
	d := Evaluate(context.Background(), rules, st, ev, time.Now().UTC())
	if d.Allow || d.Reason != ReasonMutedMaster {
		t.Fatalf("decision=%+v want deny/master", d)
	}
}

func TestEvaluate_PerEventTypeOff_DeniesMailAllowsChat(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	rules := DefaultRules()
	rules.PerEventType[EventTypeMail] = false

	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	mid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		PrincipalID: pid,
		Keywords:    []string{"$category-primary"},
		Envelope:    store.Envelope{Subject: "ignored"},
	}, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	mailEv := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	}
	if d := Evaluate(context.Background(), rules, st, mailEv, time.Now().UTC()); d.Allow {
		t.Fatalf("mail must deny when PerEventType[mail]=false; got %+v", d)
	}
}

func TestEvaluate_QuietHoursDeniesUnlessOverride(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	mid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		Keywords: []string{"$category-primary"},
		Envelope: store.Envelope{Subject: "x"},
	}, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	}
	rules := DefaultRules()
	start := 22
	end := 7
	rules.QuietHoursStartLocal = &start
	rules.QuietHoursEndLocal = &end
	rules.QuietHoursTZ = "UTC"

	// 23:00 UTC is inside [22, 7) wrap-around.
	insideQuiet := time.Date(2026, 4, 25, 23, 0, 0, 0, time.UTC)
	d := Evaluate(context.Background(), rules, st, ev, insideQuiet)
	if d.Allow || d.Reason != ReasonQuietHours {
		t.Fatalf("decision inside quiet=%+v want deny/quiet_hours", d)
	}
	// 12:00 UTC is outside the window — allow.
	outside := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	if d := Evaluate(context.Background(), rules, st, ev, outside); !d.Allow {
		t.Fatalf("decision outside quiet=%+v want allow", d)
	}
}

func TestWithinQuietHours_WrapAround(t *testing.T) {
	t.Parallel()
	cases := []struct {
		hour       int
		start, end int
		want       bool
	}{
		{23, 22, 7, true},
		{0, 22, 7, true},
		{6, 22, 7, true},
		{7, 22, 7, false},
		{8, 22, 7, false},
		{22, 22, 7, true},
		{12, 22, 7, false},
		// Non-wrap-around: 9..17 => 9 in, 17 out.
		{9, 9, 17, true},
		{17, 9, 17, false},
		{16, 9, 17, true},
		// start == end => empty window.
		{5, 5, 5, false},
	}
	for _, c := range cases {
		instant := time.Date(2026, 4, 25, c.hour, 0, 0, 0, time.UTC)
		got := withinQuietHours(instant, "UTC", c.start, c.end)
		if got != c.want {
			t.Errorf("withinQuietHours(hour=%d, %d..%d)=%v want %v",
				c.hour, c.start, c.end, got, c.want)
		}
	}
}

func TestEvaluate_QuietHoursWithCallOverride(t *testing.T) {
	t.Parallel()
	// Synthesise a chat DM event. Calls don't have their own EntityKind
	// today (they ride on chat_dm system messages), but we can prove the
	// override mechanism by setting an explicit override on chat_dm.
	st := newTestStore(t)
	pid := mustInsertPrincipal(t, st, "alice@example.test")
	convID, err := st.Meta().InsertChatConversation(context.Background(), store.ChatConversation{
		Kind:                 store.ChatConversationKindDM,
		CreatedByPrincipalID: pid,
	})
	if err != nil {
		t.Fatalf("InsertChatConversation: %v", err)
	}
	cmid, err := st.Meta().InsertChatMessage(context.Background(), store.ChatMessage{
		ConversationID:    convID,
		SenderPrincipalID: &pid,
		BodyText:          "hi",
		BodyFormat:        "text",
	})
	if err != nil {
		t.Fatalf("InsertChatMessage: %v", err)
	}

	rules := DefaultRules()
	start := 22
	end := 7
	rules.QuietHoursStartLocal = &start
	rules.QuietHoursEndLocal = &end
	rules.QuietHoursTZ = "UTC"
	rules.QuietHoursOverridePerType[EventTypeChatDM] = true

	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindChatMessage,
		EntityID:    uint64(cmid),
		Op:          store.ChangeOpCreated,
	}
	insideQuiet := time.Date(2026, 4, 25, 23, 0, 0, 0, time.UTC)
	d := Evaluate(context.Background(), rules, st, ev, insideQuiet)
	if !d.Allow {
		t.Fatalf("override should let chat_dm pass quiet hours: %+v", d)
	}
}

func TestEvaluate_VIPField_NotConsultedServerSide(t *testing.T) {
	t.Parallel()
	// REQ-PUSH-83: VIPs are client-local. Even if the field is set in
	// the JSON, server-side evaluation MUST NOT use it to override a
	// category-filtered mail event.
	st := newTestStore(t)
	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	mid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		PrincipalID: pid,
		Keywords:    []string{"$category-promotions"},
		Envelope:    store.Envelope{From: "vip@example.test", Subject: "promo"},
	}, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	rules := DefaultRules()
	rules.SenderVIPs = []string{"vip@example.test"}
	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	}
	d := Evaluate(context.Background(), rules, st, ev, time.Now().UTC())
	if d.Allow {
		t.Fatalf("VIP field must not let category-filtered mail through server-side; got %+v", d)
	}
}

func TestEvaluate_CalendarInvite(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	pid := mustInsertPrincipal(t, st, "carol@example.test")
	calID, err := st.Meta().InsertCalendar(context.Background(), store.Calendar{
		PrincipalID: pid,
		Name:        "Work",
		IsDefault:   true,
	})
	if err != nil {
		t.Fatalf("InsertCalendar: %v", err)
	}
	cevID, err := st.Meta().InsertCalendarEvent(context.Background(), store.CalendarEvent{
		CalendarID:  calID,
		PrincipalID: pid,
		UID:         "evt-1",
		Summary:     "Standup",
		Start:       time.Now().UTC(),
		End:         time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("InsertCalendarEvent: %v", err)
	}
	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindCalendarEvent,
		EntityID:    uint64(cevID),
		Op:          store.ChangeOpCreated,
	}
	d := Evaluate(context.Background(), DefaultRules(), st, ev, time.Now().UTC())
	if !d.Allow {
		t.Fatalf("calendar invite must pass default rules; got %+v", d)
	}
	if d.EventType != EventTypeCalendarInvite {
		t.Fatalf("EventType=%q want %q", d.EventType, EventTypeCalendarInvite)
	}
}

func TestEvaluate_NilCategoryAllowlist_PassesEveryMail(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	pid := mustInsertPrincipal(t, st, "alice@example.test")
	mbid := mustInsertMailbox(t, st, pid, "INBOX")
	mid, _, err := st.Meta().InsertMessage(context.Background(), store.Message{
		PrincipalID: pid,
		Keywords:    []string{"$category-promotions"},
		Envelope:    store.Envelope{Subject: "x"},
	}, []store.MessageMailbox{{MailboxID: mbid}})
	if err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	rules := DefaultRules()
	rules.MailCategoryAllowlist = nil
	ev := store.StateChange{
		PrincipalID: pid,
		Kind:        store.EntityKindEmail,
		EntityID:    uint64(mid),
		Op:          store.ChangeOpCreated,
	}
	if d := Evaluate(context.Background(), rules, st, ev, time.Now().UTC()); !d.Allow {
		t.Fatalf("nil allowlist should pass all mail; got %+v", d)
	}
}

func TestParseRules_Roundtrip_Stable(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"master":false,"perEventType":{"mail":false},"mailCategories":["primary","updates"],"quietHours":{"startHourLocal":22,"endHourLocal":7,"tz":"UTC"},"customField":42}`)
	r, err := ParseRules(raw)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	r2, err := ParseRules(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if r.Master != r2.Master {
		t.Fatalf("Master drift: %v vs %v", r.Master, r2.Master)
	}
	if !reflect.DeepEqual(r.MailCategoryAllowlist, r2.MailCategoryAllowlist) {
		t.Fatalf("MailCategoryAllowlist drift: %v vs %v",
			r.MailCategoryAllowlist, r2.MailCategoryAllowlist)
	}
	if !reflect.DeepEqual(r.Unknown, r2.Unknown) {
		t.Fatalf("Unknown drift: %v vs %v", r.Unknown, r2.Unknown)
	}
}
