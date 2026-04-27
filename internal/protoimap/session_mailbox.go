package protoimap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
)

// requireAuth returns true if the session is at least authenticated.
func (ses *session) requireAuth(tag string) bool {
	if ses.state == stateNotAuthed {
		_ = ses.resp.taggedBAD(tag, "", "not authenticated")
		return false
	}
	return true
}

func (ses *session) requireSelected(tag string) bool {
	if !ses.requireAuth(tag) {
		return false
	}
	if ses.state != stateSelected {
		_ = ses.resp.taggedBAD(tag, "", "not in SELECTED state")
		return false
	}
	return true
}

// canonicalMailboxName normalises a mailbox name — we accept INBOX
// case-insensitively and preserve case otherwise.
func canonicalMailboxName(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

func (ses *session) handleSELECT(ctx context.Context, c *Command, readOnly bool) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	name := canonicalMailboxName(c.Mailbox)
	mb, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, name)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return ses.resp.taggedNO(c.Tag, "", "select failed")
		}
		// Owner did not find a mailbox by this name; consult the
		// ACL-shared set so a grantee can SELECT mailboxes someone
		// else owns. RFC 4314 §4 — SELECT requires "lr" and we map a
		// missing mailbox / missing rights to the same NO [NOPERM]
		// or NO [NONEXISTENT] outcome a Cyrus/Dovecot peer would
		// emit, depending on which side failed first.
		shared, lerr := ses.s.store.Meta().ListMailboxesAccessibleBy(ctx, ses.pid)
		if lerr != nil {
			return ses.resp.taggedNO(c.Tag, "", "select failed")
		}
		var found bool
		for _, sm := range shared {
			if sm.Name == name {
				mb, found = sm, true
				break
			}
		}
		if !found {
			return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
		}
	}
	// RFC 4314 §4: SELECT/EXAMINE require both "l" (lookup) and "r"
	// (read). The owner short-circuits via requireRights.
	if err := ses.requireRights(ctx, mb, store.ACLRightLookup|store.ACLRightRead); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to SELECT mailbox")
	}
	msgs, err := ses.s.store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{WithEnvelope: true})
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "select failed")
	}
	// Compute untagged responses.
	existing := len(msgs)
	var unseen int
	for i, m := range msgs {
		if m.Flags&store.MessageFlagSeen == 0 {
			unseen = i + 1
			break
		}
	}
	_ = ses.resp.untagged("FLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft)")
	_ = ses.resp.untagged(fmt.Sprintf("%d EXISTS", existing))
	_ = ses.resp.untagged("0 RECENT")
	if unseen > 0 {
		_ = ses.resp.untagged(fmt.Sprintf("OK [UNSEEN %d] first unseen", unseen))
	}
	_ = ses.resp.untagged(fmt.Sprintf("OK [UIDVALIDITY %d] UIDVALIDITY", mb.UIDValidity))
	_ = ses.resp.untagged(fmt.Sprintf("OK [UIDNEXT %d] Predicted next UID", mb.UIDNext))
	_ = ses.resp.untagged("OK [PERMANENTFLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft \\*)] Limited")

	// CONDSTORE / QRESYNC SELECT options (RFC 7162 §3.1). The
	// CONDSTORE option promotes the session into MODSEQ-aware mode and
	// emits HIGHESTMODSEQ in the SELECT body. QRESYNC further enables
	// (uidvalidity modseq known-uids seq-match-data) resync responses.
	var qrArgs qresyncSelectArgs
	wantQResync := false
	if c.SelectOptions != nil {
		if _, ok := c.SelectOptions["CONDSTORE"]; ok {
			ses.enableCondstore()
		}
		if v, ok := c.SelectOptions["QRESYNC"]; ok {
			ses.enableQresync()
			wantQResync = true
			qrArgs = parseQresyncArgs(v)
		}
	}
	if ses.condstoreActive() {
		_ = ses.resp.untagged(fmt.Sprintf("OK [HIGHESTMODSEQ %d] Highest", mb.HighestModSeq))
	}

	ses.selMu.Lock()
	ses.sel = selectedMailbox{
		id:          mb.ID,
		name:        mb.Name,
		uidValidity: mb.UIDValidity,
		uidNext:     mb.UIDNext,
		msgs:        msgs,
		readOnly:    readOnly,
	}
	ses.cs.selectedHighestModSeq = mb.HighestModSeq
	ses.selMu.Unlock()
	ses.state = stateSelected

	if wantQResync {
		if err := ses.emitQresyncSelectResponses(ctx, mb, qrArgs); err != nil {
			return err
		}
	}

	code := "READ-WRITE"
	if readOnly {
		code = "READ-ONLY"
	}
	return ses.resp.taggedOK(c.Tag, code, c.Op+" completed")
}

