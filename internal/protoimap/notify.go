// NOTIFY (RFC 5465) — server-side push for clients that want to monitor
// many mailboxes without holding one IDLE per mailbox.
//
// Wire shape: NOTIFY SET [STATUS] (selector event-set) (selector
// event-set) ... | NOTIFY NONE. Each selector names a set of mailboxes
// (SELECTED, INBOX, PERSONAL, SUBSCRIBED, MAILBOXES (...) , SUBTREE
// (...)) and the events the client wants on those mailboxes
// (MessageNew, MessageExpunge, FlagChange, MailboxName,
// SubscriptionChange, AnnotationChange, MailboxMetadataChange,
// ServerMetadataChange).
//
// Implementation. NOTIFY shares the per-principal change feed with IDLE
// and JMAP push (the post-Q5 EntityKind / EntityID / ParentEntityID /
// Op shape). The session evaluates each change-feed entry against the
// active subscription's selector + event-set; matches turn into untagged
// responses (FETCH for selected mailbox, STATUS for non-selected
// mailboxes). The selector evaluator is pure session-local logic — no
// per-subscription state lives in the store.
//
// Phase 2 Wave 2.2 ships SET / NONE plus the SELECTED, MAILBOXES,
// SUBTREE, INBOX, PERSONAL, SUBSCRIBED selectors and the MessageNew,
// MessageExpunge, FlagChange, MailboxName, SubscriptionChange events.
// Annotation / metadata events depend on the not-yet-merged annotation
// surface and are deferred. The capability advertisement is gated on
// this implementation; until the dispatcher path lands the CAPABILITY
// list does NOT include NOTIFY (STANDARDS rule 10).

package protoimap

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hanshuebner/herold/internal/store"
)

// notifySelectorKind enumerates the RFC 5465 §6 selector forms the
// session evaluates.
type notifySelectorKind uint8

const (
	notifySelectorNone notifySelectorKind = iota
	notifySelectorSelected
	notifySelectorSelectedDelayed
	notifySelectorInbox
	notifySelectorPersonal
	notifySelectorSubscribed
	notifySelectorMailboxes
	notifySelectorSubtree
)

// notifyEventKind is the bitfield of RFC 5465 §5 events a selector
// subscribes to. The set is intentionally narrow (Phase 2 Wave 2.2
// scope); future wave can add AnnotationChange / *MetadataChange bits
// without reshaping this type.
type notifyEventKind uint8

const (
	// notifyEventMessageNew matches inserts (ChangeOpCreated on email).
	notifyEventMessageNew notifyEventKind = 1 << iota
	// notifyEventMessageExpunge matches deletes (ChangeOpDestroyed on
	// email).
	notifyEventMessageExpunge
	// notifyEventFlagChange matches updates (ChangeOpUpdated on email).
	notifyEventFlagChange
	// notifyEventMailboxName matches mailbox CREATE / RENAME / DELETE
	// (ChangeOp* on mailbox).
	notifyEventMailboxName
	// notifyEventSubscriptionChange matches SUBSCRIBE / UNSUBSCRIBE
	// state flips (ChangeOpUpdated on mailbox where the change is a
	// subscription bit toggle — distinguishing this from a plain
	// rename requires reading the mailbox row, which the dispatch
	// loop already does for mailbox events).
	notifyEventSubscriptionChange
)

// notifyHasEvent tests one bit; returns true when set.
func notifyHasEvent(mask, want notifyEventKind) bool { return mask&want == want }

// notifySubscription is one (selector, event-set) pair from a NOTIFY SET
// command. A session carries zero or more subscriptions; the dispatcher
// emits an event when any subscription matches.
type notifySubscription struct {
	kind notifySelectorKind
	// names is populated for MAILBOXES / SUBTREE; unused otherwise.
	names []string
	// events is the bit-or of subscribed event kinds.
	events notifyEventKind
	// withStatus indicates the client requested STATUS-on-NewMessage
	// untagged responses for the selector's mailboxes (RFC 5465 §6
	// "STATUS" modifier). Phase 2 ships the parser bit; the dispatcher
	// emits STATUS replies for the relevant mailboxes when the bit is
	// set and the change targets a non-selected mailbox.
	withStatus bool
}

// notifyState is the per-session NOTIFY configuration. Zero value means
// NOTIFY is off (the default; the session emits no NOTIFY-driven
// untagged responses unless the client opted in).
type notifyState struct {
	// active is true after a successful NOTIFY SET. NOTIFY NONE clears
	// the slice and flips active back to false.
	active bool
	// subs is the parsed subscription list from the most recent
	// NOTIFY SET. Replaces wholesale on each SET.
	subs []notifySubscription
}

