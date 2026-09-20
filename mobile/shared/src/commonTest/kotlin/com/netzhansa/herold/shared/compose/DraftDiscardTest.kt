package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.actions.MailActions
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.outbox.InMemoryBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.outbox.OutboxFailure
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.store.Tombstones
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.yield
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

private val boxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "drafts-1", name = "Drafts", role = MailboxRoles.DRAFTS),
    Mailbox(accountId = "acct-a", id = "sent-1", name = "Sent", role = MailboxRoles.SENT),
)

private val identity =
    Identity(accountId = "acct-a", id = "id-1", name = "Alice", email = "alice@example.local")

private fun draftRow(id: String) = Email(
    accountId = "acct-a",
    id = id,
    threadId = "t-1",
    subject = "Re: hello",
    receivedAt = 2000,
    mailboxIds = setOf("drafts-1"),
    keywords = setOf(Keywords.DRAFT),
)

/**
 * Throwing a draft away (issue #371): the row goes at once whatever the
 * save has reached, the destroy travels in the outbox with the retries
 * and the refusals every other write gets, and a fetch that read the
 * draft before the discard does not bring it back.
 */
class DraftDiscardTest {

    private class Harness(val clock: MutableClock = MutableClock()) {
        val tombstones = Tombstones({ clock.now })
        val store = FakeLocalStore(tombstones)
        val api = FakeJmapApi()
        val spool = InMemoryBlobSpool()
        val outbox = Outbox(store) { clock.now }
        val composer = Composer(api, outbox, spool, { clock.now })
        var drains = 0
        val drafts = Drafts(store, outbox) { drains++ }
        val drainer = OutboxDrainer(
            api = api,
            store = store,
            outbox = outbox,
            spool = spool,
            composer = composer,
            tombstones = tombstones,
            now = { clock.now },
        )
    }

    private class MutableClock(var now: Long = 1_000_000)

    private suspend fun harness(): Harness = Harness().apply {
        store.upsertMailboxes(boxes)
        store.upsertIdentities(listOf(identity))
    }

    private fun composeState() = ComposeState(
        mode = ComposeMode.REPLY,
        accountId = "acct-a",
        identity = identity,
        to = listOf(MailAddress(null, "bob@example.local")),
        subject = "Re: hello",
        bodyHtml = "<p>a reply</p>",
    )

    /**
     * The discard is taken before the save answers. The message the
     * save goes on to create is the one the discard has to take away,
     * and nothing but the compose itself identifies it at that point.
     */
    @Test
    fun aDiscardBeforeTheSaveTakesAwayWhatTheSaveWrites() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")

        h.drafts.discard(handle)
        // The save answers afterwards, as a slow round trip does.
        h.store.upsertEmails(listOf(draftRow("draft-1")))
        h.drafts.saved(handle, "acct-a", "draft-1")

