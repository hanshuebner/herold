package protoimap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"
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
	mb, err := ses.s.mailbox.GetMailboxByName(ctx, ses.pid, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
		}
		return ses.resp.taggedNO(c.Tag, "", "select failed")
	}
	msgs, err := ses.s.mailbox.ListMessages(ctx, mb.ID)
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
	_ = ses.resp.untagged(fmt.Sprintf("FLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft)"))
	_ = ses.resp.untagged(fmt.Sprintf("%d EXISTS", existing))
	_ = ses.resp.untagged("0 RECENT")
	if unseen > 0 {
		_ = ses.resp.untagged(fmt.Sprintf("OK [UNSEEN %d] first unseen", unseen))
	}
	_ = ses.resp.untagged(fmt.Sprintf("OK [UIDVALIDITY %d] UIDVALIDITY", mb.UIDValidity))
	_ = ses.resp.untagged(fmt.Sprintf("OK [UIDNEXT %d] Predicted next UID", mb.UIDNext))
	_ = ses.resp.untagged("OK [PERMANENTFLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft \\*)] Limited")

	ses.selMu.Lock()
	ses.sel = selectedMailbox{
		id:          mb.ID,
		name:        mb.Name,
		uidValidity: mb.UIDValidity,
		uidNext:     mb.UIDNext,
		msgs:        msgs,
		readOnly:    readOnly,
	}
	ses.selMu.Unlock()
	ses.state = stateSelected

	code := "READ-WRITE"
	if readOnly {
		code = "READ-ONLY"
	}
	return ses.resp.taggedOK(c.Tag, code, c.Op+" completed")
}

func (ses *session) handleCREATE(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	name := canonicalMailboxName(c.Mailbox)
	_, err := ses.s.store.Meta().InsertMailbox(ctx, store.Mailbox{
		PrincipalID: ses.pid,
		Name:        name,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return ses.resp.taggedNO(c.Tag, "ALREADYEXISTS", "mailbox exists")
		}
		return ses.resp.taggedNO(c.Tag, "", "create failed")
	}
	return ses.resp.taggedOK(c.Tag, "", "CREATE completed")
}

func (ses *session) handleDELETE(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.s.mailbox.GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.Mailbox))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
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
	mb, err := ses.s.mailbox.GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.RenameOldName))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
	}
	if err := ses.s.mailbox.RenameMailbox(ctx, mb.ID, c.RenameNewName); err != nil {
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
	mb, err := ses.s.mailbox.GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.Mailbox))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
	}
	if err := ses.s.mailbox.SetMailboxSubscribed(ctx, mb.ID, subscribe); err != nil {
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
	ref := strings.TrimSuffix(c.ListReference, "/")
	pattern := c.ListMailbox
	if ref != "" && !strings.HasPrefix(pattern, "/") {
		pattern = ref + "/" + pattern
	}
	if pattern == "" {
		// LIST "" "" asks for the hierarchy delimiter.
		_ = ses.resp.untagged(`LIST (\Noselect) "/" ""`)
		return ses.resp.taggedOK(c.Tag, "", "LIST completed")
	}
	tag := "LIST"
	if lsub {
		tag = "LSUB"
	}
	for _, mb := range boxes {
		if lsub && mb.Attributes&store.MailboxAttrSubscribed == 0 {
			continue
		}
		if !matchMailboxPattern(pattern, mb.Name) {
			continue
		}
		attrs := []string{}
		if mb.Attributes&store.MailboxAttrInbox != 0 {
			// No special attribute for INBOX proper.
		}
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
	}
	return ses.resp.taggedOK(c.Tag, "", tag+" completed")
}

func (ses *session) handleSTATUS(ctx context.Context, c *Command) error {
	if !ses.requireAuth(c.Tag) {
		return nil
	}
	mb, err := ses.s.mailbox.GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.Mailbox))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "NONEXISTENT", "mailbox not found")
	}
	msgs, err := ses.s.mailbox.ListMessages(ctx, mb.ID)
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
	mb, err := ses.s.mailbox.GetMailboxByName(ctx, ses.pid, canonicalMailboxName(c.Mailbox))
	if err != nil {
		return ses.resp.taggedNO(c.Tag, "TRYCREATE", "mailbox not found")
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
	uid, _, err := ses.s.store.Meta().InsertMessage(ctx, msg)
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
