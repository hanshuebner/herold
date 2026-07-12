package protoimap

// ACL — RFC 4314 IMAP ACL extension.
//
// This file implements the wire-level handlers for SETACL, DELETEACL,
// GETACL, MYRIGHTS, and LISTRIGHTS, plus a small requireRights helper
// the rest of the protoimap session calls before mutating or reading
// shared mailboxes. The 16-bit ACLRights mask lives in store/types_phase2.go;
// here we map between that mask and the RFC 4314 letter sequence
// "lrswipkxtea".
//
// Visibility rules (RFC 4314 §2):
//   - The mailbox owner has full rights implicitly. GETACL surfaces an
//     "owner" row alongside whatever explicit ACL rows the operator has
//     stored; SETACL on a mailbox the caller does not own requires the
//     "a" (admin) right.
//   - The "anyone" pseudo-identifier is encoded as a NULL PrincipalID at
//     the store.MailboxACL DTO layer (PrincipalID == nil); the underlying
//     grant row carries store.GrantSubjectAnyone (epic #210). On the wire
//     we render it as the literal token "anyone".
//
// LISTRIGHTS policy (RFC 4314 §3.7 leaves this to server policy):
//   - Required rights for any sharing: "l" (lookup) and "r" (read).
//     Without these the grantee cannot meaningfully see or open the
//     mailbox at all.
//   - Optional rights: "s" "w" "i" "p" "k" "x" "t" "e" "a" — every other
//     defined letter, granted individually.
//
// Capability advertisement: the "ACL" token is added to the CAPABILITY
// response in session.go once these handlers are wired (STANDARDS rule
// 10 — advertise only when implemented).
//
// Unified grant substrate (epic #182/#186, extended to full RFC 4314
// fidelity and sole-source status by epic #210, REQ-AC-50..53): every
// rights check and every shared-mailbox visibility scan in this file goes
// through the grant substrate -- store.Meta()'s SetMailboxACL / GetMailboxACL
// / RemoveMailboxACL / ListMailboxesAccessibleBy are a DTO layer over
// `mailbox`-kind grant rows (internal/store/types_phase2.go), and
// internal/authz.ResolveMailboxRights is the enforcement seam that adds
// implicit ownership and the superadmin short-circuit on top. There is no
// second (legacy mailbox_acl table) source to union: a `mailbox` grant row
// written outside IMAP (the mailing-list archive read grant, REQ-AC-52; a
// future admin grants surface) is honoured by the exact same read path as a
// SETACL-written row.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/aclcodec"
	"github.com/hanshuebner/herold/internal/authz"
	"github.com/hanshuebner/herold/internal/store"
)

// encodeRights renders a rights mask as the canonical RFC 4314 letter
// sequence. Delegates to internal/aclcodec so the IMAP wire and the
// admin REST surface never disagree on which bit maps to which letter.
func encodeRights(r store.ACLRights) string {
	return aclcodec.Encode(r)
}

// decodeRights parses an RFC 4314 §2.2 modifyrights string. Leading
// "+" or "-" prefixes are stripped by the caller before decoding (this
// helper just maps letters → mask). Unknown letters return an error so
// SETACL surfaces "BAD" rather than silently dropping bits.
//
// RFC 4314 §2.1.1 obsoleted "c" / "d" virtual rights. We accept them
// as aliases on input so older clients keep working: c → k|x, d → x|t|e
// (the obsolete "create" and "delete" composites). The expansion is
// done in this wrapper so the shared aclcodec package can reject
// unknown letters strictly for the REST surface, which does not need
// the RFC 2086 back-compat hooks.
func decodeRights(s string) (store.ACLRights, error) {
	expanded := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'c':
			expanded = append(expanded, 'k', 'x')
		case 'd':
			expanded = append(expanded, 'x', 't', 'e')
		default:
			expanded = append(expanded, s[i])
		}
	}
	out, err := aclcodec.Decode(string(expanded))
	if err != nil {
		return 0, fmt.Errorf("protoimap: %w", err)
	}
	return out, nil
}

// resolveACLIdentifier maps an RFC 4314 identifier to a *PrincipalID.
// "anyone" is the special-case nil pointer; any other token is treated
// as a canonical email and looked up in the directory. Unknown
// principals return ErrNotFound so the caller can surface
// NO [NOPERM] / NO without leaking which identifiers exist.
func (ses *session) resolveACLIdentifier(ctx context.Context, id string) (*store.PrincipalID, error) {
	if strings.EqualFold(id, "anyone") {
		return nil, nil
	}
	p, err := ses.s.store.Meta().GetPrincipalByEmail(ctx, strings.ToLower(id))
	if err != nil {
		return nil, err
	}
	pid := p.ID
	return &pid, nil
}

