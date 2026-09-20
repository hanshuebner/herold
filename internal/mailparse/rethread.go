package mailparse

// RethreadRow is one message's rethreading inputs for ComputeRethread.
// A row's identity is its position in the rows slice passed to
// ComputeRethread; ID is only used as the value a self-threaded row
// resolves to and as the row's own carried-forward ThreadID.
type RethreadRow struct {
	ID         int64
	MessageID  string
	InReplyTo  string
	References string
	Subject    string
	ThreadID   int64
}

// ComputeRethread computes each row's thread_id per REQ-STORE-40, the
// rule ingest and the bulk RethreadPrincipal / `diag rethread` path
// (re #442) share:
//
//   - two rows carrying the same Message-ID always converge on one
//     thread (re #88), regardless of subject;
//   - otherwise a row inherits the thread of the first In-Reply-To /
//     References entry that resolves to another row in this set,
//     provided the two base subjects thread together (issue #437) --
//     a resolvable but subject-mismatched ancestor stops the search
//     rather than falling through to a later reference;
//   - a row with neither becomes its own thread root.
//
// force controls whether a row that already carries a non-zero
// ThreadID keeps it (force=false, the bulk-import / sub-account
// migration convention: fill in the gaps only) or is recomputed from
// scratch alongside everything else (force=true, `diag rethread`
// applying a threading-rule change retroactively).
//
// The returned slice has one entry per row, in the same order.
//
// rows is conventionally supplied in ascending internal-date order (so
// a tie between same-instant messages resolves the way the caller's
// own SELECT ... ORDER BY sorted them), but resolution does not depend
// on that order: an ancestor referenced by In-Reply-To/References can
// sort earlier OR later in rows than the row referencing it -- clock
// skew, imported mail, and out-of-order delivery all produce exactly
// that shape, and it is precisely the already-stored mail #442 exists
// to repair. ComputeRethread resolves each row against the thread its
// ancestor's chain ultimately ends on, not whatever value the ancestor
// happens to hold at some mid-scan point, so one call converges the
// whole set regardless of array order.
//
// Implementation: phase 1 assigns each row at most one parent index
// (or a sentinel for "no parent" / "keep the existing ThreadID"),
// computed purely from each row's own static fields plus the
// MessageID index -- so phase 1's own iteration order never matters.
// Phase 2 resolves the resulting functional graph (each row points to
// at most one parent) with iterative path compression: a chain of any
// length is walked to its root or to an already-resolved row, then
// every row visited on that walk is set to the same result in one
// back-pass. A row is ever pushed onto the walk at most once across
// the whole call -- once resolved it always short-circuits later
// walks -- so total work is O(n) node visits plus O(n) parent
// computations. Well-formed mail cannot create a reference cycle (a
// message cannot reference an ancestor that does not yet exist), but
// malformed or adversarial header data in principle could; a cycle is
// broken by self-threading the row where a walk revisits a row still
// on its own path, which keeps every walk's node count finite and
// bounded, so the whole computation always terminates.
func ComputeRethread(rows []RethreadRow, force bool) []int64 {
	const (
		parentNone  = -1 // no ancestor resolved: row roots its own thread
		parentFixed = -2 // row keeps its existing ThreadID
	)

	// byID maps env_message_id to the first row carrying it, so
	// duplicate copies of the same message (e.g. a Sent copy and the
	// delivered copy of a self-sent message) and any reply chain built
	// on either copy all resolve to one anchor (re #88).
	byID := make(map[string]int, len(rows))
	for i, r := range rows {
		if r.MessageID != "" {
			if _, exists := byID[r.MessageID]; !exists {
				byID[r.MessageID] = i
			}
		}
	}

	parent := make([]int, len(rows))
	for i, r := range rows {
		if !force && r.ThreadID != 0 {
			parent[i] = parentFixed
			continue
		}

		if r.MessageID != "" {
			if first, ok := byID[r.MessageID]; ok && first != i {
				parent[i] = first
				continue
			}
		}

		p := parentNone
		all := ParseReferences(r.InReplyTo)
		seen := make(map[string]struct{}, len(all))
		for _, ref := range all {
			seen[ref] = struct{}{}
		}
		for _, ref := range ParseReferences(r.References) {
			if _, dup := seen[ref]; !dup {
				all = append(all, ref)
				seen[ref] = struct{}{}
			}
		}
		for _, ref := range all {
			if idx, ok := byID[ref]; ok && idx != i {
				// A changed base subject starts a new thread instead of
				// inheriting the ancestor's (issue #437); stop looking at
				// further references once one resolves, matched or not.
				if SubjectsThreadTogether(r.Subject, rows[idx].Subject) {
					p = idx
				}
				break
			}
		}
		parent[i] = p
	}

	// Phase 2: resolve the functional graph described by parent[] with
	// iterative path compression. resolved[i] == 0 means "not yet
	// computed"; every real result is a message id or thread id, both
	// always > 0.
	resolved := make([]int64, len(rows))
	onStack := make([]bool, len(rows))
	stack := make([]int, 0, 64)

	for start := range rows {
		if resolved[start] != 0 {
			continue
		}
		stack = stack[:0]
		cur := start
		for {
			if resolved[cur] != 0 {
				break
			}
			if onStack[cur] {
				// Cycle: break it by self-threading the row where the
				// walk re-enters its own path.
				resolved[cur] = rows[cur].ID
				break
			}
			onStack[cur] = true
			stack = append(stack, cur)

			p := parent[cur]
			if p == parentFixed {
				resolved[cur] = rows[cur].ThreadID
				break
			}
			if p == parentNone {
				resolved[cur] = rows[cur].ID
				break
			}
			cur = p
		}
		val := resolved[cur]
		for _, n := range stack {
			onStack[n] = false
			if resolved[n] == 0 {
				resolved[n] = val
			}
		}
	}

	return resolved
}