// parseQresyncArgs takes the raw "(uidvalidity modseq known-uids
// seq-match-data)" argument string captured by parseSelectOptionList
// and decodes the up-to-four positional fields. Missing fields yield
// zero values; the SELECT-with-QRESYNC handler treats those as "client
// did not supply".
func parseQresyncArgs(raw string) qresyncSelectArgs {
	args := qresyncSelectArgs{}
	s := strings.TrimSpace(raw)
	if s == "" {
		return args
	}
	if s[0] == '(' && s[len(s)-1] == ')' {
		s = s[1 : len(s)-1]
	}
	// Tokenise positional fields; the third (known-uids) and fourth
	// (seq-match-data) may be parenthesised lists.
	pos := 0
	field := 0
	for pos < len(s) {
		// skip whitespace
		for pos < len(s) && s[pos] == ' ' {
			pos++
		}
		if pos >= len(s) {
			break
		}
		var tok string
		if s[pos] == '(' {
			depth := 0
			start := pos
			for pos < len(s) {
				c := s[pos]
				if c == '(' {
					depth++
				} else if c == ')' {
					depth--
					if depth == 0 {
						pos++
						break
					}
				}
				pos++
			}
			tok = s[start:pos]
		} else {
			start := pos
			for pos < len(s) && s[pos] != ' ' {
				pos++
			}
			tok = s[start:pos]
		}
		switch field {
		case 0:
			if n, err := strconv.ParseUint(tok, 10, 32); err == nil {
				args.clientUIDValidity = store.UIDValidity(n)
			}
		case 1:
			if n, err := strconv.ParseUint(tok, 10, 64); err == nil {
				args.clientModSeq = store.ModSeq(n)
			}
		case 2:
			body := strings.Trim(tok, "()")
			if body != "" {
				if set, err := parseNumSetString(body, true); err == nil {
					if u, ok := set.(imap.UIDSet); ok {
						args.knownUIDs = u
					}
				}
			}
		case 3:
			// seq-match-data is reserved; ignored.
		}
		field++
	}
	return args
}

func (ses *session) handleCREATE(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	name := canonicalMailboxName(c.Mailbox)
	// RFC 4314 §4: CREATE requires the "k" (create-mailbox) right on
	// the parent mailbox when one exists. For top-level mailboxes
	// owned by ses.pid, the principal is implicitly authorised. When
	// creating "Foo/Bar" against a parent that someone else owns
	// (shared-namespace child), ses.pid must hold "k" on the parent.
	//
	// Limitation: Phase-2 v1 parent lookup uses ses.pid only —
	// shared-namespace child paths under another principal's owned
	// mailbox require explicit ACL grants on the parent and are
	// looked up under the parent's owner. A deeper namespace model
	// (e.g. a "#shared/<owner>/..." prefix) is out of v2 scope; JMAP
	// Mailbox handlers do not drive IMAP namespace shapes.
	if i := strings.LastIndex(name, "/"); i > 0 {
		parentName := name[:i]
		if parent, perr := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, parentName); perr == nil {
			if err := ses.requireRights(ctx, parent, store.ACLRightCreateMailbox); err != nil {
				return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights on parent mailbox")
			}
		}
	}
	// RFC 6154 §5.2 CREATE-SPECIAL-USE: optional "(USE (\Drafts \Sent
	// ...))" suffix. Translate the wire names into the
	// MailboxAttributes bitfield so subsequent LIST responses can echo
	// the special-use attributes back.
	attrs := specialUseAttrs(c.CreateSpecialUse)
	_, err := ses.s.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: ses.pid,
		Name:        name,
		Attributes:  attrs,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return ses.resp.taggedNO(c.Tag, "ALREADYEXISTS", "mailbox exists")
		}
		return ses.resp.taggedNO(c.Tag, "", "create failed")
	}
	return ses.resp.taggedOK(c.Tag, "", "CREATE completed")
}