// matchesChange returns the first subscription that matches the given
// change feed entry, plus the event kind that triggered the match. Used
// by the dispatcher in IDLE / NOTIFY background loops to decide whether
// to emit an untagged response.
func (ns *notifyState) matchesChange(ch store.StateChange, mb *store.Mailbox, selectedID store.MailboxID) (sub *notifySubscription, ev notifyEventKind, ok bool) {
	if !ns.active {
		return nil, 0, false
	}
	for i := range ns.subs {
		s := &ns.subs[i]
		ev := classifyChange(ch)
		if ev == 0 || !notifyHasEvent(s.events, ev) {
			continue
		}
		if !ns.selectorMatches(s, ch, mb, selectedID) {
			continue
		}
		return s, ev, true
	}
	return nil, 0, false
}

// classifyChange maps a (Kind, Op) pair from the change feed into the
// NOTIFY event-bit vocabulary. Returns 0 when the change has no NOTIFY
// surface.
func classifyChange(ch store.StateChange) notifyEventKind {
	switch ch.Kind {
	case store.EntityKindEmail:
		switch ch.Op {
		case store.ChangeOpCreated:
			return notifyEventMessageNew
		case store.ChangeOpDestroyed:
			return notifyEventMessageExpunge
		case store.ChangeOpUpdated:
			return notifyEventFlagChange
		}
	case store.EntityKindMailbox:
		switch ch.Op {
		case store.ChangeOpCreated, store.ChangeOpDestroyed:
			return notifyEventMailboxName
		case store.ChangeOpUpdated:
			// We cannot tell rename vs subscribe-flip from the change
			// row alone; emit both bits and let the per-event mask
			// filter decide. The dispatcher reads the mailbox row to
			// disambiguate before formatting the response.
			return notifyEventMailboxName | notifyEventSubscriptionChange
		}
	}
	return 0
}

// selectorMatches reports whether sub's selector covers the affected
// mailbox. mb is the mailbox row for ch (already loaded by the
// dispatcher). selectedID is the session's currently-SELECTed mailbox
// (zero when no SELECT is active).
func (ns *notifyState) selectorMatches(sub *notifySubscription, ch store.StateChange, mb *store.Mailbox, selectedID store.MailboxID) bool {
	if mb == nil {
		return false
	}
	switch sub.kind {
	case notifySelectorSelected, notifySelectorSelectedDelayed:
		return selectedID != 0 && mb.ID == selectedID
	case notifySelectorInbox:
		return strings.EqualFold(mb.Name, "INBOX")
	case notifySelectorPersonal:
		return true // only one principal scope per session — every owned mailbox qualifies
	case notifySelectorSubscribed:
		return mb.Attributes&store.MailboxAttrSubscribed != 0
	case notifySelectorMailboxes:
		for _, n := range sub.names {
			if n == mb.Name || strings.EqualFold(n, mb.Name) {
				return true
			}
		}
		return false
	case notifySelectorSubtree:
		for _, n := range sub.names {
			if n == mb.Name || strings.HasPrefix(mb.Name, n+"/") || strings.EqualFold(n, mb.Name) {
				return true
			}
		}
		return false
	}
	return false
}

// parseNotifyArgs parses the NOTIFY command arguments out of a raw
// command line. Returns either (cleared=true, nil, nil) for NOTIFY NONE
// or (cleared=false, subs, nil) for NOTIFY SET. Validation errors fall
// through as a non-nil error; the dispatcher maps them to a tagged BAD
// response.
func parseNotifyArgs(raw string) (cleared bool, subs []notifySubscription, err error) {
	// raw begins "<tag> NOTIFY ...". Strip tag + verb.
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return false, nil, errors.New("notify: missing argument")
	}
	if !strings.EqualFold(fields[1], "NOTIFY") {
		return false, nil, errors.New("notify: not a NOTIFY command")
	}
	rest := strings.TrimSpace(strings.Join(fields[2:], " "))
	switch {
	case strings.EqualFold(rest, "NONE"):
		return true, nil, nil
	case strings.HasPrefix(strings.ToUpper(rest), "SET"):
		body := strings.TrimSpace(rest[3:])
		// Optional "STATUS" modifier per selector group.
		// TODO(2.2-coord): the parallel imap-advanced agent's NOTIFY
		// helper returned a 2-tuple; threaded through here so the
		// package builds while their wire grammar lands. Original
		// signature returns ([]notifySubscription, error).
		s, perr := parseNotifySetBody(body)
		return false, s, perr
	}
	return false, nil, fmt.Errorf("notify: unknown form %q", rest)
}

