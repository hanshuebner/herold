package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.outbox.ActionPayload
import com.netzhansa.herold.shared.outbox.InMemoryBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.outbox.outboxJson
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
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
 * The action layer after the outbox landed: every action writes the store
 * and queues its `Email/set`, and only a drain talks to the server
 * (REQ-AND-SYNC-20).
 */
class MailActionsTest {

    private class Harness(val store: FakeLocalStore, val api: FakeJmapApi) {
        val outbox = Outbox(store)
        val drainer = OutboxDrainer(api, store, outbox, InMemoryBlobSpool())
        val actions = MailActions(store, outbox)
    }

    private suspend fun harness(email: Email = seeded()): Harness {
        val store = FakeLocalStore().apply {
            upsertMailboxes(mailboxes)
            upsertEmails(listOf(email))
        }
        return Harness(store, FakeJmapApi())
    }

    @Test
    fun starWritesTheLocalStoreAndQueuesTheKeywordPatch() = runTest {
        val h = harness()

        h.actions.setFlagged(listOf(seeded()), true)

        assertTrue(h.store.email("acct-a", "e1")!!.isFlagged)
        assertTrue(h.api.emailSetCalls.isEmpty(), "an action sends nothing itself")
        val entry = h.outbox.list().single()
        val patches = outboxJson.decodeFromString<ActionPayload>(entry.payload).patches
        assertEquals(JsonPrimitive(true), patches.getValue("e1")["keywords/\$flagged"])

        h.drainer.drain()
        assertEquals(JsonPrimitive(true), h.api.emailSetCalls.single()["e1"]!!["keywords/\$flagged"])
        assertTrue(h.outbox.list().isEmpty(), "a drained entry leaves the queue")
    }