// specialUseAttrs maps RFC 6154 use-attribute names (with the leading
// backslash preserved) into the store.MailboxAttributes bitfield.
// Unknown names are ignored — STANDARDS rule 10 says we must accept
// only what we implement, and unknown attributes cannot be stored
// faithfully.
func specialUseAttrs(uses []string) store.MailboxAttributes {
	var out store.MailboxAttributes
	for _, u := range uses {
		switch strings.ToLower(u) {
		case "\\sent":
			out |= store.MailboxAttrSent
		case "\\drafts":
			out |= store.MailboxAttrDrafts
		case "\\trash":
			out |= store.MailboxAttrTrash
		case "\\junk":
			out |= store.MailboxAttrJunk
		case "\\archive":
			out |= store.MailboxAttrArchive
		case "\\flagged":
			out |= store.MailboxAttrFlagged
		}
	}
	return out
}

func (ses *session) handleDELETE(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.Mailbox))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
	}
	// RFC 4314 §4: DELETE requires the "x" (delete-mailbox) right.
	// Owner short-circuits.
	if err := ses.requireRights(ctx, mb, store.ACLRightDeleteMailbox); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to DELETE")
	}
	if err := ses.s.store.Meta().DeleteMailbox(ctx, mb.ID); err != nil {
		return ses.resp.taggedNO(c.Tag, "", "delete failed")
	}
	return ses.resp.taggedOK(c.Tag, "", "DELETE completed")
}

func (ses *session) handleRENAME(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.RenameOldName))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
	}
	// RFC 4314 §4: RENAME is a delete + create. Require "x" on the
	// source and (when the destination is under a different parent)
	// "k" on the parent. Owner short-circuits.
	if err := ses.requireRights(ctx, mb, store.ACLRightDeleteMailbox); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to RENAME")
	}
	if i := strings.LastIndex(c.RenameNewName, "/"); i > 0 {
		parentName := c.RenameNewName[:i]
		if parent, perr := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, parentName); perr == nil {
			if err := ses.requireRights(ctx, parent, store.ACLRightCreateMailbox); err != nil {
				return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights on destination parent")
			}
		}
	}
	if err := ses.s.store.Meta().RenameMailbox(ctx, mb.ID, c.RenameNewName); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return ses.resp.taggedNO(c.Tag, "ALREADYEXISTS", "destination exists")
		}
		return ses.resp.taggedNO(c.Tag, "", "rename failed")
	}
	return ses.resp.taggedOK(c.Tag, "", "RENAME completed")
}

func (ses *session) handleSUBSCRIBE(ctx context.Context, c *Command, subscribe bool) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.Mailbox))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
	}
	// RFC 4314 §4: SUBSCRIBE / UNSUBSCRIBE require the "l" lookup
	// right (otherwise the principal cannot meaningfully see the
	// mailbox). Owner short-circuits.
	if err := ses.requireRights(ctx, mb, store.ACLRightLookup); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to (un)subscribe")
	}
	if err := ses.s.store.Meta().SetMailboxSubscribed(ctx, mb.ID, subscribe); err != nil {
		return ses.resp.taggedNO(c.Tag, "", "subscribe failed")
	}
	op := "SUBSCRIBE"
	if !subscribe {
		op = "UNSUBSCRIBE"
	}
	return ses.resp.taggedOK(c.Tag, "", op+" completed")
}

// matchMailboxPattern matches an IMAP LIST pattern against a name. '*'
// matches anything; '%' matches anything that does not include a hierarchy
// separator ('/').
func matchMailboxPattern(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	if strings.ContainsAny(pattern, "*%") {
		return globMatch(pattern, name)
	}
	return pattern == name
}