// parseNotifySetBody walks one or more "(filter event-set)" groups. The
// shape is intentionally tolerant — clients in the wild emit slight
// variations on the optional STATUS modifier and we accept the common
// ones. Returns the parsed subscriptions or an error on malformed input.
func parseNotifySetBody(body string) ([]notifySubscription, error) {
	body = strings.TrimSpace(body)
	// Optional "STATUS" prefix marks all selectors in the SET as wanting
	// STATUS pushes.
	statusFlag := false
	if up := strings.ToUpper(body); strings.HasPrefix(up, "STATUS ") {
		statusFlag = true
		body = strings.TrimSpace(body[len("STATUS "):])
	}
	var subs []notifySubscription
	pos := 0
	for pos < len(body) {
		// skip whitespace
		for pos < len(body) && (body[pos] == ' ' || body[pos] == '\t') {
			pos++
		}
		if pos >= len(body) {
			break
		}
		if body[pos] != '(' {
			return nil, fmt.Errorf("notify: expected '(' at offset %d", pos)
		}
		// Find matching ')'.
		depth := 0
		start := pos
		for pos < len(body) {
			c := body[pos]
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
		if depth != 0 {
			return nil, errors.New("notify: unbalanced parentheses")
		}
		group := body[start+1 : pos-1]
		sub, err := parseNotifyGroup(group)
		if err != nil {
			return nil, err
		}
		sub.withStatus = statusFlag
		subs = append(subs, sub)
	}
	return subs, nil
}

// parseNotifyGroup parses one "<filter> <event-set>" group into a
// notifySubscription. The filter is a selector token (with optional
// MAILBOXES / SUBTREE list); the event-set is "(EVT EVT ...)" or a bare
// event token.
func parseNotifyGroup(group string) (notifySubscription, error) {
	g := strings.TrimSpace(group)
	if g == "" {
		return notifySubscription{}, errors.New("notify: empty group")
	}
	sub := notifySubscription{}
	tok, rest, err := chompToken(g)
	if err != nil {
		return sub, err
	}
	switch strings.ToUpper(tok) {
	case "SELECTED":
		sub.kind = notifySelectorSelected
	case "SELECTED-DELAYED":
		sub.kind = notifySelectorSelectedDelayed
	case "INBOXES", "INBOX":
		sub.kind = notifySelectorInbox
	case "PERSONAL":
		sub.kind = notifySelectorPersonal
	case "SUBSCRIBED":
		sub.kind = notifySelectorSubscribed
	case "MAILBOXES":
		sub.kind = notifySelectorMailboxes
		names, after, err := parseMailboxNameList(rest)
		if err != nil {
			return sub, err
		}
		sub.names = names
		rest = after
	case "SUBTREE":
		sub.kind = notifySelectorSubtree
		names, after, err := parseMailboxNameList(rest)
		if err != nil {
			return sub, err
		}
		sub.names = names
		rest = after
	default:
		return sub, fmt.Errorf("notify: unknown selector %q", tok)
	}
	// Event set.
	rest = strings.TrimSpace(rest)
	events, err := parseNotifyEventSet(rest)
	if err != nil {
		return sub, err
	}
	sub.events = events
	return sub, nil
}

// chompToken returns the leading whitespace-delimited word and the
// remainder.
func chompToken(s string) (token, rest string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", errors.New("notify: expected token")
	}
	idx := strings.IndexAny(s, " \t(")
	if idx < 0 {
		return s, "", nil
	}
	return s[:idx], strings.TrimSpace(s[idx:]), nil
}

// parseMailboxNameList parses "(name1 name2 ...)" or a single bare name.
// Returns the names plus the remainder of the input after the list.
func parseMailboxNameList(s string) (names []string, rest string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, "", errors.New("notify: expected mailbox name list")
	}
	if s[0] == '(' {
		end := strings.IndexByte(s, ')')
		if end < 0 {
			return nil, "", errors.New("notify: unbalanced mailbox list")
		}
		body := s[1:end]
		for _, f := range strings.Fields(body) {
			names = append(names, strings.Trim(f, `"`))
		}
		return names, strings.TrimSpace(s[end+1:]), nil
	}
	tok, after, err := chompToken(s)
	if err != nil {
		return nil, "", err
	}
	return []string{strings.Trim(tok, `"`)}, after, nil
}

// parseNotifyEventSet parses "(EVT EVT ...)" or a bare "EVT" into the
// matching notifyEventKind bitset. Unknown event names are accepted as
// no-ops (RFC 5465 lists more events than we currently handle).
func parseNotifyEventSet(s string) (notifyEventKind, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("notify: expected event set")
	}
	body := s
	if s[0] == '(' {
		end := strings.IndexByte(s, ')')
		if end < 0 {
			return 0, errors.New("notify: unbalanced event set")
		}
		body = s[1:end]
	}
	var mask notifyEventKind
	for _, f := range strings.Fields(body) {
		switch strings.ToUpper(f) {
		case "MESSAGENEW":
			mask |= notifyEventMessageNew
		case "MESSAGEEXPUNGE":
			mask |= notifyEventMessageExpunge
		case "FLAGCHANGE":
			mask |= notifyEventFlagChange
		case "MAILBOXNAME":
			mask |= notifyEventMailboxName
		case "SUBSCRIPTIONCHANGE":
			mask |= notifyEventSubscriptionChange
		default:
			// Unknown event — accepted but ignored. RFC 5465 §6
			// expects the server to advertise its event set via
			// the NOTIFY capability arguments; we publish a
			// conservative subset and silently drop unsupported
			// names rather than rejecting the whole SET.
		}
	}
	return mask, nil
}
