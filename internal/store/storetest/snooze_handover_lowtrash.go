package storetest

import (
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Trash is created at provisioning, so in production it has a lower id
// than the mailboxes a user creates later. Consolidation must still keep
// the reminder off the Trash membership.
func testSnoozeConsolidateSkipsLowIDTrash(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "consolidate-lowtrash@example.com")
	trash := mustInsertMailboxWithAttrs(t, s, p.ID, "Trash", store.MailboxAttrTrash)
	inbox := mustInsertMailbox(t, s, p.ID, "Inbox")
	wake := mustInsertMailbox(t, s, p.ID, "Wake")
	if trash.ID >= inbox.ID {
		t.Fatalf("fixture: Trash id %d must be below Inbox id %d", trash.ID, inbox.ID)
	}
	ref := putBlob(t, s, "lowtrash body")
	if _, _, err := s.Meta().InsertMessage(ctx,
		store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size},
		[]store.MessageMailbox{{MailboxID: inbox.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	f := &handoverFixture{t: t, s: s, pid: p.ID, lo: inbox, wake: wake,
		msg: firstMessageIDFromFeed(t, s, p.ID), until: time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)}
	w := wake.ID
	if _, err := s.Meta().SetSnooze(ctx, f.msg, inbox.ID, &f.until, &w); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	if _, _, err := s.Meta().AddMessageToMailbox(ctx, f.msg, trash.ID); err != nil {
		t.Fatalf("AddMessageToMailbox(Trash): %v", err)
	}
	if err := s.Meta().ConsolidateSnooze(ctx, f.msg); err != nil {
		t.Fatalf("ConsolidateSnooze: %v", err)
	}
	f.wantHolder("after adding Trash and consolidating", inbox.ID, 0)
	m, err := s.Meta().GetMessage(ctx, f.msg)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	for _, mm := range m.Mailboxes {
		if mm.MailboxID == trash.ID && (mm.SnoozedUntil != nil || mm.WakeMailboxID != nil || len(mm.Keywords) != 0) {
			t.Errorf("Trash membership carries reminder state: %+v", mm)
		}
	}
}

// A reminder found on a message that lives only in Trash ends when the
// message is consolidated: keyword and deadline clear together.
func testSnoozeConsolidateEndsReminderOnTrashOnly(t *testing.T, s store.Store) {
	ctx := ctxT(t)
	p := mustInsertPrincipal(t, s, "consolidate-trashonly@example.com")
	trash := mustInsertMailboxWithAttrs(t, s, p.ID, "Trash", store.MailboxAttrTrash)
	ref := putBlob(t, s, "trashonly body")
	if _, _, err := s.Meta().InsertMessage(ctx,
		store.Message{PrincipalID: p.ID, Blob: ref, Size: ref.Size},
		[]store.MessageMailbox{{MailboxID: trash.ID}}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	f := &handoverFixture{t: t, s: s, pid: p.ID, msg: firstMessageIDFromFeed(t, s, p.ID)}
	due := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.Meta().SetSnooze(ctx, f.msg, trash.ID, &due, nil); err != nil {
		t.Fatalf("SetSnooze: %v", err)
	}
	if err := s.Meta().ConsolidateSnooze(ctx, f.msg); err != nil {
		t.Fatalf("ConsolidateSnooze: %v", err)
	}
	f.wantNoReminder("after consolidating a Trash-only message", trash.ID)
}