// globMatch implements IMAP LIST pattern matching: '*' matches any
// sequence of characters (including the hierarchy separator), '%' matches
// any sequence that does not cross the separator.
func globMatch(pattern, s string) bool {
	return wildMatch(pattern, s)
}

func wildMatch(pat, s string) bool {
	if pat == "" {
		return s == ""
	}
	if pat[0] == '*' {
		rest := pat[1:]
		for i := 0; i <= len(s); i++ {
			if wildMatch(rest, s[i:]) {
				return true
			}
		}
		return false
	}
	if pat[0] == '%' {
		rest := pat[1:]
		for i := 0; i <= len(s); i++ {
			if wildMatch(rest, s[i:]) {
				return true
			}
			if i < len(s) && s[i] == '/' {
				return false
			}
		}
		return false
	}
	if len(s) == 0 {
		return false
	}
	if pat[0] == s[0] {
		return wildMatch(pat[1:], s[1:])
	}
	return false
}

func (ses *session) handleLIST(ctx context.Context, c *Command, lsub bool) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	boxes, err := ses.s.store.Meta().ListMailboxes(ctx, ses.pid)
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "list failed")
	}
	// RFC 4314 §4 / §5.2: LIST/LSUB output must include shared
	// mailboxes the principal can see via their ACL rows. The store
	// already returns only mailboxes whose ACL grants the "l" lookup
	// right, so we just append the result. The "Shared/" namespace
	// hint is operator convention; visibility is governed by ACL.
	shared, serr := ses.s.store.Meta().ListMailboxesAccessibleBy(ctx, ses.pid)
	if serr == nil {
		// De-dup by ID against owned set in case the store returns a
		// mailbox the principal both owns and has an ACL row on.
		seen := make(map[store.MailboxID]struct{}, len(boxes))
		for _, mb := range boxes {
			seen[mb.ID] = struct{}{}
		}
		for _, mb := range shared {
			if _, ok := seen[mb.ID]; ok {
				continue
			}
			boxes = append(boxes, mb)
		}
	}
	ref := strings.TrimSuffix(c.ListReference, "/")
	patterns := c.ListPatterns
	if len(patterns) == 0 {
		patterns = []string{c.ListMailbox}
	}
	for i, p := range patterns {
		if ref != "" && !strings.HasPrefix(p, "/") {
			patterns[i] = ref + "/" + p
		}
	}
	tag := "LIST"
	if lsub {
		tag = "LSUB"
	}
	if len(patterns) == 1 && patterns[0] == "" {
		// LIST "" "" asks for the hierarchy delimiter.
		_ = ses.resp.untagged(tag + ` (\Noselect) "/" ""`)
		return ses.resp.taggedOK(c.Tag, "", tag+" completed")
	}
	// LIST-EXTENDED select-options: SUBSCRIBED restricts output to
	// subscribed mailboxes (mirroring LSUB); REMOTE / RECURSIVEMATCH
	// are accepted but currently no-ops.
	selectSubscribed := false
	for _, opt := range c.ListSelectOpts {
		switch strings.ToUpper(opt) {
		case "SUBSCRIBED":
			selectSubscribed = true
		}
	}
	// LIST-STATUS RETURN option: when STATUS is named, append a
	// matching "* STATUS" untagged response per matched mailbox.
	statusItems := []string(nil)
	for _, opt := range c.ListReturnOpts {
		if strings.HasPrefix(strings.ToUpper(opt), "STATUS") {
			// The opt has shape "STATUS(MESSAGES UNSEEN ...)" — parse
			// the parenthesised body. parseAtomParenList already
			// consumed the parens; here the items are not preserved
			// because the ListReturnOpts slice is a flat atom list.
			// As a heuristic, when the client emitted a RETURN
			// (STATUS (...)) form we expand to a default item set.
			statusItems = []string{"MESSAGES", "UIDNEXT", "UIDVALIDITY", "UNSEEN"}
		}
	}
	for _, mb := range boxes {
		if (lsub || selectSubscribed) && mb.Attributes&store.MailboxAttrSubscribed == 0 {
			continue
		}
		matched := false
		for _, p := range patterns {
			if matchMailboxPattern(p, mb.Name) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		attrs := []string{}
		if mb.Attributes&store.MailboxAttrSent != 0 {
			attrs = append(attrs, "\\Sent")
		}
		if mb.Attributes&store.MailboxAttrDrafts != 0 {
			attrs = append(attrs, "\\Drafts")
		}
		if mb.Attributes&store.MailboxAttrTrash != 0 {
			attrs = append(attrs, "\\Trash")
		}
		if mb.Attributes&store.MailboxAttrJunk != 0 {
			attrs = append(attrs, "\\Junk")
		}
		if mb.Attributes&store.MailboxAttrArchive != 0 {
			attrs = append(attrs, "\\Archive")
		}
		if mb.Attributes&store.MailboxAttrSubscribed != 0 {
			attrs = append(attrs, "\\Subscribed")
		}
		_ = ses.resp.untagged(fmt.Sprintf(`%s (%s) "/" %s`, tag, strings.Join(attrs, " "), imapQuote(mb.Name)))
		if len(statusItems) > 0 {
			// LIST-STATUS (RFC 5819): emit a "* STATUS" untagged
			// response for each matched mailbox.
			ses.emitListStatus(ctx, mb, statusItems)
		}
	}
	return ses.resp.taggedOK(c.Tag, "", tag+" completed")
}

