package storetest

import (
	"bytes"
	"testing"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// testDescriptorEpochs checks which writes advance store.DescriptorEpochs
// (REQ-PROTO-43): every write the JMAP session descriptor's accounts,
// per-principal capabilities and clientlog capability depend on advances
// its counter, and mail and session-touch writes advance neither.
func testDescriptorEpochs(t *testing.T, s store.Store) {
	t.Helper()
	ctx := ctxT(t)
	meta := s.Meta()
	src, ok := meta.(store.DescriptorEpochSource)
	if !ok {
		t.Fatal("Metadata does not implement store.DescriptorEpochSource")
	}
	ep := src.DescriptorEpochs()

	accounts := func(name string, fn func() error) {
		t.Helper()
		a, ss := ep.Accounts(), ep.Sessions()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ep.Accounts() == a {
			t.Errorf("%s did not advance the accounts epoch", name)
		}
		if ep.Sessions() != ss {
			t.Errorf("%s advanced the sessions epoch", name)
		}
	}
	sessions := func(name string, fn func() error) {
		t.Helper()
		a, ss := ep.Accounts(), ep.Sessions()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ep.Sessions() == ss {
			t.Errorf("%s did not advance the sessions epoch", name)
		}
		if ep.Accounts() != a {
			t.Errorf("%s advanced the accounts epoch", name)
		}
	}
	neither := func(name string, fn func() error) {
		t.Helper()
		a, ss := ep.Accounts(), ep.Sessions()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ep.Accounts() != a || ep.Sessions() != ss {
			t.Errorf("%s advanced an epoch", name)
		}
	}

	owner := mustInsertPrincipal(t, s, "epoch-owner@example.test")
	grantee := mustInsertPrincipal(t, s, "epoch-grantee@example.test")
	mb := mustInsertMailbox(t, s, owner.ID, "INBOX")
	gid := grantee.ID

	neither("InsertMessage", func() error {
		ref, err := s.Blobs().Put(ctx, bytes.NewReader([]byte("Subject: x\r\n\r\nbody\r\n")))
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		_, _, err = meta.InsertMessage(ctx, store.Message{PrincipalID: owner.ID, InternalDate: now,
			ReceivedAt: now, Size: ref.Size, Blob: ref}, []store.MessageMailbox{{MailboxID: mb.ID}})
		return err
	})
	neither("InsertMailbox", func() error {
		_, err := meta.InsertMailbox(ctx, store.Mailbox{PrincipalID: owner.ID, Name: "Other"})
		return err
	})

	accounts("SetMailboxACL", func() error {
		return meta.SetMailboxACL(ctx, mb.ID, &gid, store.ACLRightLookup|store.ACLRightRead, owner.ID)
	})
	accounts("RemoveMailboxACL", func() error { return meta.RemoveMailboxACL(ctx, mb.ID, &gid) })
	var g store.Grant
	accounts("InsertGrant", func() error {
		var err error
		g, err = meta.InsertGrant(ctx, store.Grant{SubjectKind: store.GrantSubjectPrincipal, SubjectID: uint64(gid),
			ResourceKind: store.GrantResourceMailbox, ResourceID: "999", Level: store.GrantLevelRead,
			Provenance: store.GrantProvenanceLocal})
		return err
	})
	accounts("DeleteGrant", func() error { return meta.DeleteGrant(ctx, g) })

	desired := []store.GrantDesired{{ResourceKind: store.GrantResourceMailbox, ResourceID: "998", Level: store.GrantLevelRead}}
	t0 := time.Now().UTC().Add(-time.Hour)
	accounts("ReconcileIdPGrants adding a grant", func() error {
		_, _, err := meta.ReconcileIdPGrants(ctx, gid, "epochs", desired, t0)
		return err
	})
	neither("ReconcileIdPGrants with nothing to change", func() error {
		_, _, err := meta.ReconcileIdPGrants(ctx, gid, "epochs", desired, t0)
		return err
	})
	neither("SweepStaleIdPGrants with nothing stale", func() error {
		_, err := meta.SweepStaleIdPGrants(ctx, t0.Add(-time.Hour))
		return err
	})
	accounts("SweepStaleIdPGrants removing a grant", func() error {
		_, err := meta.SweepStaleIdPGrants(ctx, t0.Add(time.Minute))
		return err
	})

	other, err := meta.InsertMailbox(ctx, store.Mailbox{PrincipalID: owner.ID, Name: "Doomed"})
	if err != nil {
		t.Fatalf("InsertMailbox: %v", err)
	}
	accounts("DeleteMailbox", func() error { return meta.DeleteMailbox(ctx, other.ID) })

	renamed := owner
	renamed.CanonicalEmail = "epoch-owner-renamed@example.test"
	accounts("UpdatePrincipal", func() error { return meta.UpdatePrincipal(ctx, renamed) })
	var sub store.Principal
	accounts("InsertSubPrincipal", func() error {
		var err error
		sub, err = meta.InsertSubPrincipal(ctx, owner.ID, store.Principal{Kind: store.PrincipalKindSubAccount,
			CanonicalEmail: "epoch-sub@example.test"})
		return err
	})
	accounts("DeletePrincipal", func() error { return meta.DeletePrincipal(ctx, sub.ID) })

	var app store.App
	accounts("InsertApp", func() error {
		var err error
		app, err = meta.InsertApp(ctx, newTestApp("test.epochs.app"))
		return err
	})
	accounts("ApproveAppManifest", func() error {
		var err error
		app, err = meta.ApproveAppManifest(ctx, app.ID, app.PendingManifestDigest)
		return err
	})
	accounts("UpdateApp", func() error {
		app.AllPrincipals = true
		var err error
		app, err = meta.UpdateApp(ctx, app)
		return err
	})
	accounts("SetAppPrincipals", func() error { return meta.SetAppPrincipals(ctx, app.ID, []store.PrincipalID{gid}) })
	neither("RecordAppInvoked", func() error { return meta.RecordAppInvoked(ctx, app.ID, time.Now()) })
	accounts("DeleteApp", func() error { return meta.DeleteApp(ctx, app.ID) })

	now := time.Now().UTC().Truncate(time.Microsecond)
	row := store.SessionRow{SessionID: "csrf-epochs", PrincipalID: gid, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	sessions("UpsertSession", func() error { return meta.UpsertSession(ctx, row) })
	sessions("UpdateSessionTelemetry", func() error { return meta.UpdateSessionTelemetry(ctx, row.SessionID, true) })
	neither("UpdateSessionLastSeen", func() error {
		return meta.UpdateSessionLastSeen(ctx, row.SessionID, now.UnixMicro(), "192.0.2.1")
	})
	neither("ClearExpiredLivetail with nothing expired", func() error {
		_, err := meta.ClearExpiredLivetail(ctx, now.UnixMicro())
		return err
	})
	past := now.Add(-time.Minute)
	row.ClientlogLivetailUntil = &past
	sessions("UpsertSession with livetail", func() error { return meta.UpsertSession(ctx, row) })
	sessions("ClearExpiredLivetail clearing a row", func() error {
		_, err := meta.ClearExpiredLivetail(ctx, now.UnixMicro())
		return err
	})
	sessions("TombstoneSession", func() error {
		return meta.TombstoneSession(ctx, row.SessionID, gid, now.UnixMicro(), int64(time.Hour/time.Microsecond))
	})
	sessions("DeleteSession", func() error { return meta.DeleteSession(ctx, row.SessionID) })
}
