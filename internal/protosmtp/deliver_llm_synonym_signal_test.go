package protosmtp_test

// deliver_llm_synonym_signal_test.go covers re #489: message 3971's
// production record, where the classifier plugin returned verdict=ham,
// score=0.25, and spam_signals naming "commercial_promotion" and
// "unsolicited_marketing_pitch" -- two labels for the same
// unsolicited-bulk-marketing trait internal/spam.
// DefaultDecisiveSpamSignals names as "unsolicited_bulk_marketing".
// matchDecisiveSignal's exact-name comparison found no match, so the
// contradiction was only flagged (Inconsistent) and the message was
// delivered to Inbox. This file's first case reproduces that shape and
// expects Junk; the second confirms the canonical name alone still
// resolves; the third confirms a genuinely non-decisive signal alone
// stays flagged-only (Inbox), unchanged from today.
//
// Each case runs on both backends (SQLite and Postgres).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanshuebner/herold/internal/protosmtp"
	"github.com/hanshuebner/herold/internal/store"
)

func TestDelivery_Message3971SynonymNamedSpamSignalsGoesToJunk_SQLite(t *testing.T) {
	testDeliveryMessage3971SynonymNamedSpamSignalsGoesToJunk(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_Message3971SynonymNamedSpamSignalsGoesToJunk_Postgres(t *testing.T) {
	testDeliveryMessage3971SynonymNamedSpamSignalsGoesToJunk(t, newPGStoreFactory)
}

// testDeliveryMessage3971SynonymNamedSpamSignalsGoesToJunk reproduces
// message 3971's production record: a Spanish-language bulk marketing
// pitch, DMARC-aligned pass, List-Unsubscribe present, classified
// verdict=ham score=0.25 with spam_signals naming "commercial_promotion"
// and "unsolicited_marketing_pitch" -- neither of which is the literal
// "unsolicited_bulk_marketing" name DefaultDecisiveSpamSignals compares
// against. Delivery must resolve to Junk once the synonym is normalized.
func testDeliveryMessage3971SynonymNamedSpamSignalsGoesToJunk(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.25,"reason":"This is a legitimate educational workshop promotion from an authenticated sender with proper mailing list infrastructure, not unsolicited bulk marketing.","spam_signals":["commercial_promotion","unsolicited_marketing_pitch"],"ham_signals":["passing_authentication","list_id_present","list_unsubscribe_header","educational_content","legitimate_business_training"]}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<info@capacitacion.iccstudy.org>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: GRE Ultimos Cambios SUNAT <info@capacitacion.iccstudy.org>\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: Evita contingencias y comiso de bienes por errores en GRE\r\n" +
		"List-Id: <boletin.iccstudy.org>\r\n" +
		"List-Unsubscribe: <mailto:unsubscribe@capacitacion.iccstudy.org>\r\n\r\n" +
		"Capacitacion sobre la Guia de Remision Electronica (GRE) y SUNAT.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	junk, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "Junk")
	if err != nil {
		t.Fatalf("GetMailboxByName(Junk): %v", err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, junk.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(Junk): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in Junk = %d, want 1 (a ham verdict naming the decisive trait under a synonym must still resolve to Junk)", len(msgs))
	}

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "spam" {
		t.Fatalf("SpamVerdict = %v, want spam (the applied verdict)", rec.SpamVerdict)
	}
	if rec.SpamModelVerdict == nil || *rec.SpamModelVerdict != "ham" {
		t.Fatalf("SpamModelVerdict = %v, want ham (the plugin's own original verdict)", rec.SpamModelVerdict)
	}
	if rec.SpamDecisiveSignalMatch == nil || *rec.SpamDecisiveSignalMatch != "unsolicited_bulk_marketing" {
		t.Fatalf("SpamDecisiveSignalMatch = %v, want unsolicited_bulk_marketing (the canonical name the synonym normalises onto)", rec.SpamDecisiveSignalMatch)
	}
	// The persisted spam_signals keep the model's own reported names
	// verbatim; normalization affects only the decisiveness check and
	// the separate SpamDecisiveSignalMatch field.
	if rec.SpamSignals == nil {
		t.Fatalf("SpamSignals = nil, want the model's own reported names")
	}
	got := map[string]bool{}
	for _, s := range *rec.SpamSignals {
		got[s] = true
	}
	if !got["commercial_promotion"] || !got["unsolicited_marketing_pitch"] {
		t.Fatalf("SpamSignals = %v, want [commercial_promotion unsolicited_marketing_pitch] preserved verbatim", *rec.SpamSignals)
	}
}

func TestDelivery_CanonicalDecisiveSignalNameGoesToJunk_SQLite(t *testing.T) {
	testDeliveryCanonicalDecisiveSignalNameGoesToJunk(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_CanonicalDecisiveSignalNameGoesToJunk_Postgres(t *testing.T) {
	testDeliveryCanonicalDecisiveSignalNameGoesToJunk(t, newPGStoreFactory)
}

// testDeliveryCanonicalDecisiveSignalNameGoesToJunk confirms the
// unchanged case: a classifier response naming the decisive signal
// using its own canonical spelling still resolves to Junk after the
// normalization change (re #489, canonical-name case).
func testDeliveryCanonicalDecisiveSignalNameGoesToJunk(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.2,"reason":"promotional content","spam_signals":["unsolicited_bulk_marketing"],"ham_signals":[]}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<sales@canonical-pitch.example>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: Canonical Pitch <sales@canonical-pitch.example>\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: Boost your business today\r\n\r\n" +
		"Cheap marketing services for your business.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	junk, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "Junk")
	if err != nil {
		t.Fatalf("GetMailboxByName(Junk): %v", err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, junk.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(Junk): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in Junk = %d, want 1 (the canonical decisive-signal name must still resolve to Junk)", len(msgs))
	}

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamDecisiveSignalMatch == nil || *rec.SpamDecisiveSignalMatch != "unsolicited_bulk_marketing" {
		t.Fatalf("SpamDecisiveSignalMatch = %v, want unsolicited_bulk_marketing", rec.SpamDecisiveSignalMatch)
	}
}

func TestDelivery_NonDecisiveSignalOnlyStaysInInbox_SQLite(t *testing.T) {
	testDeliveryNonDecisiveSignalOnlyStaysInInbox(t, func(*testing.T) store.Store { return nil })
}

func TestDelivery_NonDecisiveSignalOnlyStaysInInbox_Postgres(t *testing.T) {
	testDeliveryNonDecisiveSignalOnlyStaysInInbox(t, newPGStoreFactory)
}

// testDeliveryNonDecisiveSignalOnlyStaysInInbox confirms the
// normalization change does not widen decisiveness beyond the
// recognised synonyms: a ham verdict naming only a genuinely
// non-decisive signal (e.g. urgency_language, not a synonym of
// unsolicited_bulk_marketing) stays flagged-only and delivered to Inbox,
// exactly as before this fix (re #489, third checklist item).
func testDeliveryNonDecisiveSignalOnlyStaysInInbox(t *testing.T, storeFactory func(t *testing.T) store.Store) {
	f := newFixture(t, fixtureOpts{mode: protosmtp.RelayIn, store: storeFactory(t)})
	f.spamPlug.Handle("spam.classify", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"verdict":"ham","score":0.3,"reason":"a little pushy but plausible","spam_signals":["urgency_language"],"ham_signals":["known_correspondent"]}`), nil
	})

	cli, closeFn := f.dial(t)
	defer closeFn()
	mustOK(t, cli, 220)
	cli.send(t, "EHLO client.example.test")
	mustOK(t, cli, 250)
	cli.send(t, "MAIL FROM:<sender@nondecisive.example>")
	mustOK(t, cli, 250)
	cli.send(t, "RCPT TO:<alice@example.test>")
	mustOK(t, cli, 250)
	cli.send(t, "DATA")
	mustOK(t, cli, 354)
	body := "From: sender@nondecisive.example\r\n" +
		"To: alice@example.test\r\n" +
		"Subject: Please respond soon\r\n\r\n" +
		"We need your answer as soon as possible.\r\n.\r\n"
	cli.sendRaw(t, []byte(body))
	mustOK(t, cli, 250)
	cli.send(t, "QUIT")
	mustOK(t, cli, 221)

	ctx := context.Background()
	inbox, err := f.ha.Store.Meta().GetMailboxByName(ctx, f.principal, "INBOX")
	if err != nil {
		t.Fatalf("GetMailboxByName(INBOX): %v", err)
	}
	msgs, err := f.ha.Store.Meta().ListMessages(ctx, inbox.ID, store.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages(INBOX): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages in INBOX = %d, want 1 (a non-decisive-only signal must stay flagged, not resolved)", len(msgs))
	}

	rec, err := f.ha.Store.Meta().GetLLMClassification(ctx, msgs[0].ID)
	if err != nil {
		t.Fatalf("GetLLMClassification: %v", err)
	}
	if rec.SpamVerdict == nil || *rec.SpamVerdict != "ham" {
		t.Fatalf("SpamVerdict = %v, want ham (no decisive signal present)", rec.SpamVerdict)
	}
	if rec.SpamModelVerdict != nil {
		t.Fatalf("SpamModelVerdict = %v, want nil (no server-side resolution happened)", *rec.SpamModelVerdict)
	}
	if rec.SpamDecisiveSignalMatch != nil {
		t.Fatalf("SpamDecisiveSignalMatch = %v, want nil (no server-side resolution happened)", *rec.SpamDecisiveSignalMatch)
	}
	if rec.SpamInconsistent == nil || !*rec.SpamInconsistent {
		t.Fatalf("SpamInconsistent = %v, want true (flagged, not resolved)", rec.SpamInconsistent)
	}
}