// requireRights centralises the ACL gate. It reads the caller's
// effective rights against mb (owner is implicit ACLRightsAll; any grant
// row contributes additively; the "anyone" grant applies to everyone)
// and returns nil when (mask & need) == need. Callers map a non-nil
// return to NO [NOPERM].
//
// ses.pid being the owner of mb short-circuits to "all rights" so the
// owner does not need a grant row to operate on their own mailbox, and so
// this fast path never depends on a store read for the common case.
func (ses *session) requireRights(ctx context.Context, mb store.Mailbox, need store.ACLRights) error {
	if mb.PrincipalID == ses.pid {
		return nil
	}
	have, err := ses.effectiveRights(ctx, mb)
	if err != nil {
		return err
	}
	if have&need != need {
		return errInsufficientRights
	}
	return nil
}

// errInsufficientRights is the sentinel requireRights returns when the
// caller's effective mask does not cover the required bits. Mapped to
// "NO [NOPERM]" by the call sites; not exported.
var errInsufficientRights = errors.New("protoimap: insufficient ACL rights")

// effectiveRights returns ses.pid's full RFC 4314 rights mask on mb via
// internal/authz.ResolveMailboxRights (epic #210) — the sole enforcement
// path, covering implicit ownership, the superadmin short-circuit, and
// every applicable `mailbox` grant row (the caller's own row plus any
// "anyone" row), fail-closed on a store error (REQ-AC-12).
func (ses *session) effectiveRights(ctx context.Context, mb store.Mailbox) (store.ACLRights, error) {
	principal, err := ses.s.store.Meta().GetPrincipalByID(ctx, ses.pid)
	if err != nil {
		return 0, err
	}
	return authz.ResolveMailboxRights(ctx, ses.s.store.Meta(), principal, mb)
}

// accessibleMailboxes is the single call site every SELECT / STATUS /
// APPEND / LIST / ACL-lookup path uses to enumerate mailboxes ses.pid can
// reach that it does not own: every mailbox with a `mailbox` grant row
// giving ses.pid the lookup right, directly or via the "anyone" subject
// (REQ-AC-53 — "one check ... across IMAP"; store.Meta().ListMailboxesAccessibleBy
// is the grant-backed DTO, epic #210). Superadmin's blanket visibility (if
// any) is a separate, pre-existing concern this function does not touch.
func (ses *session) accessibleMailboxes(ctx context.Context) ([]store.Mailbox, error) {
	return ses.s.store.Meta().ListMailboxesAccessibleBy(ctx, ses.pid)
}

// -----------------------------------------------------------------------------
// SETACL <mailbox> <identifier> [+|-]<rights>
// -----------------------------------------------------------------------------

func (ses *session) handleSETACL(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.lookupACLMailbox(ctx, c.Tag, c.ACLMailbox)
	if err != nil {
		return nil
	}
	// Only the owner or a principal with the admin ("a") right may
	// modify ACL rows. RFC 4314 §3.1.
	if err := ses.requireRights(ctx, mb, store.ACLRightAdmin); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to administer ACL")
	}
	pid, err := ses.resolveACLIdentifier(ctx, c.ACLIdentifier)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "unknown identifier")
	}
	mod := c.ACLRights
	op := byte('=')
	if len(mod) > 0 && (mod[0] == '+' || mod[0] == '-') {
		op = mod[0]
		mod = mod[1:]
	}
	delta, derr := decodeRights(mod)
	if derr != nil {
		return ses.resp.taggedBAD(c.Tag, "", derr.Error())
	}
	// For "+" / "-" we read the existing MANUAL rights and adjust; for "="
	// we replace wholesale. RFC 4314 §3.1. The delta applies to the manual
	// (local/acl-migration) portion only -- GetMailboxACLManual, not
	// GetMailboxACL -- so an idp:<provider>-granted right is never baked
	// into the durable manual row: "+t" on a grantee with only an
	// idp:lr grant writes local:t (not local:lrt), and a later idp:
	// revocation leaves exactly that t behind, no leaked l/r. "-r" can
	// only remove from the manual portion; if r is solely idp-granted, the
	// grantee's effective rights still show r afterward (revoking an
	// idp:-conferred right requires changing the IdP claim mapping, not
	// SETACL).
	target := delta
	if op != '=' {
		current, err := ses.s.store.Meta().GetMailboxACLManual(ctx, mb.ID, pid)
		if err != nil {
			return ses.resp.taggedNO(c.Tag, "", "ACL read failed")
		}
		switch op {
		case '+':
			target = current | delta
		case '-':
			target = current &^ delta
		}
	}
	if target == 0 {
		// Empty rights with "=" semantics deletes the row, matching
		// RFC 4314 §3.1.1: "If the resulting set of rights is empty,
		// the entry is removed".
		if err := ses.s.store.Meta().RemoveMailboxACL(ctx, mb.ID, pid); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			return ses.resp.taggedNO(c.Tag, "", "ACL remove failed")
		}
	} else {
		if err := ses.s.store.Meta().SetMailboxACL(ctx, mb.ID, pid, target, ses.pid); err != nil {
			return ses.resp.taggedNO(c.Tag, "", "ACL write failed")
		}
	}
	ses.logger.Info("protoimap: SETACL",
		"activity", "audit",
		"mailbox", c.ACLMailbox,
		"identifier", c.ACLIdentifier,
		"rights", encodeRights(target),
	)
	return ses.resp.taggedOK(c.Tag, "", "SETACL completed")
}

