package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.outbox.InMemoryBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.store.MembershipHolds
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
    Mailbox(accountId = "acct-a", id = "trash-1", name = "Trash", role = MailboxRoles.TRASH),
    Mailbox(accountId = "acct-a", id = "label-1", name = "Projects"),
)

private fun seeded() = Email(
    accountId = "acct-a",
    id = "e1",
    threadId = "t1",
    subject = "Hello",
    receivedAt = 1000,
    keywords = emptySet(),
    mailboxIds = setOf("inbox-1"),
)

/**
 * A row an optimistic action changed does not flicker back to its
 * pre-action state when a response already in flight when the action
 * happened - an `Email/get`, or a sync pass's answer - lands afterwards
 * (issue #473). The response-after-action ordering is driven directly:
 * the action runs, then [FakeLocalStore.upsertEmails] is called with the
 * pre-action row, standing in for the delayed response, with no reliance
 * on timing.
 */
class MembershipHoldAcceptanceTest {

    private class Clock(var now: Long = 1_000_000)

    private class Harness(val clock: Clock = Clock(), ttlMs: Long = 120_000L) {
        val holds = MembershipHolds({ clock.now }, ttlMs)
        val store = FakeLocalStore(membershipHolds = holds)
        val api = FakeJmapApi()
        val outbox = Outbox(store) { clock.now }
        val actions = MailActions(store, outbox)
        val drainer = OutboxDrainer(api, store, outbox, InMemoryBlobSpool(), now = { clock.now })
    }

    private suspend fun harness(ttlMs: Long = 120_000L): Harness {
        val h = Harness(ttlMs = ttlMs)
        h.store.upsertMailboxes(mailboxes)
        h.store.upsertEmails(listOf(seeded()))
        return h
    }

    @Test
    fun anArchiveHoldsTheRowAgainstAResponseInFlight() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        h.actions.archive(listOf(before), mailboxes)
        // The Email/get that was already on its way when the swipe
        // happened answers now, with the pre-archive mailboxes.
        h.store.upsertEmails(listOf(before))

