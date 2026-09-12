package com.netzhansa.herold.shared.outbox

import com.netzhansa.herold.shared.actions.MailActions
import com.netzhansa.herold.shared.compose.AttachmentStatus
import com.netzhansa.herold.shared.compose.ComposeAttachment
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.compose.ComposeState
import com.netzhansa.herold.shared.compose.Composer
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.JmapException
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

private val boxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
    Mailbox(accountId = "acct-a", id = "drafts-1", name = "Drafts", role = MailboxRoles.DRAFTS),
    Mailbox(accountId = "acct-a", id = "sent-1", name = "Sent", role = MailboxRoles.SENT),
)

private val identity = Identity(accountId = "acct-a", id = "id-1", name = "Alice", email = "alice@example.local")

private fun message(id: String) = Email(
    accountId = "acct-a",
    id = id,
    threadId = "t-$id",
    subject = "Hello $id",
    receivedAt = 1000,
    mailboxIds = setOf("inbox-1"),
)

/**
 * The queue semantics of the drain: order, backoff, revert on a refusal,
 * and a send resuming at the step that failed
 * (REQ-AND-SYNC-22/23, issue #351).
 */
class OutboxDrainerTest {

    private class Harness(val clock: MutableClock = MutableClock()) {
        val store = FakeLocalStore()
        val api = FakeJmapApi()
        val spool = InMemoryBlobSpool()
        val outbox = Outbox(store) { clock.now }
        val composer = Composer(api, outbox, spool, { clock.now })
        val drainer = OutboxDrainer(api, store, outbox, spool, composer, { clock.now })
        val actions = MailActions(store, outbox)
    }

    private class MutableClock(var now: Long = 1_000_000)

    private suspend fun harness(vararg emails: Email): Harness = Harness().apply {
        store.upsertMailboxes(boxes)
        store.upsertEmails(emails.toList())
        store.upsertIdentities(listOf(identity))
    }

    private fun composeState(subject: String, attachments: List<ComposeAttachment> = emptyList()) = ComposeState(
        mode = ComposeMode.NEW,
        accountId = "acct-a",
        identity = identity,
        to = listOf(MailAddress(null, "bob@example.local")),
        subject = subject,
        bodyHtml = "<p>hello</p>",
        attachments = attachments,
    )

    @Test
    fun entriesOfAnAccountDrainInTheOrderTheyWereQueued() = runTest {
        val h = harness(message("e1"), message("e2"))

        h.actions.setFlagged(listOf(h.store.email("acct-a", "e1")!!), true)
        h.actions.setSeen(listOf(h.store.email("acct-a", "e2")!!), true)
        h.drainer.drain()

        assertEquals(listOf("e1", "e2"), h.api.emailSetCalls.map { it.keys.single() })
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aTransientFailureBacksOffAndHoldsTheEntriesBehindIt() = runTest {
        val h = harness(message("e1"), message("e2"))
        h.api.setFailure = kotlinx.io.IOException("network unreachable")

        h.actions.setFlagged(listOf(h.store.email("acct-a", "e1")!!), true)
        h.actions.setSeen(listOf(h.store.email("acct-a", "e2")!!), true)
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.retryable)
        assertEquals(2, outcome.pending, "the entry behind the failed one is untouched")
        val first = h.outbox.list().first()
        assertEquals(OutboxState.QUEUED, first.state)
        assertTrue(first.nextAttemptAt > h.clock.now, "the retry waits out a backoff")
        assertEquals(0, h.outbox.list().last().attempts)

        // Nothing is retried before the backoff has passed.
        h.api.emailSetCalls.clear()
        h.drainer.drain()
        assertTrue(h.api.emailSetCalls.isEmpty())

        h.clock.now = first.nextAttemptAt + 1
        h.api.setFailure = null
        h.drainer.drain()
        assertEquals(listOf("e1", "e2"), h.api.emailSetCalls.map { it.keys.single() })
    }

    @Test
    fun aRefusedActionRevertsTheRowsAndStaysListedForARetry() = runTest {
        val h = harness(message("e1"))
        h.api.setRejections = mapOf("e1" to "mailbox is read-only")

        h.actions.archive(listOf(h.store.email("acct-a", "e1")!!), boxes)
        h.drainer.drain()

        assertEquals(setOf("inbox-1"), h.store.email("acct-a", "e1")!!.mailboxIds)
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, entry.state)
        assertTrue(entry.permanent)

