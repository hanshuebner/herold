package store

// subaccount.go implements sub-account promotion and removal (issue #227,
// REQ-SUBACCT-09/10, REQ-IMAP-IMP-106/107): separating an existing
// Identity into its own sub-principal, migrating the mail an IMAP-import
// account already pulled in for it, and reversing that split. It lives in
// the store package -- operating on the Store interface, calling only
// Metadata methods -- so JMAP, the admin REST surface, and boot-time
// resume all share one dedup-safe, crash-safe implementation, mirroring
// imapimport_removal.go's RemoveIMAPImportAccount.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// splitLocalDomain lowercases addr and splits it into its local-part and
// domain, mirroring internal/directory's splitAddress. Returns ok=false
// for anything that is not a single-@ addr-spec (defensive: a persisted
// JMAPIdentity.Email is expected to always parse, but this package has
// no address-validation dependency of its own to lean on).
func splitLocalDomain(addr string) (local, domain string, ok bool) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return "", "", false
	}
	return addr[:at], addr[at+1:], true
}

// subAccountMigrationBatchSize bounds how many messages
// RunSubAccountMigration processes before persisting progress and
// checking ctx for cancellation (REQ-IMAP-IMP-107: bounded batches, so a
// kill -9 loses at most one batch's worth of unpersisted progress, not
// the whole sweep).
const subAccountMigrationBatchSize = 200

// systemMailboxSpecs is the fixed set of system mailboxes a sub-account
// gets at separation time -- the same set store.InsertPrincipal-adjacent
// provisioning (internal/directory.provisionDefaultMailboxes) gives a new
// user, kept in sync manually since directory cannot be imported here
// (it already imports store).
var systemMailboxSpecs = []struct {
	name string
	attr MailboxAttributes
}{
	{"INBOX", MailboxAttrInbox},
	{"Sent", MailboxAttrSent},
	{"Drafts", MailboxAttrDrafts},
	{"Trash", MailboxAttrTrash},
	{"Junk", MailboxAttrJunk},
	{"Archive", MailboxAttrArchive},
}

// specialUseAttrOf returns the single special-use bit set in attrs among
// the ones systemMailboxSpecs covers, or (0, false) if none is set.
func specialUseAttrOf(attrs MailboxAttributes) (MailboxAttributes, bool) {
	for _, spec := range systemMailboxSpecs {
		if attrs&spec.attr != 0 {
			return spec.attr, true
		}
	}
	return 0, false
}

// specialUseNameOf returns the canonical name systemMailboxSpecs uses for
// attr, or "" if attr is not one of the recognised special-use bits.
func specialUseNameOf(attr MailboxAttributes) string {
	for _, spec := range systemMailboxSpecs {
		if spec.attr == attr {
			return spec.name
		}
	}
	return ""
}

// provisionSubAccountSystemMailboxes creates the sub-principal's INBOX /
// Sent / Drafts / Trash / Junk / Archive tree -- the same set a new user
// gets -- tolerating ErrConflict so a re-run after a crash (or a second
// SeparateIdentity call on an already-separated identity) is a no-op.
func provisionSubAccountSystemMailboxes(ctx context.Context, st Store, subID PrincipalID) error {
	for _, spec := range systemMailboxSpecs {
		_, err := st.Meta().InsertMailbox(ctx, Mailbox{
			PrincipalID: subID,
			Name:        spec.name,
			Attributes:  spec.attr,
		})
		if err != nil && !errors.Is(err, ErrConflict) {
			return fmt.Errorf("store: provision sub-account mailbox %q: %w", spec.name, err)
		}
	}
	return nil
}

// getOrCreateMailboxByName inserts a mailbox named name for pid, or
// returns the existing one on a name collision. Mirrors the idiom
// internal/imapimport/sync.go uses for folder-mapped and provenance-label
// mailboxes: flat names (no ParentID nesting), "/" as the conventional
// IMAP hierarchy separator baked into the name string itself when a
// caller wants a subtree (e.g. "<identity>/INBOX").
func getOrCreateMailboxByName(ctx context.Context, st Store, pid PrincipalID, name string, attrs MailboxAttributes) (Mailbox, error) {
	mb, err := st.Meta().InsertMailbox(ctx, Mailbox{PrincipalID: pid, Name: name, Attributes: attrs})
	if err == nil {
		return mb, nil
	}
	if errors.Is(err, ErrConflict) {
		return st.Meta().GetMailboxByName(ctx, pid, name)
	}
	return Mailbox{}, err
}

