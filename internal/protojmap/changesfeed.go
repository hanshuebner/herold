package protojmap

import (
	"context"

	"github.com/hanshuebner/herold/internal/store"
)

// changeFeedPage is the page size used when paging through
// Metadata.ReadChangeFeed / ReadChangeFeedAll from a Foo/changes
// walker.
const changeFeedPage = 1000

// foldChangeEntry applies one change-feed entry's effect onto the
// created/updated/destroyed sets, per RFC 8620 5.2: an id created and
// later destroyed within the same window disappears from both; an id
// created and later updated stays "created"; an id updated and later
// destroyed reports only as "destroyed".
func foldChangeEntry(op store.ChangeOp, id uint64, created, updated, destroyed map[uint64]struct{}) {
	switch op {
	case store.ChangeOpCreated:
		delete(destroyed, id)
		created[id] = struct{}{}
	case store.ChangeOpUpdated:
		if _, ok := created[id]; ok {
			return
		}
		if _, ok := destroyed[id]; ok {
			return
		}
		updated[id] = struct{}{}
	case store.ChangeOpDestroyed:
		if _, ok := created[id]; ok {
			delete(created, id)
			return
		}
		delete(updated, id)
		destroyed[id] = struct{}{}
	}
}

// isUnseenChangeID reports whether id has not yet appeared in any of
// the three folded sets, i.e. whether folding the next entry for id
// would grow the reported set by one.
func isUnseenChangeID(id uint64, created, updated, destroyed map[uint64]struct{}) bool {
	if _, ok := created[id]; ok {
		return false
	}
	if _, ok := updated[id]; ok {
		return false
	}
	_, ok := destroyed[id]
	return !ok
}

// WalkChangesBySeq folds a principal's user-caused change-feed entries
// of the given kind, starting after sinceSeq, into created/updated/
// destroyed id sets (RFC 8620 5.2). When maxChanges is positive and
// folding the next not-yet-seen id would push the reported set past
// it, the walk stops there: cutoff is the Seq of the last entry
// actually folded, and hasMore is true.
//
// Use WalkChangesBySeq for handlers whose state string is the raw
// change-feed Seq (Email, Mailbox, Conversation, Message, Membership):
// cutoff is then a value already in the same space as sinceSeq and
// as the next Metadata.ReadChangeFeed fromSeq, so a trimmed answer's
// newState both accurately describes the returned changes and directly
// resumes the walk on the next call. Comparing sinceSeq against an
// op-count instead (as WalkChangesByOrdinal does for the jmap_states-
// counter datatypes) silently dropped every chat entry until 127 ops
// had accumulated (issue #47) — the two walkers exist because the two
// state encodings are not interchangeable.
func WalkChangesBySeq(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	kind store.EntityKind,
	sinceSeq store.ChangeSeq,
	maxChanges int,
) (created, updated, destroyed map[uint64]struct{}, cutoff store.ChangeSeq, hasMore bool, err error) {
	created = map[uint64]struct{}{}
	updated = map[uint64]struct{}{}
	destroyed = map[uint64]struct{}{}
	cutoff = sinceSeq
	cursor := sinceSeq
outer:
	for {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, nil, 0, false, cerr
		}
		batch, ferr := meta.ReadChangeFeed(ctx, pid, cursor, changeFeedPage)
		if ferr != nil {
			return nil, nil, nil, 0, false, ferr
		}
		for _, entry := range batch {
			if entry.Kind == kind {
				if maxChanges > 0 && isUnseenChangeID(entry.EntityID, created, updated, destroyed) &&
					len(created)+len(updated)+len(destroyed) >= maxChanges {
					hasMore = true
					break outer
				}
				foldChangeEntry(entry.Op, entry.EntityID, created, updated, destroyed)
				cutoff = entry.Seq
			}
			cursor = entry.Seq
		}
		if len(batch) < changeFeedPage {
			break
		}
	}
	return created, updated, destroyed, cutoff, hasMore, nil
}

// WalkChangesByOrdinal folds a principal's change-feed entries of the
// given kind into created/updated/destroyed id sets (RFC 8620 5.2),
// for handlers whose state string is a datatype-local counter
// (jmap_states) rather than the raw change-feed Seq: Contact,
// AddressBook, Calendar, CalendarEvent, SeenAddress. sinceOrdinal is
// compared against the 1-based count of this kind's entries seen so
// far in the feed, since the jmap_states counter and the change-feed
// entries for that kind advance together. includeAll selects
// ReadChangeFeedAll (SeenAddress reads background-caused rows too)
// over ReadChangeFeed.
//
// When maxChanges is positive and folding the next not-yet-seen id
// would push the reported set past it, the walk stops there: cutoff
// is the ordinal of the last entry actually folded, a valid resume
// point for the next call's sinceState, and hasMore is true.
func WalkChangesByOrdinal(
	ctx context.Context,
	meta store.Metadata,
	pid store.PrincipalID,
	kind store.EntityKind,
	sinceOrdinal int64,
	maxChanges int,
	includeAll bool,
) (created, updated, destroyed map[uint64]struct{}, cutoff int64, hasMore bool, err error) {
	created = map[uint64]struct{}{}
	updated = map[uint64]struct{}{}
	destroyed = map[uint64]struct{}{}
	cutoff = sinceOrdinal
	var cursor store.ChangeSeq
	ordinal := int64(0)
outer:
	for {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, nil, 0, false, cerr
		}
		var batch []store.StateChange
		var ferr error
		if includeAll {
			batch, ferr = meta.ReadChangeFeedAll(ctx, pid, cursor, changeFeedPage)
		} else {
			batch, ferr = meta.ReadChangeFeed(ctx, pid, cursor, changeFeedPage)
		}
		if ferr != nil {
			return nil, nil, nil, 0, false, ferr
		}
		for _, entry := range batch {
			if entry.Kind == kind {
				ordinal++
				if ordinal > sinceOrdinal {
					if maxChanges > 0 && isUnseenChangeID(entry.EntityID, created, updated, destroyed) &&
						len(created)+len(updated)+len(destroyed) >= maxChanges {
						hasMore = true
						break outer
					}
					foldChangeEntry(entry.Op, entry.EntityID, created, updated, destroyed)
					cutoff = ordinal
				}
			}
			cursor = entry.Seq
		}
		if len(batch) < changeFeedPage {
			break
		}
	}
	return created, updated, destroyed, cutoff, hasMore, nil
}