        // A retry re-submits the same entry rather than needing the user
        // to redo the action.
        h.api.setRejections = emptyMap()
        h.outbox.retry(entry.id)
        h.drainer.drain()
        assertEquals(2, h.api.emailSetCalls.size)
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aPermanentlyFailedEntryDoesNotBlockTheOnesBehindIt() = runTest {
        val h = harness(message("e1"), message("e2"))
        h.api.setRejections = mapOf("e1" to "refused")

        h.actions.setFlagged(listOf(h.store.email("acct-a", "e1")!!), true)
        h.actions.setFlagged(listOf(h.store.email("acct-a", "e2")!!), true)
        h.drainer.drain()

        assertEquals(listOf("e1", "e2"), h.api.emailSetCalls.map { it.keys.single() })
        assertEquals(1, h.outbox.list().size)
        assertEquals(OutboxState.FAILED, h.outbox.list().single().state)
    }

    @Test
    fun aDrainedActionTakesTheServersVersionOfTheMessage() = runTest {
        val h = harness(message("e1"))
        h.api.emails = mapOf(
            "e1" to com.netzhansa.herold.shared.jmap.WireEmail(
                id = "e1",
                threadId = "t-e1",
                subject = "Hello e1",
                mailboxIds = mapOf("archive-1" to true),
                keywords = mapOf("\$seen" to true),
            ),
        )

        h.actions.archive(listOf(h.store.email("acct-a", "e1")!!), boxes)
        h.drainer.drain()

        val stored = h.store.email("acct-a", "e1")!!
        assertEquals(setOf("archive-1"), stored.mailboxIds)
        assertTrue(stored.keywords.contains("\$seen"), "the server's keywords replace the optimistic ones")
    }

    @Test
    fun aSendUploadsWritesAndSubmitsInThatOrder() = runTest {
        val h = harness()
        val handle = h.spool.put(byteArrayOf(1, 2, 3), "notes.txt")
        val attachment = ComposeAttachment(
            key = handle,
            name = "notes.txt",
            type = "text/plain",
            size = 3,
            status = AttachmentStatus.PENDING,
            spool = handle,
        )

        val queued = h.composer.send(composeState("offline send", listOf(attachment)), boxes)
        assertTrue(queued is ComposeResult.Queued)
        assertTrue(h.api.uploads.isEmpty(), "nothing goes up until the drain")

        h.drainer.drain()

        assertEquals(1, h.api.uploads.size)
        assertEquals(1, h.api.emailCreates.size)
        val call = h.api.sendCalls.single()
        assertEquals("id-1", call.identityId)
        assertEquals(listOf("bob@example.local"), call.envelope.rcptTo)
        assertEquals(h.api.createdEmailId, call.draftId, "the submission references the draft the drain created")
        assertTrue(h.outbox.list().isEmpty())
        assertNull(h.spool.read(handle), "the spooled copy goes with the drained entry")
    }

