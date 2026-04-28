package thread

import (
	"hash/fnv"
	"sort"
	"strconv"
	"strings"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// ThreadKey is the derived stable identifier for a thread. We hash the
// canonical "root" message-id of the JWZ container forest into a 64-bit
// value so the wire form is short and clients echo it across syncs.
type ThreadKey uint64

// container is one node in the JWZ forest.
type container struct {
	// messageID is the trimmed lower-cased Message-ID; empty for
	// synthetic containers (referenced but not yet ingested).
	messageID string
	// msgIDs is the list of store rows that share this Message-ID
	// (Email/import or duplicate deliveries can produce more than
	// one). Empty for synthetic containers.
	msgIDs []store.MessageID
	// parent is the JWZ parent pointer. Cycles are guarded.
	parent *container
}

// root chases parent pointers with a depth bound to defeat cycles
// (defensive — the algorithm should not produce them).
func (c *container) root() *container {
	cur := c
	for hops := 0; cur.parent != nil && hops < 1024; hops++ {
		cur = cur.parent
	}
	return cur
}

// computeThreads runs the JWZ algorithm over a flat list of messages
// and returns a map from each MessageID to its assigned ThreadKey, plus
// a map from ThreadKey to the list of MessageIDs in the thread (in
// arrival-time order). The algorithm:
//
//  1. Index messages by Message-ID and synthesize containers for any
//     References / In-Reply-To token that names an unknown ancestor.
//  2. Wire up parent links along the References chain.
//  3. Subject-collapse fallback: messages that share a normalised
//     subject (RFC 5256 §2.1) and whose subject was a reply ("Re:")
//     join the existing thread.
//  4. Walk each connected component, find the canonical root (the
//     lexically smallest reachable Message-ID), and assign every
//     message in the component a ThreadKey derived from that root.
//
// The fourth step makes the assignment stable across restarts — two
// messages that thread together always observe the same ThreadKey
// regardless of insertion order.
func computeThreads(msgs []store.Message) (map[store.MessageID]ThreadKey, map[ThreadKey][]store.MessageID) {
	byID := make(map[string]*container)
	getOrCreate := func(id string) *container {
		if id == "" {
			return nil
		}
		if c, ok := byID[id]; ok {
			return c
		}
		c := &container{messageID: id}
		byID[id] = c
		return c
	}
	// One synthetic anchor per anonymous (no Message-ID) message so
	// they get their own thread; tracked separately so we can iterate.
	anonContainers := []*container{}
	rowToContainer := make(map[store.MessageID]*container, len(msgs))
	// Pass 1: register each message.
	for _, m := range msgs {
		mid := normalizeMessageID(m.Envelope.MessageID)
		var c *container
		if mid != "" {
			c = getOrCreate(mid)
			c.msgIDs = append(c.msgIDs, m.ID)
		} else {
			c = &container{msgIDs: []store.MessageID{m.ID}}
			anonContainers = append(anonContainers, c)
		}
		rowToContainer[m.ID] = c
		// Phase 1's Envelope.InReplyTo carries either an In-Reply-To
		// value or a References-style chain. parseReferences handles
		// both — angle-bracket tokens in arrival order.
		refs := parseReferences(m.Envelope.InReplyTo)
		var prev *container
		for _, r := range refs {
			cur := getOrCreate(r)
			if prev != nil && cur != prev && cur.parent == nil && !ancestorOf(cur, prev) {
				cur.parent = prev
			}
			prev = cur
		}
		if prev != nil && c.parent == nil && c != prev && !ancestorOf(c, prev) {
			c.parent = prev
		}
	}
	// Pass 2: subject-collapse fallback. We pick a "subject anchor"
	// container per normalised subject (the earliest non-reply
	// message), and any reply containers with the same subject whose
	// component root differs from the anchor's root link to it.
	subjectAnchor := make(map[string]*container)
	subjectIsReply := make(map[string]bool)
	for _, m := range msgs {
		c := rowToContainer[m.ID]
		if c == nil || c.messageID == "" {
			continue
		}
		subj, isReply := normalizeSubject(m.Envelope.Subject)
		if subj == "" {
			continue
		}
		existing, ok := subjectAnchor[subj]
		if !ok {
			subjectAnchor[subj] = c
			subjectIsReply[subj] = isReply
			continue
		}
		// Prefer the earliest non-reply container as the subject
		// anchor.
		if subjectIsReply[subj] && !isReply {
			subjectAnchor[subj] = c
			subjectIsReply[subj] = false
		}
		_ = existing
	}
	for _, m := range msgs {
		c := rowToContainer[m.ID]
		if c == nil || c.messageID == "" {
			continue
		}
		subj, isReply := normalizeSubject(m.Envelope.Subject)
		if subj == "" || !isReply {
			continue
		}
		anchor, ok := subjectAnchor[subj]
		if !ok || anchor == c {
			continue
		}
		ar := anchor.root()
		cr := c.root()
		if ar == cr {
			continue
		}
		// Hook the smaller component into the larger one.
		if cr.parent == nil && !ancestorOf(cr, ar) {
			cr.parent = ar
		}
	}
	// Pass 3: assign ThreadKeys. The canonical root id is the lexically
	// smallest non-empty Message-ID in each component.
	componentCanon := make(map[*container]string)
	for _, c := range byID {
		r := c.root()
		canon := componentCanon[r]
		if c.messageID != "" && (canon == "" || c.messageID < canon) {
			canon = c.messageID
		}
		componentCanon[r] = canon
	}
	msgToThread := make(map[store.MessageID]ThreadKey, len(msgs))
	threadToMsgs := make(map[ThreadKey][]store.MessageID)
	for _, m := range msgs {
		c := rowToContainer[m.ID]
		var canon string
		if c.messageID != "" {
			canon = componentCanon[c.root()]
			if canon == "" {
				canon = c.messageID
			}
		} else {
			// Anonymous: own thread keyed off the row id.
			canon = "anon:" + strconv.FormatUint(uint64(m.ID), 10)
		}
		key := keyFor(canon)
		msgToThread[m.ID] = key
		threadToMsgs[key] = append(threadToMsgs[key], m.ID)
	}
	for k, ids := range threadToMsgs {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		threadToMsgs[k] = ids
	}
	_ = anonContainers // kept for clarity; iteration done above
	return msgToThread, threadToMsgs
}

// ancestorOf reports whether candidate is an ancestor of c (including
// c itself). Used to refuse parent links that would form a cycle.
func ancestorOf(c, candidate *container) bool {
	for cur, hops := c, 0; cur != nil && hops < 1024; cur, hops = cur.parent, hops+1 {
		if cur == candidate {
			return true
		}
	}
	return false
}

// keyFor hashes a canonical Message-ID into a ThreadKey. FNV-1a is
// fast and stable; we reserve 0 for "no thread".
func keyFor(canon string) ThreadKey {
	h := fnv.New64a()
	_, _ = h.Write([]byte(canon))
	v := h.Sum64()
	if v == 0 {
		v = 1
	}
	return ThreadKey(v)
}

// normalizeMessageID strips angle brackets and lowercases the id, per
// RFC 5322 §3.6.4. Delegates to mailparse.NormalizeMessageID; retained
// as a package-local alias so the rest of jwz.go does not need to be
// updated and the call sites read naturally.
func normalizeMessageID(id string) string {
	return mailparse.NormalizeMessageID(id)
}

// parseReferences pulls Message-IDs out of a References / In-Reply-To
// header value. Delegates to mailparse.ParseReferences; retained as a
// package-local alias so the JWZ algorithm does not need restructuring.
func parseReferences(s string) []string {
	return mailparse.ParseReferences(s)
}

// normalizeSubject implements RFC 5256 §2.1 base-subject extraction:
// strip leading "Re:" / "Fwd:" / "Fw:" runs (case-insensitive) and
// optional list-id "[tag]" prefixes. Returns the trimmed lower-cased
// subject and a boolean indicating whether anything was stripped (i.e.
// the message looks like a reply).
func normalizeSubject(subject string) (string, bool) {
	s := strings.TrimSpace(subject)
	stripped := false
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "[") {
			if i := strings.Index(s, "]"); i > 0 {
				s = s[i+1:]
				continue
			}
		}
		lower := strings.ToLower(s)
		switch {
		case strings.HasPrefix(lower, "re:"):
			s = s[3:]
			stripped = true
		case strings.HasPrefix(lower, "fwd:"):
			s = s[4:]
			stripped = true
		case strings.HasPrefix(lower, "fw:"):
			s = s[3:]
			stripped = true
		default:
			return strings.ToLower(strings.TrimSpace(s)), stripped
		}
	}
}