// resolveTargetMailbox maps a source mailbox (owned by the old principal)
// to its counterpart under newPrincipalID, creating it on demand and
// caching the result in cache for the lifetime of one sweep/removal
// pass. A source mailbox carrying one of the fixed special-use
// attributes maps to the already-provisioned system mailbox of the same
// attribute; every other mailbox maps to a same-named mailbox, get-or-
// created flat under newPrincipalID (REQ-IMAP-IMP-106: "mailbox created
// by upstream name ... folder map respected").
func resolveTargetMailbox(ctx context.Context, st Store, newPrincipalID PrincipalID, cache map[MailboxID]MailboxID, src MailboxID) (MailboxID, error) {
	if v, ok := cache[src]; ok {
		return v, nil
	}
	srcMB, err := st.Meta().GetMailboxByID(ctx, src)
	if err != nil {
		return 0, err
	}
	name := srcMB.Name
	attrs := MailboxAttributes(0)
	if attr, ok := specialUseAttrOf(srcMB.Attributes); ok {
		name = specialUseNameOf(attr)
		attrs = attr
	}
	target, err := getOrCreateMailboxByName(ctx, st, newPrincipalID, name, attrs)
	if err != nil {
		return 0, err
	}
	cache[src] = target.ID
	return target.ID, nil
}

// -- SeparateIdentity (REQ-SUBACCT-09) ---------------------------------

// SeparateIdentity creates a sub-principal owned by parentID, moves
// identityID's Identity row and every IMAP-import account attached to it
// into the sub-principal, provisions the sub-principal's system mailbox
// tree, and records a SubAccountMigration row in status pending for
// RunSubAccountMigration to sweep. It does not itself move any mail --
// callers invoke RunSubAccountMigration (directly, or via the boot-time
// ListPendingSubAccountMigrations resume) to do that.
//
// Idempotent: if identityID has already been separated, returns the
// existing SubAccountMigration row unchanged. Also tolerates a crash
// between creating the sub-principal and recording the row: on retry, an
// InsertSubPrincipal conflict on identityID's own address is resolved by
// adopting the existing sub-principal rather than failing, so a
// kill -9 immediately after the sub-principal was created does not
// strand the identity.
func SeparateIdentity(ctx context.Context, st Store, parentID PrincipalID, identityID string) (SubAccountMigration, error) {
	if identityID == "" || identityID == "default" {
		return SubAccountMigration{}, fmt.Errorf("%w: identityID must name a persisted Identity, not the synthesised default", ErrInvalidArgument)
	}

	if mig, err := st.Meta().GetSubAccountMigrationByIdentity(ctx, identityID); err == nil {
		return mig, nil
	} else if !errors.Is(err, ErrNotFound) {
		return SubAccountMigration{}, err
	}

	identity, err := st.Meta().GetJMAPIdentity(ctx, identityID)
	if err != nil {
		return SubAccountMigration{}, err
	}

	parent, err := st.Meta().GetPrincipalByID(ctx, parentID)
	if err != nil {
		return SubAccountMigration{}, err
	}
	if parent.Kind != PrincipalKindUser {
		return SubAccountMigration{}, fmt.Errorf("%w: parent %d is not an individual principal", ErrInvalidArgument, parentID)
	}
	if identity.PrincipalID != parentID {
		return SubAccountMigration{}, fmt.Errorf("%w: identity %s does not belong to principal %d", ErrInvalidArgument, identityID, parentID)
	}

	displayName := identity.Name
	if displayName == "" {
		displayName = identity.Email
	}
	sub, err := st.Meta().InsertSubPrincipal(ctx, parentID, Principal{
		CanonicalEmail: identity.Email,
		DisplayName:    displayName,
	})
	if err != nil {
		if !errors.Is(err, ErrConflict) {
			return SubAccountMigration{}, err
		}
		// A crash between a prior InsertSubPrincipal and recording the
		// SubAccountMigration row (caught above) leaves exactly one
		// sub-principal with this address and no migration row; adopt it
		// rather than fail so the retry converges.
		existing, gerr := st.Meta().GetPrincipalByEmail(ctx, identity.Email)
		if gerr != nil {
			return SubAccountMigration{}, gerr
		}
		if existing.Kind != PrincipalKindSubAccount || existing.ParentPrincipalID != parentID {
			return SubAccountMigration{}, fmt.Errorf("%w: address %s is already in use", ErrConflict, identity.Email)
		}
		sub = existing
	}

	if err := provisionSubAccountSystemMailboxes(ctx, st, sub.ID); err != nil {
		return SubAccountMigration{}, err
	}

	// Retarget any alias row for the identity's own address to the
	// sub-principal (REQ-SUBACCT-07). This is a consistency fix for the
	// alias table itself, not the delivery path: local SMTP delivery to
	// the identity's address already reaches the sub-account through
	// directory.ResolveAddress's canonical-email check, which runs
	// before alias resolution and matches the sub-principal's own
	// canonical email (set to identity.Email above). Without this step,
	// ResolveAlias, the admin alias views and the audit trail would keep
	// reporting the parent as the target of an address whose mail the
	// sub-account now receives.
	//
	// This call and the identity/account rebinds below are separate
	// idempotent steps against the store, not one transaction: each
	// tolerates being re-applied, so a crash between any two of them is
	// recovered simply by calling SeparateIdentity again (the early
	// GetSubAccountMigrationByIdentity check above only short-circuits
	// once the whole sequence, including this retarget, has completed
	// and the migration row is recorded at the end of this function; a
	// partial prior run re-executes every step, each a no-op wherever it
	// already converged). A no-op when the identity carries no alias row
	// at all.
	if local, domain, ok := splitLocalDomain(identity.Email); ok {
		if err := st.Meta().RetargetAliasesByAddress(ctx, local, domain, sub.ID); err != nil {
			return SubAccountMigration{}, err
		}
	}

	if err := st.Meta().RebindJMAPIdentityPrincipal(ctx, identityID, sub.ID); err != nil {
		return SubAccountMigration{}, err
	}

	accounts, err := st.Meta().ListIMAPImportAccountsByPrincipal(ctx, parentID)
	if err != nil {
		return SubAccountMigration{}, err
	}
	for _, acc := range accounts {
		if acc.IdentityID != identityID {
			continue
		}
		if err := st.Meta().RebindIMAPImportAccountPrincipal(ctx, acc.ID, sub.ID); err != nil {
			return SubAccountMigration{}, err
		}
	}

	return st.Meta().InsertSubAccountMigration(ctx, SubAccountMigration{
		ParentPrincipalID: parentID,
		SubPrincipalID:    sub.ID,
		IdentityID:        identityID,
		Status:            SubAccountMigrationStatusPending,
	})
}