// emitListStatus is the LIST-STATUS extension's piggyback STATUS line.
// Failures here are best-effort — the parent LIST response already
// landed on the wire; a missing STATUS line just means the client falls
// back to a separate STATUS round-trip.
func (ses *session) emitListStatus(ctx context.Context, mb store.Mailbox, items []string) {
	msgs, err := ses.s.store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{})
	if err != nil {
		return
	}
	var unseen int64
	var size int64
	for _, m := range msgs {
		if m.Flags&store.MessageFlagSeen == 0 {
			unseen++
		}
		size += m.Size
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		switch strings.ToUpper(it) {
		case "MESSAGES":
			parts = append(parts, fmt.Sprintf("MESSAGES %d", len(msgs)))
		case "UIDNEXT":
			parts = append(parts, fmt.Sprintf("UIDNEXT %d", mb.UIDNext))
		case "UIDVALIDITY":
			parts = append(parts, fmt.Sprintf("UIDVALIDITY %d", mb.UIDValidity))
		case "UNSEEN":
			parts = append(parts, fmt.Sprintf("UNSEEN %d", unseen))
		case "SIZE":
			parts = append(parts, fmt.Sprintf("SIZE %d", size))
		case "HIGHESTMODSEQ":
			parts = append(parts, fmt.Sprintf("HIGHESTMODSEQ %d", mb.HighestModSeq))
		}
	}
	_ = ses.resp.untagged(fmt.Sprintf(`STATUS %s (%s)`, imapQuote(mb.Name), strings.Join(parts, " ")))
}