    @Test
    fun archiveMovesOutOfInboxIntoArchiveAndRestorePutsItBack() = runTest {
        val h = harness()
        val before = h.store.email("acct-a", "e1")!!

        val snapshot = h.actions.archive(listOf(before), mailboxes)
        h.drainer.drain()

        assertEquals(setOf("archive-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
        val patch = h.api.emailSetCalls.single().getValue("e1")
        assertEquals(JsonNull, patch["mailboxIds/inbox-1"])
        assertEquals(JsonPrimitive(true), patch["mailboxIds/archive-1"])

        h.actions.restore(snapshot)
        h.drainer.drain()
        assertEquals(setOf("inbox-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
        val undoPatch = h.api.emailSetCalls.last().getValue("e1")
        assertEquals(JsonPrimitive(true), undoPatch["mailboxIds/inbox-1"])
        assertEquals(JsonNull, undoPatch["mailboxIds/archive-1"])
    }

    @Test
    fun archiveLocallyMovesTheRowsBeforeAnythingIsQueued() = runTest {
        val h = harness()

        val pending = h.actions.archiveLocally(listOf(h.store.email("acct-a", "e1")!!), mailboxes)

        // The undo affordance rides on this: the row is out of the inbox
        // with nothing queued yet (issue #338).
        assertEquals(setOf("archive-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
        assertTrue(h.outbox.list().isEmpty(), "archiveLocally must not queue anything")

        h.actions.commit(pending)
        assertEquals(1, h.outbox.list().size)
        h.drainer.drain()
        assertEquals(JsonNull, h.api.emailSetCalls.single().getValue("e1")["mailboxIds/inbox-1"])
    }

    @Test
    fun anUndoBeforeTheDrainDropsTheEntryAndSendsNothing() = runTest {
        val h = harness()

        val pending = h.actions.archiveLocally(listOf(h.store.email("acct-a", "e1")!!), mailboxes)
        h.actions.commit(pending)
        h.actions.undo(pending)

        assertTrue(h.outbox.list().isEmpty(), "the queued archive is gone")
        assertEquals(setOf("inbox-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
        h.drainer.drain()
        assertTrue(h.api.emailSetCalls.isEmpty(), "an undone action never reaches the server")
    }

    @Test
    fun anUndoAfterTheDrainQueuesTheInverse() = runTest {
        val h = harness()

        val pending = h.actions.archiveLocally(listOf(h.store.email("acct-a", "e1")!!), mailboxes)
        h.actions.commit(pending)
        h.drainer.drain()
        h.actions.undo(pending)
        h.drainer.drain()

        assertEquals(setOf("inbox-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
        assertEquals(2, h.api.emailSetCalls.size)
        assertEquals(JsonPrimitive(true), h.api.emailSetCalls.last().getValue("e1")["mailboxIds/inbox-1"])
    }

    @Test
    fun anActionTakenWithNoConnectionStaysAppliedAndQueued() = runTest {
        val h = harness()
        h.api.setFailure = kotlinx.io.IOException("network unreachable")

        h.actions.archive(listOf(h.store.email("acct-a", "e1")!!), mailboxes)
        h.drainer.drain()

        assertEquals(
            setOf("archive-1"),
            h.store.email("acct-a", "e1")!!.mailboxIds,
            "the optimistic state survives a failed drain (REQ-AND-SYNC-20)",
        )
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.QUEUED, entry.state)
        assertEquals(1, entry.attempts)
    }

    @Test
    fun archivingAnAlreadyArchivedRowHasNothingToQueue() = runTest {
        val h = harness(seeded().copy(mailboxIds = setOf("archive-1")))

        val pending = h.actions.archiveLocally(listOf(h.store.email("acct-a", "e1")!!), mailboxes)

        assertTrue(pending.isEmpty)
        h.actions.commit(pending)
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aServerRejectionRevertsAndKeepsTheEntryWithItsReason() = runTest {
        val h = harness()
        h.api.setRejections = mapOf("e1" to "mailbox is read-only")

        h.actions.setSeen(listOf(h.store.email("acct-a", "e1")!!), true)
        h.drainer.drain()

        assertTrue(h.store.email("acct-a", "e1")!!.isUnread, "the optimistic read mark is reverted")
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, entry.state)
        assertTrue(entry.permanent)
        assertEquals("mailbox is read-only", entry.lastError)
    }

    @Test
    fun labellingTogglesTheCustomMailboxMembership() = runTest {
        val h = harness()
        val label = mailboxes.first { it.id == "label-1" }

        h.actions.setLabel(listOf(h.store.email("acct-a", "e1")!!), label, applied = true)
        assertEquals(setOf("inbox-1", "label-1"), h.store.email("acct-a", "e1")!!.mailboxIds)

        h.actions.setLabel(listOf(h.store.email("acct-a", "e1")!!), label, applied = false)
        assertEquals(setOf("inbox-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
    }

    @Test
    fun snoozeQueuesSnoozedUntilAndMarksTheMessageSnoozedLocally() = runTest {
        val h = harness()

        h.actions.snooze(listOf(h.store.email("acct-a", "e1")!!), "2026-09-12T06:00:00Z")
        h.drainer.drain()

        val stored = h.store.email("acct-a", "e1")!!
        assertEquals("2026-09-12T06:00:00Z", stored.snoozedUntil)
        assertTrue(stored.isSnoozed)
        assertEquals(
            JsonPrimitive("2026-09-12T06:00:00Z"),
            h.api.emailSetCalls.single().getValue("e1")["snoozedUntil"],
        )
    }

    @Test
    fun cancellingASnoozeClearsTheWakeTimeAndTheKeyword() = runTest {
        val api = FakeJmapApi()
        val store = store(seeded().copy(snoozedUntil = "2026-09-12T06:00:00Z", keywords = setOf(Keywords.SNOOZED)))
        val actions = MailActions(api, store)

        actions.unsnooze(listOf(store.email("acct-a", "e1")!!))

        val stored = store.email("acct-a", "e1")!!
        assertEquals(null, stored.snoozedUntil)
        assertTrue(!stored.isSnoozed)
        assertEquals(JsonNull, api.emailSetCalls.single().getValue("e1")["snoozedUntil"])
    }

    @Test
    fun recategorisingReplacesTheCategoryKeyword() = runTest {
        val seed = seeded().copy(keywords = setOf(Keywords.categoryKeyword("Primary")))
        val h = harness(seed)

        h.actions.setCategory(listOf(seed), "Promotions")
        h.drainer.drain()

        assertEquals("promotions", h.store.email("acct-a", "e1")!!.category)
        val patch = h.api.emailSetCalls.single().getValue("e1")
        assertEquals(JsonNull, patch["keywords/\$category-primary"])
        assertEquals(JsonPrimitive(true), patch["keywords/\$category-promotions"])
    }

    @Test
    fun aQueuedActionIsDiscardedWhenTheServerVersionArrivesFirst() = runTest {
        val h = harness()

        h.actions.setFlagged(listOf(h.store.email("acct-a", "e1")!!), true)
        val discarded = h.outbox.discardSupersededBy("acct-a", listOf("e1"))

        assertEquals(1, discarded.size)
        assertTrue(h.outbox.list().isEmpty(), "server truth wins (REQ-AND-SYNC-24)")
        h.drainer.drain()
        assertTrue(h.api.emailSetCalls.isEmpty())
    }

    @Test
    fun theQueueSurvivesAnActionOnAMessageOfAnotherAccount() = runTest {
        val h = harness()
        h.store.upsertMailboxes(
            listOf(Mailbox(accountId = "acct-b", id = "inbox-2", name = "Inbox", role = MailboxRoles.INBOX)),
        )
        h.store.upsertEmails(
            listOf(seeded().copy(accountId = "acct-b", id = "e2", mailboxIds = setOf("inbox-2"))),
        )

        h.actions.setFlagged(listOf(h.store.email("acct-a", "e1")!!), true)
        h.actions.setFlagged(listOf(h.store.email("acct-b", "e2")!!), true)

        assertEquals(listOf("acct-a", "acct-b"), h.outbox.list().map { it.accountId })
        assertNull(h.outbox.list().first().lastError)
    }
}