// -- RunSubAccountMigration (REQ-SUBACCT-09, REQ-IMAP-IMP-106/107) ----

// RunSubAccountMigration sweeps every message tracked by the separated
// Identity's IMAP-import account(s) -- as of SeparateIdentity, already
// rebound to the sub-principal -- and reparents or copies it into the
// sub-principal's mailbox tree:
//
//   - a message claimed only by this import channel is reparented in
//     place (Metadata.ReparentMessage): no new message row, no blob
//     refcount change.
//   - a message also claimed by another channel (native delivery, a
//     second import account) is copied (Metadata.CopyImportedMessageToSubAccount):
//     the sub-account gets its own row: the parent keeps its own.
//
// Processes in bounded batches (subAccountMigrationBatchSize),
// persisting progress after each batch. Crash-safe and idempotent: every
// per-message step re-derives "already done" from durable state (the
// message's own PrincipalID for the move case, the message_state
// row's CopiedMessageID marker for the copy case) rather than a
// separately-persisted cursor, so re-running after a restart resumes
// exactly where it left off without duplicating or losing a message
// (REQ-IMAP-IMP-107). Returns early (status stays "running") if ctx is
// cancelled between batches.
func RunSubAccountMigration(ctx context.Context, st Store, id string) (SubAccountMigration, error) {
	mig, err := st.Meta().GetSubAccountMigration(ctx, id)
	if err != nil {
		return SubAccountMigration{}, err
	}
	if mig.Status == SubAccountMigrationStatusDone {
		return mig, nil
	}

	if err := st.Meta().UpdateSubAccountMigrationProgress(ctx, id, SubAccountMigrationStatusRunning, 0, 0, nil, ""); err != nil {
		return SubAccountMigration{}, err
	}

	accounts, err := st.Meta().ListIMAPImportAccountsByPrincipal(ctx, mig.SubPrincipalID)
	if err != nil {
		return SubAccountMigration{}, err
	}

	cache := map[MailboxID]MailboxID{}
	var sinceLastPersist int64

	for _, acc := range accounts {
		if acc.IdentityID != mig.IdentityID {
			continue
		}
		states, err := st.Meta().ListIMAPImportMessageStatesByAccount(ctx, acc.ID)
		if err != nil {
			return SubAccountMigration{}, err
		}

		// Group by HeroldMessageID (a message may be tracked by more than
		// one folder under this account) so each message is visited once,
		// mirroring purgeIMAPImportedMail's aggregation.
		targets := map[MessageID]map[MailboxID]bool{}
		copiedAlready := map[MessageID]bool{}
		var order []MessageID
		for _, s := range states {
			if s.HeroldMessageID == 0 {
				continue
			}
			if targets[s.HeroldMessageID] == nil {
				targets[s.HeroldMessageID] = map[MailboxID]bool{}
				order = append(order, s.HeroldMessageID)
			}
			targets[s.HeroldMessageID][s.HeroldMailboxID] = true
			if s.CopiedMessageID != 0 {
				copiedAlready[s.HeroldMessageID] = true
			}
		}

		total := int64(len(order))
		if err := st.Meta().UpdateSubAccountMigrationProgress(ctx, id, SubAccountMigrationStatusRunning, 0, 0, &total, ""); err != nil {
			return SubAccountMigration{}, err
		}

		for _, msgID := range order {
			if err := ctx.Err(); err != nil {
				return st.Meta().GetSubAccountMigration(ctx, id)
			}

			moved, copied, merr := migrateOneMessage(ctx, st, mig, acc, msgID, targets[msgID], copiedAlready[msgID], cache)
			if merr != nil {
				_ = st.Meta().UpdateSubAccountMigrationProgress(ctx, id, SubAccountMigrationStatusRunning, 0, 0, nil, merr.Error())
				return SubAccountMigration{}, merr
			}

			var movedDelta, copiedDelta int64
			if moved {
				movedDelta = 1
			}
			if copied {
				copiedDelta = 1
			}
			if movedDelta != 0 || copiedDelta != 0 {
				sinceLastPersist++
				if err := st.Meta().UpdateSubAccountMigrationProgress(ctx, id, SubAccountMigrationStatusRunning, movedDelta, copiedDelta, nil, ""); err != nil {
					return SubAccountMigration{}, err
				}
			}
			if sinceLastPersist >= subAccountMigrationBatchSize {
				sinceLastPersist = 0
				if err := ctx.Err(); err != nil {
					return st.Meta().GetSubAccountMigration(ctx, id)
				}
			}
		}
	}

	// Every message tracked by this Identity's import account(s) has now
	// been reparented or copied. Re-threading the sub-account picks up
	// the copied messages (inserted with SkipThreading) into their own
	// thread groups, matching the bulk-import convention.
	if _, err := st.Meta().RethreadPrincipal(ctx, mig.SubPrincipalID); err != nil {
		return SubAccountMigration{}, err
	}

	if err := st.Meta().UpdateSubAccountMigrationProgress(ctx, id, SubAccountMigrationStatusDone, 0, 0, nil, ""); err != nil {
		return SubAccountMigration{}, err
	}
	return st.Meta().GetSubAccountMigration(ctx, id)
}