func (ses *session) handleSTATUS(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	canonical := canonicalMailboxName(c.Mailbox)
	mb, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonical)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return ses.resp.taggedNO(c.Tag, "", "status failed")
		}
		// Try shared mailboxes.
		shared, lerr := ses.s.store.Meta().ListMailboxesAccessibleBy(ctx, ses.pid)
		if lerr != nil {
			return ses.resp.taggedNO(c.Tag, "", "status failed")
		}
		var found bool
		for _, sm := range shared {
			if sm.Name == canonical {
				mb, found = sm, true
				break
			}
		}
		if !found {
			return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
		}
	}
	// RFC 4314 §4: STATUS requires "lr" (same as SELECT/EXAMINE) so
	// principals without read rights cannot probe message counts.
	if err := ses.requireRights(ctx, mb, store.ACLRightLookup|store.ACLRightRead); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights for STATUS")
	}
	msgs, err := ses.s.store.Meta().ListMessages(ctx, mb.ID, store.MessageFilter{WithEnvelope: true})
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "status failed")
	}
	var unseen int64
	var size int64
	for _, m := range msgs {
		if m.Flags&store.MessageFlagSeen == 0 {
			unseen++
		}
		size += m.Size
	}
	parts := make([]string, 0, len(c.StatusItems))
	for _, item := range c.StatusItems {
		switch strings.ToUpper(item) {
		case "MESSAGES":
			parts = append(parts, fmt.Sprintf("MESSAGES %d", len(msgs)))
		case "UIDNEXT":
			parts = append(parts, fmt.Sprintf("UIDNEXT %d", mb.UIDNext))
		case "UIDVALIDITY":
			parts = append(parts, fmt.Sprintf("UIDVALIDITY %d", mb.UIDValidity))
		case "UNSEEN":
			parts = append(parts, fmt.Sprintf("UNSEEN %d", unseen))
		case "RECENT":
			parts = append(parts, "RECENT 0")
		case "SIZE":
			parts = append(parts, fmt.Sprintf("SIZE %d", size))
		case "HIGHESTMODSEQ":
			parts = append(parts, fmt.Sprintf("HIGHESTMODSEQ %d", mb.HighestModSeq))
		}
	}
	_ = ses.resp.untagged(fmt.Sprintf(`STATUS %s (%s)`, imapQuote(mb.Name), strings.Join(parts, " ")))
	return ses.resp.taggedOK(c.Tag, "", "STATUS completed")
}

func (ses *session) handleAPPEND(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	canonical := canonicalMailboxName(c.Mailbox)
	mb, err := ses.s.store.Meta().GetMailboxByName(ctx, ses.pid, canonical)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return ses.resp.taggedNO(c.Tag, "", "append failed")
		}
		// Try shared mailboxes a grantee can APPEND into.
		shared, lerr := ses.s.store.Meta().ListMailboxesAccessibleBy(ctx, ses.pid)
		if lerr != nil {
			return ses.resp.taggedNO(c.Tag, "TRYCREATE", "mailbox not found")
		}
		var found bool
		for _, sm := range shared {
			if sm.Name == canonical {
				mb, found = sm, true
				break
			}
		}
		if !found {
			return ses.resp.taggedNO(c.Tag, "TRYCREATE", "mailbox not found")
		}
	}
	// RFC 4314 §4: APPEND requires the "i" insert right. Owner
	// short-circuits.
	if err := ses.requireRights(ctx, mb, store.ACLRightInsert); err != nil {
		return ses.resp.taggedNO(c.Tag, "NOPERM", "insufficient rights to APPEND")
	}
	// MULTIAPPEND (RFC 3502): more than one literal arrived in this
	// APPEND. Dispatch to the batch path; the single-message form
	// preserves its existing wire response (one UID in APPENDUID).
	if len(c.AppendItems) > 1 {
		return ses.applyMultiAppend(ctx, c, mb)
	}
	blobRef, err := ses.s.store.Blobs().Put(ctx, bytes.NewReader(c.AppendData))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "", "blob write failed")
	}
	flags := flagMaskFromNames(c.AppendFlags)
	kw := keywordsFromNames(c.AppendFlags)
	env := parseEnvelope(c.AppendData)
	now := ses.s.clk.Now()
	internal := c.AppendInternal
	if internal.IsZero() {
		internal = now
	}
	msg := store.Message{
		MailboxID:    mb.ID,
		Flags:        flags,
		Keywords:     kw,
		InternalDate: internal,
		ReceivedAt:   now,
		Size:         int64(len(c.AppendData)),
		Blob:         blobRef,
		Envelope:     env,
	}
	insertTimer := observe.StartStoreOp("insert_message")
	uid, _, err := ses.s.store.Meta().InsertMessage(ctx, msg)
	insertTimer.Done()
	if err != nil {
		if errors.Is(err, store.ErrQuotaExceeded) {
			return ses.resp.taggedNO(c.Tag, "OVERQUOTA", "quota exceeded")
		}
		return ses.resp.taggedNO(c.Tag, "", "append failed")
	}
	code := fmt.Sprintf("APPENDUID %d %d", mb.UIDValidity, uid)
	return ses.resp.taggedOK(c.Tag, code, "APPEND completed")
}