        assertNull(h.store.email("acct-a", "draft-1"), "the draft row must be gone")
        val queued = h.outbox.list().filter { it.kind == OutboxKind.DESTROY }
        assertEquals(listOf(listOf("draft-1")), queued.map { it.entityIds })
        h.drainer.drain()
        assertEquals(listOf(listOf("draft-1")), h.api.emailDestroys)
    }

    /** The plain order: the save answered, then the discard. */
    @Test
    fun aDiscardAfterTheSaveTakesTheDraftOffTheServer() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")
        h.store.upsertEmails(listOf(draftRow("draft-2")))
        h.drafts.saved(handle, "acct-a", "draft-2")

        h.drafts.discard(handle)

        assertNull(h.store.email("acct-a", "draft-2"), "the draft row must be gone")
        h.drainer.drain()
        assertEquals(listOf(listOf("draft-2")), h.api.emailDestroys)
        assertTrue(h.outbox.list().isEmpty(), "the drained entry must be gone")
    }

    /**
     * The conversation's own Discard, which names the message rather
     * than a compose: the way out for a draft whose snackbar offer has
     * come down (issue #371).
     */
    @Test
    fun theConversationsDiscardTakesASavedDraftAway() = runTest {
        val h = harness()
        h.store.upsertEmails(listOf(draftRow("draft-6")))

        h.drafts.discardSaved("acct-a", "draft-6")

        assertNull(h.store.email("acct-a", "draft-6"), "the draft row must be gone")
        h.drainer.drain()
        assertEquals(listOf(listOf("draft-6")), h.api.emailDestroys)
        assertTrue(h.outbox.list().isEmpty(), "the drained entry must be gone")
    }

    /** A queued save the drain has not reached leaves nothing behind. */
    @Test
    fun aDiscardOfAQueuedSaveDropsTheEntry() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")
        val entryId = h.outbox.enqueueCompose(
            kind = OutboxKind.DRAFT,
            label = "Draft: Re: hello",
            payload = h.composer.payloadOf(composeState(), identity, "drafts-1", "sent-1"),
        )
        h.drafts.queued(handle, "acct-a", entryId)

        h.drafts.discard(handle)

        assertTrue(h.outbox.list().isEmpty(), "the queued save must be dropped")
        h.drainer.drain()
        assertTrue(h.api.emailCreates.isEmpty(), "nothing must reach the server")
        assertTrue(h.api.emailDestroys.isEmpty())
    }

    /**
     * The discard lands while the queued save is being submitted, so
     * the entry cannot be taken out of the queue: the drain destroys
     * the draft it has just written.
     */
    @Test
    fun aDiscardWhileTheSaveIsInFlightTakesTheWrittenDraftAway() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")
        val entryId = h.outbox.enqueueCompose(
            kind = OutboxKind.DRAFT,
            label = "Draft: Re: hello",
            payload = h.composer.payloadOf(composeState(), identity, "drafts-1", "sent-1"),
        )
        h.drafts.queued(handle, "acct-a", entryId)
        // The drain has the entry, so dropping it is no longer possible.
        h.store.updateOutboxState(
            id = entryId,
            state = OutboxState.SENDING,
            attempts = 0,
            lastError = null,
            permanent = false,
            nextAttemptAt = 0,
        )

        h.drafts.discard(handle)
        h.api.createdEmailId = "draft-3"
        h.store.updateOutboxState(
            id = entryId,
            state = OutboxState.QUEUED,
            attempts = 0,
            lastError = null,
            permanent = false,
            nextAttemptAt = 0,
        )
        h.drainer.drain()
        h.drainer.drain()

        assertEquals(listOf(listOf("draft-3")), h.api.emailDestroys)
        assertNull(h.store.email("acct-a", "draft-3"))
        assertTrue(h.outbox.list().isEmpty(), "both entries must have drained")
    }

    /**
     * The drain wrote the queued save before the discard was taken, so
     * the entry is gone and the message it left is the one to take
     * away. This is the order the 0.9.5 hand-back reproduced.
     */
    @Test
    fun aDiscardAfterTheDrainWroteTheQueuedSaveTakesTheMessageAway() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")
        val entryId = h.outbox.enqueueCompose(
            kind = OutboxKind.DRAFT,
            label = "Draft: Re: hello",
            payload = h.composer.payloadOf(composeState(), identity, "drafts-1", "sent-1"),
        )
        h.drafts.queued(handle, "acct-a", entryId)
        h.api.createdEmailId = "draft-7"
        h.drainer.drain()
        // The save is on the server and its row has arrived with a sync.
        h.store.upsertEmails(listOf(draftRow("draft-7")))
        assertTrue(h.outbox.list().isEmpty(), "the save must have drained")

        h.drafts.discard(handle)

        assertNull(h.store.email("acct-a", "draft-7"), "the draft row must be gone")
        h.drainer.drain()
        assertEquals(listOf(listOf("draft-7")), h.api.emailDestroys)
    }

    /**
     * A destroy that could not be submitted is retried, and one the
     * server refuses is listed with its reason, tells the user and puts
     * the draft back where the screen can see it.
     */
    @Test
    fun aRefusedDestroySurfacesAndTheDraftComesBack() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")
        h.store.upsertEmails(listOf(draftRow("draft-4")))
        h.drafts.saved(handle, "acct-a", "draft-4")
        h.api.emails = mapOf("draft-4" to wireDraft("draft-4"))
        h.drafts.discard(handle)

        // First the wire is busy: the entry waits and comes back.
        h.api.destroyFailure = JmapException("serverUnavailable", 503)
        h.drainer.drain()
        val retried = h.outbox.list().single()
        assertEquals(OutboxState.QUEUED, retried.state)
        assertEquals(1, retried.attempts)
        assertTrue(retried.nextAttemptAt > h.clock.now, "the retry must be scheduled")

        // Then the server refuses it outright.
        h.api.destroyFailure = null
        h.api.destroyRefusal = "forbidden"
        h.clock.now += 60_000
        val failures = mutableListOf<OutboxFailure>()
        val watcher = launch { h.drainer.failures.collect { failures += it } }
        // The collector has to be subscribed before the drain emits.
        yield()
        h.drainer.drain()
        // ... and to run once more before it is taken down.
        yield()
        watcher.cancel()

        val failed = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, failed.state)
        assertEquals("forbidden", failed.lastError)
        assertEquals(listOf(OutboxKind.DESTROY), failures.map { it.kind })
        assertEquals("forbidden", failures.single().message)
        assertEquals(
            "draft-4",
            h.store.email("acct-a", "draft-4")?.id,
            "a refused discard leaves the draft where the server has it",
        )
    }

    /**
     * The fetch that was in flight when the discard happened. A pass
     * writes whatever its `Email/get` answered, and a response prepared
     * before the destroy carries the draft.
     */
    @Test
    fun aFetchInFlightDoesNotBringADiscardedDraftBack() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")
        val inFlight = draftRow("draft-5")
        h.store.upsertEmails(listOf(inFlight))
        h.drafts.saved(handle, "acct-a", "draft-5")

        h.drafts.discard(handle)
        h.store.upsertEmails(listOf(inFlight))

        assertNull(h.store.email("acct-a", "draft-5"), "the discarded draft must stay away")
        assertTrue(
            h.store.threadEmails("acct-a", "t-1").first().isEmpty(),
            "the conversation must hold no draft card",
        )
    }

    /** The label the outbox screen gives a queued discard. */
    @Test
    fun theQueuedDiscardIsNamedOnTheOutboxScreen() = runTest {
        val h = harness()
        val handle = DraftHandle("acct-a")
        h.store.upsertEmails(listOf(draftRow("draft-6")))
        h.drafts.saved(handle, "acct-a", "draft-6")
        h.drafts.discard(handle)
        assertEquals(MailActions.Labels.DISCARD_DRAFT, h.outbox.list().single().label)
    }

    private fun wireDraft(id: String) = com.netzhansa.herold.shared.jmap.WireEmail(
        id = id,
        threadId = "t-1",
        subject = "Re: hello",
        receivedAt = "2026-09-19T13:33:22Z",
        mailboxIds = mapOf("drafts-1" to true),
        keywords = mapOf(Keywords.DRAFT to true),
    )
}