// migrateOneMessage performs the crash-safe per-message step: skip if
// already done, otherwise decide move vs. copy (REQ-IMAP-IMP-103 dedup
// rule) and perform it. aTargets is the set of mailboxes this account
// itself placed msgID into (across every folder it tracks it under);
// copiedAlready is precomputed from the message_state row(s)' durable
// marker. Returns which of (moved, copied) happened, both false when the
// message was already done by a prior run.
func migrateOneMessage(ctx context.Context, st Store, mig SubAccountMigration, acc IMAPImportAccount, msgID MessageID, aTargets map[MailboxID]bool, copiedAlready bool, cache map[MailboxID]MailboxID) (moved, copied bool, err error) {
	msg, err := st.Meta().GetMessage(ctx, msgID)
	if errors.Is(err, ErrNotFound) {
		// Gone entirely (e.g. destroyed by an unrelated path); nothing to
		// migrate.
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if msg.PrincipalID == mig.SubPrincipalID {
		// Already reparented by a prior run.
		return false, false, nil
	}
	if copiedAlready {
		return false, false, nil
	}

	prov := acc.ProvenanceMailboxID
	allStates, err := st.Meta().ListIMAPImportMessageStatesByMessage(ctx, msgID)
	if err != nil {
		return false, false, err
	}
	soleClaim := true
	for _, s := range allStates {
		if s.AccountID != acc.ID {
			soleClaim = false
			break
		}
	}
	if soleClaim {
		for _, mm := range msg.Mailboxes {
			if mm.MailboxID == prov {
				continue
			}
			if !aTargets[mm.MailboxID] {
				soleClaim = false
				break
			}
		}
	}

	if soleClaim {
		mailboxMoves := map[MailboxID]MailboxID{}
		for _, mm := range msg.Mailboxes {
			target, terr := resolveTargetMailbox(ctx, st, mig.SubPrincipalID, cache, mm.MailboxID)
			if terr != nil {
				return false, false, terr
			}
			mailboxMoves[mm.MailboxID] = target
		}
		if err := st.Meta().ReparentMessage(ctx, msgID, mig.SubPrincipalID, mailboxMoves); err != nil {
			return false, false, err
		}
		return true, false, nil
	}

	// Copy: only the memberships this account itself is responsible for
	// (its folder-mapped mailbox(es) plus its provenance label) move into
	// the sub-account; every other membership is a foreign claim and
	// stays with the parent untouched.
	newMsg := msg
	newMsg.PrincipalID = mig.SubPrincipalID
	newMsg.ThreadID = 0
	var newTargets []MessageMailbox
	for _, mm := range msg.Mailboxes {
		if mm.MailboxID != prov && !aTargets[mm.MailboxID] {
			continue
		}
		target, terr := resolveTargetMailbox(ctx, st, mig.SubPrincipalID, cache, mm.MailboxID)
		if terr != nil {
			return false, false, terr
		}
		newTargets = append(newTargets, MessageMailbox{
			MailboxID: target,
			Flags:     mm.Flags,
			Keywords:  mm.Keywords,
		})
	}
	if len(newTargets) == 0 {
		// This account's own claim was already detached by some other
		// process (e.g. a concurrent purge); nothing left to copy.
		return false, false, nil
	}
	result, err := st.Meta().CopyImportedMessageToSubAccount(ctx, CopyImportedMessageRequest{
		AccountID:       acc.ID,
		SourceMessageID: msgID,
		NewMessage:      newMsg,
		Targets:         newTargets,
	})
	if err != nil {
		return false, false, err
	}
	if result.AlreadyDone {
		return false, false, nil
	}
	return false, true, nil
}

// -- RemoveSubAccount (REQ-SUBACCT-10) ---------------------------------

// RemoveSubAccount deletes subID, honouring a keep-or-purge choice for
// its mail:
//
//   - purge: destroys the sub-principal and everything it owns (its
//     mailbox tree, its mail, its Identity, its IMAP-import accounts) via
//     Metadata.DeletePrincipal, which already cascades sub-account
//     cleanup atomically (mirrors testSubPrincipals_DeleteParentCascades).
//
//   - keep: moves every message in the sub-principal's mailbox tree back
//     under the parent, into a "<label>/<leaf>" flat-named subtree (label
//     = the separated Identity's address when known, else the
//     sub-principal's own address), moves the Identity and its
//     IMAP-import account(s) back to the parent, then deletes the
//     (now mail-empty) sub-principal.
//
// Both variants are idempotent on partial completion: purge is a single
// DeletePrincipal transaction (all-or-nothing already); keep re-derives
// its remaining work from current mailbox contents on every call (a
// message once reparented is no longer listed under the source mailbox,
// so a resumed run naturally skips it), and a subID that no longer
// exists (a prior run already finished) is treated as already-done
// rather than an error.
func RemoveSubAccount(ctx context.Context, st Store, subID PrincipalID, purge bool) error {
	sub, err := st.Meta().GetPrincipalByID(ctx, subID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sub.Kind != PrincipalKindSubAccount {
		return fmt.Errorf("%w: %d is not a sub-account", ErrInvalidArgument, subID)
	}

	parent, err := st.Meta().GetSubPrincipalParent(ctx, subID)
	if err != nil {
		return err
	}

	// Retarget any alias row SeparateIdentity pointed at the sub-account
	// back to the parent (REQ-SUBACCT-07): a consistency fix for the
	// alias table symmetric with SeparateIdentity's own retarget (see
	// that function's comment for why the delivery path itself does not
	// depend on this). Runs before either branch touches the
	// sub-principal row, and specifically ahead of the purge
	// DeletePrincipal call below: target_principal carries an ON DELETE
	// CASCADE, so an alias row still pointed at subID would be destroyed
	// along with the sub-principal rather than preserved and retargeted.
	//
	// A separate idempotent step, not part of one transaction with the
	// deletion/rebind work that follows: a crash partway through is
	// recovered by calling RemoveSubAccount again, which re-derives
	// "already done" from current store state at every step (this
	// retarget included -- re-running it against rows already pointed
	// at parent.ID is a no-op) rather than from a persisted cursor. A
	// no-op when no such alias row exists.
	if local, domain, ok := splitLocalDomain(sub.CanonicalEmail); ok {
		if err := st.Meta().RetargetAliasesByAddress(ctx, local, domain, parent.ID); err != nil {
			return err
		}
	}

	if purge {
		if err := st.Meta().DeletePrincipal(ctx, subID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	}

	mig, migErr := st.Meta().GetSubAccountMigrationBySubPrincipal(ctx, subID)
	label := sub.CanonicalEmail
	if migErr == nil {
		if idn, ierr := st.Meta().GetJMAPIdentity(ctx, mig.IdentityID); ierr == nil && idn.Email != "" {
			label = idn.Email
		}
	} else if !errors.Is(migErr, ErrNotFound) {
		return migErr
	}

	// ReparentMessage moves every membership named in one call atomically
	// and reassigns the message's owning principal in the same
	// transaction; calling it once per (message, mailbox) pair instead of
	// once per message -- with only that one mailbox in the map -- would
	// flip the message's principal to the parent after the first
	// membership moves, and every later membership of the same message
	// would then look "already done" (same principal) and never move,
	// stranding it under the sub-account forever. So this enumerates
	// every message's *complete* current set of sub-side memberships
	// first (a read-only pass; nothing has moved yet) and moves each
	// message with a single ReparentMessage call carrying all of them.
	//
	// Idempotent/resumable by construction: a message already moved by
	// an earlier, interrupted run is no longer listed in any sub
	// mailbox, so a re-run's enumeration pass simply omits it.
	mailboxes, err := st.Meta().ListMailboxes(ctx, subID)
	if err != nil {
		return err
	}
	moves := map[MessageID]map[MailboxID]MailboxID{}
	for _, mb := range mailboxes {
		target, terr := getOrCreateMailboxByName(ctx, st, parent.ID, label+"/"+mb.Name, 0)
		if terr != nil {
			return terr
		}

		var after UID
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			msgs, err := st.Meta().ListMessages(ctx, mb.ID, MessageFilter{AfterUID: after, Limit: 500})
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				break
			}
			for _, m := range msgs {
				if moves[m.ID] == nil {
					moves[m.ID] = map[MailboxID]MailboxID{}
				}
				moves[m.ID][mb.ID] = target.ID
				after = m.UID
			}
			if len(msgs) < 500 {
				break
			}
		}
	}
	for msgID, mailboxMoves := range moves {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := st.Meta().ReparentMessage(ctx, msgID, parent.ID, mailboxMoves); err != nil {
			return err
		}
	}

	if migErr == nil {
		if err := st.Meta().RebindJMAPIdentityPrincipal(ctx, mig.IdentityID, parent.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	} else {
		identities, err := st.Meta().ListJMAPIdentities(ctx, subID)
		if err != nil {
			return err
		}
		for _, idn := range identities {
			if err := st.Meta().RebindJMAPIdentityPrincipal(ctx, idn.ID, parent.ID); err != nil {
				return err
			}
		}
	}

	accounts, err := st.Meta().ListIMAPImportAccountsByPrincipal(ctx, subID)
	if err != nil {
		return err
	}
	for _, acc := range accounts {
		if err := st.Meta().RebindIMAPImportAccountPrincipal(ctx, acc.ID, parent.ID); err != nil {
			return err
		}
	}

	if err := st.Meta().DeletePrincipal(ctx, subID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}