    @Test
    fun aSendResumesAtTheStepThatFailed() = runTest {
        val h = harness()
        val handle = h.spool.put(byteArrayOf(1, 2, 3), "notes.txt")
        val attachment = ComposeAttachment(
            key = handle,
            name = "notes.txt",
            type = "text/plain",
            size = 3,
            status = AttachmentStatus.PENDING,
            spool = handle,
        )
        h.composer.send(composeState("resumed send", listOf(attachment)), boxes)

        // The upload takes, the draft write does not.
        h.api.emailCreateError = "the server was busy"
        h.drainer.drain()
        assertEquals(1, h.api.uploads.size)
        val held = h.outbox.list().single()
        val payload = outboxJson.decodeFromString<ComposePayload>(held.payload)
        assertNotNull(payload.attachments.single().blobId, "the upload is not repeated")

        h.api.emailCreateError = null
        h.outbox.retry(held.id)
        h.drainer.drain()

        assertEquals(1, h.api.uploads.size, "the blob went up once")
        assertEquals(2, h.api.emailCreates.size, "the step that failed is the one repeated")
        assertEquals(1, h.api.sendCalls.size)
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aRefusedSubmissionKeepsTheDraftSoARetryDoesNotDuplicateIt() = runTest {
        val h = harness()
        h.api.submissionError = "forbiddenFrom"

        h.composer.send(composeState("refused send"), boxes)
        h.drainer.drain()

        val entry = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, entry.state)
        assertTrue(entry.permanent)
        assertEquals("forbiddenFrom", entry.lastError)
        val payload = outboxJson.decodeFromString<ComposePayload>(entry.payload)
        assertEquals(h.api.createdEmailId, payload.draftId)

        h.api.submissionError = null
        h.outbox.retry(entry.id)
        h.drainer.drain()
        assertEquals(1, h.api.emailCreates.size, "the retry reuses the draft the first attempt created")
        assertEquals(2, h.api.sendCalls.size)
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aSendHeldForItsUndoWindowWaitsAndCanBeTakenBack() = runTest {
        val h = harness()

        val queued = h.composer.send(composeState("held send"), boxes, holdMs = 5_000) as ComposeResult.Queued
        assertEquals(h.clock.now + 5_000, queued.heldUntilMs)

        h.drainer.drain()
        assertTrue(h.api.sendCalls.isEmpty(), "the hold keeps the message on the device")

        assertNotNull(h.outbox.cancelIfQueued(queued.entryId), "undo drops the held entry")
        h.clock.now += 10_000
        h.drainer.drain()
        assertTrue(h.api.sendCalls.isEmpty(), "an undone send never reaches the server")
    }

    @Test
    fun aSendLeftAloneGoesOutOnceItsHoldHasPassed() = runTest {
        val h = harness()

        h.composer.send(composeState("held send"), boxes, holdMs = 5_000)
        h.drainer.drain()
        assertTrue(h.api.sendCalls.isEmpty())

        h.clock.now += 5_001
        h.drainer.drain()
        assertEquals(1, h.api.sendCalls.size)
    }

    @Test
    fun aHeldSendDoesNotHoldUpTheActionsBehindIt() = runTest {
        val h = harness(message("e1"))

        h.composer.send(composeState("held send"), boxes, holdMs = 30_000)
        h.actions.setFlagged(listOf(h.store.email("acct-a", "e1")!!), true)
        h.drainer.drain()

        assertTrue(h.api.sendCalls.isEmpty())
        assertEquals(1, h.api.emailSetCalls.size)
    }

    @Test
    fun anOfflineDraftSaveIsQueuedAndWrittenOnTheNextDrain() = runTest {
        val h = harness()
        h.api.composeFailure = kotlinx.io.IOException("network unreachable")

        val result = h.composer.saveDraft(composeState("queued draft"), boxes)
        assertTrue(result is ComposeResult.Queued)

        h.api.composeFailure = null
        h.drainer.drain()

        assertEquals(1, h.api.emailCreates.size)
        assertTrue(h.api.sendCalls.isEmpty(), "a draft is written, not submitted")
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aServerRefusalOfARequestIsPermanentWhileAServerFailureIsRetried() = runTest {
        val h = harness(message("e1"))
        h.api.setFailure = JmapException("forbidden", status = 403)

        h.actions.setFlagged(listOf(h.store.email("acct-a", "e1")!!), true)
        h.drainer.drain()
        assertEquals(OutboxState.FAILED, h.outbox.list().single().state)
        assertTrue(h.outbox.list().single().permanent)

        h.outbox.retry(h.outbox.list().single().id)
        h.api.setFailure = JmapException("server is restarting", status = 503)
        h.drainer.drain()
        assertEquals(OutboxState.QUEUED, h.outbox.list().single().state)
        assertTrue(!h.outbox.list().single().permanent)
    }

    @Test
    fun aRuleEntryDrainsAndTakesTheServerSRuleSetIntoTheStore() = runTest {
        val h = Harness()
        val filters = com.netzhansa.herold.shared.actions.FilterActions(h.store, h.outbox)
        h.api.ruleSetOutcome = com.netzhansa.herold.shared.jmap.RuleSetOutcome(
            created = mapOf("rule1" to com.netzhansa.herold.shared.jmap.WireManagedRule(id = "9", name = "Acme")),
        )
        h.api.rules = listOf(com.netzhansa.herold.shared.jmap.WireManagedRule(id = "9", name = "Acme"))

        filters.create(
            accountId = "acct-a",
            name = "Acme",
            conditions = listOf(
                com.netzhansa.herold.shared.domain.RuleCondition("from", "equals", "bob@example.local"),
            ),
            actions = listOf(com.netzhansa.herold.shared.domain.RuleAction("skip-inbox")),
            order = 0,
        )
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.submitted)
        assertEquals(1, h.api.ruleSetCalls.size)
        assertEquals("9", h.store.managedRuleList().single().id)
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aMuteEntryCallsThreadMuteAndARefusedRuleStaysListed() = runTest {
        val h = Harness()
        val filters = com.netzhansa.herold.shared.actions.FilterActions(h.store, h.outbox)
        filters.setMuted("acct-a", "t-1", muted = true)
        h.drainer.drain()
        assertEquals(listOf("t-1" to true), h.api.threadMuteCalls)

        h.api.ruleSetOutcome = com.netzhansa.herold.shared.jmap.RuleSetOutcome(
            errors = mapOf("rule1" to "unknown condition field"),
        )
        filters.create(
            accountId = "acct-a",
            name = "Bad",
            conditions = listOf(com.netzhansa.herold.shared.domain.RuleCondition("nope", "equals", "x")),
            actions = listOf(com.netzhansa.herold.shared.domain.RuleAction("skip-inbox")),
            order = 0,
        )
        val outcome = h.drainer.drain()
        assertEquals(1, outcome.rejected)
        val failed = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, failed.state)
        assertEquals("unknown condition field", failed.lastError)
    }
}