        assertEquals(
            setOf("archive-1"),
            h.store.email("acct-a", "e1")!!.mailboxIds,
            "the archive must not flicker back into the inbox",
        )
    }

    @Test
    fun aMoveToTrashHoldsTheRowAgainstAResponseInFlight() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        val pending = h.actions.deleteLocally(listOf(before), mailboxes)
        h.actions.commit(pending)
        h.store.upsertEmails(listOf(before))

        assertEquals(
            setOf("trash-1"),
            h.store.email("acct-a", "e1")!!.mailboxIds,
            "the move to trash must not flicker back into the inbox",
        )
    }

    @Test
    fun aLabelChangeHoldsTheRowAgainstAResponseInFlight() = runTest {
        val h = harness()
        val label = mailboxes.first { it.id == "label-1" }
        val before = h.store.email("acct-a", "e1")!!

        h.actions.setLabel(listOf(before), label, applied = true)
        h.store.upsertEmails(listOf(before))

        assertEquals(
            setOf("inbox-1", "label-1"),
            h.store.email("acct-a", "e1")!!.mailboxIds,
            "the label must not flicker off",
        )
    }

    @Test
    fun aReadStateChangeHoldsTheRowAgainstAResponseInFlight() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        h.actions.setSeen(listOf(before), true)
        h.store.upsertEmails(listOf(before))

        assertTrue(
            h.store.email("acct-a", "e1")!!.keywords.contains(Keywords.SEEN),
            "the read mark must not flicker off",
        )
    }

    /**
     * The hold freezes the whole of a row's membership, keywords and
     * `snoozedUntil` together, not only the field the action itself
     * patched - separating an action's own field from an unrelated one a
     * concurrent client changed would need per-field provenance the
     * store does not keep. Everything else a response carries still
     * lands.
     */
    @Test
    fun aHeldRowStillTakesTheRestOfAResponse() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        h.actions.archive(listOf(before), mailboxes)
        h.store.upsertEmails(listOf(before.copy(subject = "Updated", preview = "a new preview")))

        val after = h.store.email("acct-a", "e1")!!
        assertEquals(setOf("archive-1"), after.mailboxIds, "membership stays held")
        assertEquals("Updated", after.subject, "the rest of the row still lands")
        assertEquals("a new preview", after.preview)
    }

    @Test
    fun theHoldExpiresSoAnActionThatNeverDrainsDoesNotPinTheRowForever() = runTest {
        val h = harness(ttlMs = 1_000)
        val before = h.store.email("acct-a", "e1")!!

        h.actions.archive(listOf(before), mailboxes)
        h.clock.now += 1_001
        h.store.upsertEmails(listOf(before))

        assertEquals(
            setOf("inbox-1"),
            h.store.email("acct-a", "e1")!!.mailboxIds,
            "an expired hold must let a later response through",
        )
    }

    @Test
    fun aSuccessfulDrainReleasesTheHoldForTheNextFetch() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        h.actions.archive(listOf(before), mailboxes)
        h.drainer.drain()
        // A genuinely newer server state - the row picked up another
        // label meanwhile - now lands normally.
        h.store.upsertEmails(listOf(before.copy(mailboxIds = setOf("archive-1", "label-1"))))

        assertEquals(setOf("archive-1", "label-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
    }

    /**
     * A rejection reverts the row at once, but the entry itself stays
     * listed as failed rather than being removed - it is what a manual
     * retry from the outbox screen acts on. The hold stays with it, since
     * the only place a hold is ever released is an entry actually leaving
     * the outbox (issue #473): [Outbox.remove], [Outbox.cancelIfQueued]
     * and [Outbox.discardSupersededBy].
     */
    @Test
    fun aRejectedActionsHoldPersistsUntilTheEntryIsDiscarded() = runTest {
        val h = harness()
        h.api.setRejections = mapOf("e1" to "mailbox is read-only")
        val before = h.store.email("acct-a", "e1")!!

        h.actions.setSeen(listOf(before), true)
        h.drainer.drain()
        assertTrue(h.store.email("acct-a", "e1")!!.isUnread, "the refusal restores the row")

        h.store.upsertEmails(listOf(before.copy(keywords = setOf(Keywords.SEEN))))
        assertTrue(
            !h.store.email("acct-a", "e1")!!.keywords.contains(Keywords.SEEN),
            "the hold must still guard the reverted row while the failed entry is listed",
        )

        h.outbox.remove(h.outbox.list().single().id)
        h.store.upsertEmails(listOf(before.copy(keywords = setOf(Keywords.SEEN))))
        assertTrue(
            h.store.email("acct-a", "e1")!!.keywords.contains(Keywords.SEEN),
            "discarding the failed entry must release the hold",
        )
    }

    /**
     * An undo taken before the drain reaches the entry drops it with
     * [Outbox.cancelIfQueued] rather than [OutboxDrainer] ever seeing it,
     * so that is where its hold has to be released too (issue #473). The
     * entry never reached the server, so nothing was ever in flight that
     * could still carry anything but what the restored snapshot agrees
     * with; the restoring write needs no hold of its own.
     */
    @Test
    fun anUndoBeforeTheDrainLeavesNoHold() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        val pending = h.actions.archiveLocally(listOf(before), mailboxes)
        h.actions.commit(pending)
        h.actions.undo(pending)
        assertTrue(h.outbox.list().isEmpty(), "the cancelled entry must be gone")

        // A genuinely newer server state now lands normally.
        h.store.upsertEmails(listOf(before.copy(mailboxIds = setOf("inbox-1", "label-1"))))
        assertEquals(
            setOf("inbox-1", "label-1"),
            h.store.email("acct-a", "e1")!!.mailboxIds,
            "an undone action must leave no hold behind",
        )
    }

    /**
     * [Outbox.discardSupersededBy] drops a queued entry when a sync pass's
     * `Email/changes` already names the id (REQ-AND-SYNC-24), with no
     * [OutboxDrainer] involved either - the second path that has to
     * release the hold itself (issue #473).
     */
    @Test
    fun aSupersedingSyncLeavesNoHold() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        val pending = h.actions.archiveLocally(listOf(before), mailboxes)
        h.actions.commit(pending)
        h.outbox.discardSupersededBy("acct-a", listOf("e1"))
        assertTrue(h.outbox.list().isEmpty(), "the superseded entry must be gone")

        h.store.upsertEmails(listOf(before.copy(mailboxIds = setOf("inbox-1", "label-1"))))
        assertEquals(
            setOf("inbox-1", "label-1"),
            h.store.email("acct-a", "e1")!!.mailboxIds,
            "a superseded action must leave no hold behind",
        )
    }
}
