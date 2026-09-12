package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.outbox.Outbox
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

private val boxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
)

private fun message(id: String) = Email(
    accountId = "acct-a",
    id = id,
    threadId = "t-$id",
    subject = "Hello",
    receivedAt = 1000,
    mailboxIds = setOf("inbox-1"),
)

class UndoCenterTest {

    private suspend fun store(vararg emails: Email) = FakeLocalStore().apply {
        upsertMailboxes(boxes)
        upsertEmails(emails.toList())
    }

    @Test
    fun anOfferIsParkedWithTheLocalWriteAndTakenOnce() = runTest {
        val store = store(message("e1"))
        val outbox = Outbox(store)
        val actions = MailActions(store, outbox)
        val centre = UndoCenter()

        val offered = centre.offer(
            UndoMessages.ARCHIVED,
            actions.archiveLocally(listOf(store.email("acct-a", "e1")!!), boxes),
            actions,
        )

        assertNotNull(offered)
        // The row is out of the inbox and the change is queued, which is
        // what the offer is offered alongside.
        assertTrue(store.email("acct-a", "e1")!!.mailboxIds.contains("archive-1"))
        assertEquals(1, outbox.list().size)

        val taken = centre.take()
        assertEquals(offered, taken)
        assertNull(centre.take(), "an offer is shown once, however many lists are watching")
    }

    @Test
    fun theUndoOfAParkedOfferRestoresTheMessage() = runTest {
        val store = store(message("e1"))
        val outbox = Outbox(store)
        val actions = MailActions(store, outbox)
        val centre = UndoCenter()

        val offer = centre.offer(
            UndoMessages.ARCHIVED,
            actions.archiveLocally(listOf(store.email("acct-a", "e1")!!), boxes),
            actions,
        )!!
        offer.undo()

        assertTrue(store.email("acct-a", "e1")!!.mailboxIds.contains("inbox-1"))
        assertTrue(!store.email("acct-a", "e1")!!.mailboxIds.contains("archive-1"))
        assertTrue(outbox.list().isEmpty(), "the undone archive leaves nothing queued")
    }

    @Test
    fun anActionThatChangesNothingIsNotOfferedAndQueuesNothing() = runTest {
        val store = store(message("e1").copy(mailboxIds = setOf("archive-1")))
        val outbox = Outbox(store)
        val actions = MailActions(store, outbox)
        val centre = UndoCenter()

        val offered = centre.offer(
            UndoMessages.ARCHIVED,
            actions.archiveLocally(listOf(store.email("acct-a", "e1")!!), boxes),
            actions,
        )

        assertNull(offered)
        assertNull(centre.take())
        assertTrue(outbox.list().isEmpty())
    }

    @Test
    fun aSnoozeIsOfferedTheSameWayAndCarriesItsWakeTime() = runTest {
        val store = store(message("e1"))
        val outbox = Outbox(store)
        val actions = MailActions(store, outbox)
        val centre = UndoCenter()

        val offer = centre.offer(
            UndoMessages.SNOOZED,
            actions.snoozeLocally(listOf(store.email("acct-a", "e1")!!), "2026-09-13T08:00:00Z"),
            actions,
        )!!

        assertEquals("2026-09-13T08:00:00Z", store.email("acct-a", "e1")!!.snoozedUntil)
        offer.undo()
        assertNull(store.email("acct-a", "e1")!!.snoozedUntil)
    }

    @Test
    fun anOfferWithItsOwnUndoRunsThatUndo() = runTest {
        val centre = UndoCenter()
        var taken = false

        val offer = centre.offer(UndoMessages.SENDING, windowMs = 5_000) { taken = true }
        assertEquals(5_000L, offer.windowMs)
        centre.take()!!.undo()

        assertTrue(taken)
    }
}