// flagMaskFromNames translates "\\Seen \\Deleted ..." into the store's
// bitmask; unknown names are treated as keywords, not flags.
func flagMaskFromNames(names []string) store.MessageFlags {
	var f store.MessageFlags
	for _, n := range names {
		switch strings.ToLower(n) {
		case "\\seen":
			f |= store.MessageFlagSeen
		case "\\answered":
			f |= store.MessageFlagAnswered
		case "\\flagged":
			f |= store.MessageFlagFlagged
		case "\\deleted":
			f |= store.MessageFlagDeleted
		case "\\draft":
			f |= store.MessageFlagDraft
		case "\\recent":
			f |= store.MessageFlagRecent
		}
	}
	return f
}

func keywordsFromNames(names []string) []string {
	var out []string
	for _, n := range names {
		if !strings.HasPrefix(n, "\\") {
			out = append(out, strings.ToLower(n))
		}
	}
	return out
}

// parseEnvelope extracts the RFC 5322 headers into a store.Envelope using
// net/mail. Parse errors are soft: we just populate whatever we could.
func parseEnvelope(raw []byte) store.Envelope {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return store.Envelope{}
	}
	env := store.Envelope{
		Subject:   msg.Header.Get("Subject"),
		From:      msg.Header.Get("From"),
		To:        msg.Header.Get("To"),
		Cc:        msg.Header.Get("Cc"),
		Bcc:       msg.Header.Get("Bcc"),
		ReplyTo:   msg.Header.Get("Reply-To"),
		MessageID: strings.Trim(msg.Header.Get("Message-ID"), "<>"),
		InReplyTo: strings.Trim(msg.Header.Get("In-Reply-To"), "<>"),
	}
	if ds := msg.Header.Get("Date"); ds != "" {
		if t, err := mail.ParseDate(ds); err == nil {
			env.Date = t
		}
	}
	return env
}

// flagNamesFromMask renders a store.MessageFlags + keywords list into a
// space-separated flag-list suitable for FETCH/STORE untagged responses.
func flagNamesFromMask(f store.MessageFlags, kw []string) []string {
	var out []string
	if f&store.MessageFlagSeen != 0 {
		out = append(out, "\\Seen")
	}
	if f&store.MessageFlagAnswered != 0 {
		out = append(out, "\\Answered")
	}
	if f&store.MessageFlagFlagged != 0 {
		out = append(out, "\\Flagged")
	}
	if f&store.MessageFlagDeleted != 0 {
		out = append(out, "\\Deleted")
	}
	if f&store.MessageFlagDraft != 0 {
		out = append(out, "\\Draft")
	}
	out = append(out, kw...)
	return out
}

// convertEnvelope builds an imap.Envelope from the store cache.
func convertEnvelope(s store.Envelope) imap.Envelope {
	return imap.Envelope{
		Date:      s.Date,
		Subject:   s.Subject,
		From:      parseAddrList(s.From),
		To:        parseAddrList(s.To),
		Cc:        parseAddrList(s.Cc),
		Bcc:       parseAddrList(s.Bcc),
		ReplyTo:   parseAddrList(s.ReplyTo),
		MessageID: s.MessageID,
		InReplyTo: splitInReplyTo(s.InReplyTo),
	}
}

func parseAddrList(raw string) []imap.Address {
	if raw == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(raw)
	if err != nil {
		return nil
	}
	out := make([]imap.Address, 0, len(addrs))
	for _, a := range addrs {
		at := strings.LastIndexByte(a.Address, '@')
		var mbox, host string
		if at >= 0 {
			mbox = a.Address[:at]
			host = a.Address[at+1:]
		}
		out = append(out, imap.Address{Name: a.Name, Mailbox: mbox, Host: host})
	}
	return out
}

func splitInReplyTo(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Fields(s)
	for i := range parts {
		parts[i] = strings.Trim(parts[i], "<>")
	}
	return parts
}

// ensureMsgTime clamps zero timestamps to the Clock's current value so
// responses never emit the Unix epoch by accident.
func (ses *session) ensureMsgTime(t time.Time) time.Time {
	if t.IsZero() {
		return ses.s.clk.Now()
	}
	return t
}