// -----------------------------------------------------------------------------
// DELETEACL <mailbox> <identifier>
// -----------------------------------------------------------------------------

func (ses *session) handleDELETEACL(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.lookupACLMailbox(ctx, c.Tag, c.ACLMailbox)
	if err != nil {
		return nil
	}
	if err := ses.requireRights(ctx, mb, store.ACLRightAdmin); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to administer ACL")
	}
	pid, err := ses.resolveACLIdentifier(ctx, c.ACLIdentifier)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "unknown identifier")
	}
	if err := ses.s.store.Meta().RemoveMailboxACL(ctx, mb.ID, pid); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return ses.resp.taggedNO(c.Tag, "", "ACL remove failed")
	}
	ses.logger.Info("protoimap: DELETEACL",
		"activity", "audit",
		"mailbox", c.ACLMailbox,
		"identifier", c.ACLIdentifier,
	)
	return ses.resp.taggedOK(c.Tag, "", "DELETEACL completed")
}

// -----------------------------------------------------------------------------
// GETACL <mailbox>
// -----------------------------------------------------------------------------

func (ses *session) handleGETACL(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.lookupACLMailbox(ctx, c.Tag, c.ACLMailbox)
	if err != nil {
		return nil
	}
	// RFC 4314 §3.4: GETACL itself requires the "a" admin right (or
	// owner). We do not surface ACL details to grantees.
	if err := ses.requireRights(ctx, mb, store.ACLRightAdmin); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to read ACL")
	}
	rows, err := ses.s.store.Meta().GetMailboxACL(ctx, mb.ID)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "ACL read failed")
	}
	// Resolve owner email so the implicit row renders sensibly.
	owner, err := ses.s.store.Meta().GetPrincipalByID(ctx, mb.PrincipalID)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "owner lookup failed")
	}
	type entry struct {
		id     string
		rights string
	}
	// GetMailboxACL already returns the unified view -- every `mailbox`
	// grant row on mb, whatever wrote it (SETACL, the admin REST surface,
	// a mailing-list archive read grant, REQ-AC-52) -- so there is a single
	// source to render here, no legacy/grant-substrate distinction (epic
	// #210).
	entries := []entry{{id: owner.CanonicalEmail, rights: encodeRights(store.ACLRightsAll)}}
	for _, row := range rows {
		var name string
		if row.PrincipalID == nil {
			name = "anyone"
		} else {
			p, perr := ses.s.store.Meta().GetPrincipalByID(ctx, *row.PrincipalID)
			if perr != nil {
				// Stale row pointing at a deleted principal: skip
				// silently rather than leaking the deletion mid-list.
				continue
			}
			name = p.CanonicalEmail
		}
		entries = append(entries, entry{id: name, rights: encodeRights(row.Rights)})
	}
	// Stable order so tests can assert canonically.
	sort.SliceStable(entries[1:], func(i, j int) bool {
		return entries[1+i].id < entries[1+j].id
	})
	var sb strings.Builder
	sb.WriteString("ACL ")
	sb.WriteString(imapQuote(mb.Name))
	for _, e := range entries {
		sb.WriteByte(' ')
		sb.WriteString(imapQuote(e.id))
		sb.WriteByte(' ')
		sb.WriteString(imapQuote(e.rights))
	}
	if err := ses.resp.untagged(sb.String()); err != nil {
		return err
	}
	return ses.resp.taggedOK(c.Tag, "", "GETACL completed")
}

