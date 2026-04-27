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
//     the store layer (store.MailboxACL.PrincipalID == nil). On the wire
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

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// rightsLetterTable is the canonical RFC 4314 §2.1 vocabulary in the
// order the spec lists them. The order matters for canonical wire-form
// output (GETACL / MYRIGHTS / LISTRIGHTS): a deterministic letter
// sequence simplifies test assertions and matches typical Cyrus/Dovecot
// rendering.
var rightsLetterTable = []struct {
	letter byte
	bit    store.ACLRights
}{
	{'l', store.ACLRightLookup},
	{'r', store.ACLRightRead},
	{'s', store.ACLRightSeen},
	{'w', store.ACLRightWrite},
	{'i', store.ACLRightInsert},
	{'p', store.ACLRightPost},
	{'k', store.ACLRightCreateMailbox},
	{'x', store.ACLRightDeleteMailbox},
	{'t', store.ACLRightDeleteMessage},
	{'e', store.ACLRightExpunge},
	{'a', store.ACLRightAdmin},
}

// encodeRights renders a rights mask as the canonical RFC 4314 letter
// sequence. The letters appear in the lrswipkxtea order regardless of
// the bit positions in the mask, so test assertions stay stable as new
// bits are added.
func encodeRights(r store.ACLRights) string {
	var sb strings.Builder
	for _, e := range rightsLetterTable {
		if r&e.bit != 0 {
			sb.WriteByte(e.letter)
		}
	}
	return sb.String()
}

// decodeRights parses an RFC 4314 §2.2 modifyrights string. Leading
// "+" or "-" prefixes are stripped by the caller before decoding (this
// helper just maps letters → mask). Unknown letters return an error so
// SETACL surfaces "BAD" rather than silently dropping bits.
func decodeRights(s string) (store.ACLRights, error) {
	var out store.ACLRights
	for i := 0; i < len(s); i++ {
		c := s[i]
		matched := false
		for _, e := range rightsLetterTable {
			if e.letter == c {
				out |= e.bit
				matched = true
				break
			}
		}
		if !matched {
			// RFC 4314 §2.1.1 obsoleted "c" / "d" virtual rights.
			// We accept them as aliases on input so older clients
			// keep working: c → k|x, d → x|t|e (the obsolete
			// "create" and "delete" composites). New scripts SHOULD
			// use the modern letters; SETACL persists the expanded
			// composite, not the alias.
			switch c {
			case 'c':
				out |= store.ACLRightCreateMailbox | store.ACLRightDeleteMailbox
			case 'd':
				out |= store.ACLRightDeleteMailbox |
					store.ACLRightDeleteMessage |
					store.ACLRightExpunge
			default:
				return 0, fmt.Errorf("protoimap: unknown ACL right %q", c)
			}
		}
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
// effective rights against mb (owner is implicit ACLRightsAll; any ACL
// row contributes additively; the "anyone" row applies to everyone)
// and returns nil when (mask & need) == need. Callers map a non-nil
// return to NO [NOPERM].
//
// ses.pid being the owner of mb short-circuits to "all rights" so the
// owner does not need an ACL row to operate on their own mailbox.
func (ses *session) requireRights(ctx context.Context, mb store.Mailbox, need store.ACLRights) error {
	if mb.PrincipalID == ses.pid {
		return nil
	}
	have, err := ses.effectiveRights(ctx, mb.ID)
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

// effectiveRights returns the OR of every ACL row applicable to
// ses.pid on mailboxID: the explicit per-principal row plus any
// "anyone" row. Callers MUST NOT call this when ses.pid is the
// mailbox owner — owners get the implicit ACLRightsAll mask via
// requireRights.
func (ses *session) effectiveRights(ctx context.Context, mailboxID store.MailboxID) (store.ACLRights, error) {
	rows, err := ses.s.store.Meta().GetMailboxACL(ctx, mailboxID)
	if err != nil {
		return 0, err
	}
	var have store.ACLRights
	for _, row := range rows {
		if row.PrincipalID == nil {
			// "anyone" row applies to every authenticated principal.
			have |= row.Rights
			continue
		}
		if *row.PrincipalID == ses.pid {
			have |= row.Rights
		}
	}
	return have, nil
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
	// For "+" / "-" we read the existing row and adjust; for "=" we
	// replace wholesale. RFC 4314 §3.1.
	target := delta
	if op != '=' {
		rows, err := ses.s.store.Meta().GetMailboxACL(ctx, mb.ID)
		if err != nil {
			return ses.resp.taggedNO(c.Tag, "", "ACL read failed")
		}
		var current store.ACLRights
		for _, row := range rows {
			if (row.PrincipalID == nil) == (pid == nil) &&
				(pid == nil || *row.PrincipalID == *pid) {
				current = row.Rights
				break
			}
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
	entries := []entry{{id: owner.CanonicalEmail, rights: encodeRights(store.ACLRightsAll)}}
	for _, row := range rows {
		var name string
		if row.PrincipalID == nil {
			name = "anyone"
		} else {
			p, perr := ses.s.store.Meta().GetPrincipalByID(ctx, *row.PrincipalID)
			if perr != nil {
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
	var have store.ACLRights
	if mb.PrincipalID == ses.pid {
		have = store.ACLRightsAll
	} else {
		have, err = ses.effectiveRights(ctx, mb.ID)
		if err != nil {
			return ses.resp.taggedNO(c.Tag, "", "ACL read failed")
		}
		// MYRIGHTS without lookup is meaningless — RFC 4314 §3.5
		// requires the caller to have at least one right on the
		// mailbox to receive a reply. We do not leak existence to
		// principals with zero rights.
		if have == 0 {
			return ses.resp.taggedNO(c.Tag, "NOPERM", "no rights on mailbox")
		}
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
	for _, e := range rightsLetterTable {
		if optionalBits&e.bit != 0 {
			optional = append(optional, string(e.letter))
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
	// Search shared mailboxes the principal can reach.
	shared, lerr := ses.s.store.Meta().ListMailboxesAccessibleBy(ctx, ses.pid)
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