// -----------------------------------------------------------------------------
// MYRIGHTS <mailbox>
// -----------------------------------------------------------------------------

func (ses *session) handleMYRIGHTS(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.lookupACLMailbox(ctx, c.Tag, c.ACLMailbox)
	if err != nil {
		return nil
	}
	have, err := ses.effectiveRights(ctx, mb)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "ACL read failed")
	}
	// MYRIGHTS without lookup is meaningless — RFC 4314 §3.5 requires the
	// caller to have at least one right on the mailbox to receive a reply.
	// We do not leak existence to principals with zero rights. The owner
	// always resolves to ACLRightsAll (never zero), so this only denies a
	// genuinely rights-less non-owner.
	if have == 0 {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "no rights on mailbox")
	}
	if err := ses.resp.untagged(fmt.Sprintf("MYRIGHTS %s %s",
		imapQuote(mb.Name), imapQuote(encodeRights(have)))); err != nil {
		return err
	}
	return ses.resp.taggedOK(c.Tag, "", "MYRIGHTS completed")
}

// -----------------------------------------------------------------------------
// LISTRIGHTS <mailbox> <identifier>
// -----------------------------------------------------------------------------

func (ses *session) handleLISTRIGHTS(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.lookupACLMailbox(ctx, c.Tag, c.ACLMailbox)
	if err != nil {
		return nil
	}
	// LISTRIGHTS surfaces the policy itself, which is operator-
	// configurable elsewhere; we still gate it behind the admin
	// right so policy is not leaked to grantees who did not earn
	// "a".
	if err := ses.requireRights(ctx, mb, store.ACLRightAdmin); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to read ACL policy")
	}
	// Resolve the identifier so we can echo a canonical name (the
	// server is free to canonicalise per RFC 4314 §3.7).
	pid, err := ses.resolveACLIdentifier(ctx, c.ACLIdentifier)
	echoed := c.ACLIdentifier
	if err == nil && pid != nil {
		if p, perr := ses.s.store.Meta().GetPrincipalByID(ctx, *pid); perr == nil {
			echoed = p.CanonicalEmail
		}
	}
	// Server policy: required = "lr" (a sensible minimum to share
	// at all); optional = every other defined letter, granted
	// individually. RFC 4314 §3.7 leaves this to server policy and
	// requires the optional list to be one space-separated atom per
	// independently-grantable right.
	required := "lr"
	optionalBits := store.ACLRightsAll &^ (store.ACLRightLookup | store.ACLRightRead)
	var optional []string
	for _, e := range aclcodec.LetterTable {
		if optionalBits&e.Bit != 0 {
			optional = append(optional, string(e.Letter))
		}
	}
	parts := []string{
		"LISTRIGHTS",
		imapQuote(mb.Name),
		imapQuote(echoed),
		imapQuote(required),
	}
	for _, o := range optional {
		parts = append(parts, imapQuote(o))
	}
	if err := ses.resp.untagged(strings.Join(parts, " ")); err != nil {
		return err
	}
	return ses.resp.taggedOK(c.Tag, "", "LISTRIGHTS completed")
}

// lookupACLMailbox resolves the mailbox the ACL command names. Lookup
// is owner-scoped: ses.pid's own mailboxes resolve directly; for shared
// mailboxes the caller passes the canonical Name and we scan the
// principal's accessible-mailbox set. Returns ErrNotFound (mapped to
// NO [NONEXISTENT]) when no candidate matches.
//
// This helper hides the asymmetry between "I own the mailbox" and "I
// have an ACL row on someone else's mailbox" from the per-command
// handlers above.
func (ses *session) lookupACLMailbox(ctx context.Context, tag, name string) (store.Mailbox, error) {
	canonical := canonicalMailboxName(name)
	mb, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonical)
	if err == nil {
		return mb, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		_ = ses.resp.taggedNO(tag, "", "mailbox lookup failed")
		return store.Mailbox{}, err
	}
	// Search shared mailboxes the principal can reach via the mailbox-grant
	// substrate (REQ-AC-53).
	shared, lerr := ses.accessibleMailboxes(ctx)
	if lerr != nil {
		_ = ses.resp.taggedNO(tag, "", "mailbox lookup failed")
		return store.Mailbox{}, lerr
	}
	for _, sm := range shared {
		if sm.Name == canonical {
			return sm, nil
		}
	}
	_ = ses.resp.taggedNO(tag, "NONEXISTENT", "mailbox not found")
	return store.Mailbox{}, store.ErrNotFound
}
