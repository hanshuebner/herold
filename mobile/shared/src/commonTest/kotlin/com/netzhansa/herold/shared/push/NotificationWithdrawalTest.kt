package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.fake.FakePostedNotifications
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.ChangesOutcome
import com.netzhansa.herold.shared.jmap.JmapAccount
import com.netzhansa.herold.shared.jmap.JmapSession
import com.netzhansa.herold.shared.jmap.WireAddress
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.jmap.WireMailbox
import com.netzhansa.herold.shared.jmap.WireThread
import com.netzhansa.herold.shared.sync.SyncEngine
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

private const val ACCOUNT = "acct-a"
private const val THREAD = "t-e1"

private val mailCapability = mapOf(Capability.MAIL to JsonObject(emptyMap()))

private fun session() = JmapSession(
    capabilities = mailCapability,
    accounts = mapOf(ACCOUNT to JmapAccount(name = "acct", accountCapabilities = mailCapability)),
    primaryAccounts = mapOf(Capability.MAIL to ACCOUNT),
    username = "alice@example.local",
    apiUrl = "https://mail.example/jmap",
)

private fun wireEmail(
    id: String,
    threadId: String = THREAD,
    mailboxIds: Map<String, Boolean> = mapOf("inbox-1" to true),
    keywords: Map<String, Boolean> = emptyMap(),
) = WireEmail(
    id = id,
    threadId = threadId,
    mailboxIds = mailboxIds,
    keywords = keywords,
    from = listOf(WireAddress(name = "Sender", email = "sender@example.local")),
    subject = "Subject $id",
    receivedAt = "2026-09-22T10:00:00Z",
    preview = "preview of $id",
)

/**
 * A notification stands for an unread inbox message, so it goes when the
 * message stops being one - whichever client read it, filed it away or
 * deleted it (issue #481, REQ-AND-PUSH-14). The fold is where the client
 * learns of the change, so the fold is where the shade is corrected.
 */
class NotificationWithdrawalTest {

    private val api = FakeJmapApi(session()).apply {
        mailboxes = listOf(
            WireMailbox(id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
            WireMailbox(id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
        )
    }
    private val store = FakeLocalStore()
    private val notifier = FakePostedNotifications()
    private val engine = SyncEngine(api, store, notifications = notifier)

    /** An inbox holding [ids], synced once so the next pass is a fold. */
    private suspend fun inboxOf(vararg ids: String) {
        api.inboxIds = ids.toList()
        api.emails = ids.associateWith { wireEmail(it) }
        api.threads = listOf(WireThread(id = THREAD, emailIds = ids.toList()))
        engine.syncAll()
    }

    /** A fold that carries [updated] as the server now holds them. */
    private suspend fun fold(
        updated: Map<String, WireEmail> = emptyMap(),
        destroyed: List<String> = emptyList(),
    ) {
        api.emails = api.emails + updated
        api.emailChanges = ChangesOutcome.Changed(
            newState = "email-" + (api.emailChangesCalls + 2),
            created = emptyList(),
            updated = updated.keys.toList(),
            destroyed = destroyed,
            hasMoreChanges = false,
        )
        engine.syncAll()
    }

    @Test
    fun aMessageReadOnAnotherClientLosesItsNotification() = runTest {
        inboxOf("e1")
        notifier.post(ACCOUNT, THREAD, "e1")

        fold(updated = mapOf("e1" to wireEmail("e1", keywords = mapOf(Keywords.SEEN to true))))

        assertEquals(listOf(THREAD), notifier.cancelled)
        assertEquals(DismissReason.SEEN, notifier.reasonFor("e1"))
    }

    @Test
    fun aMessageArchivedOnAnotherClientLosesItsNotification() = runTest {
        inboxOf("e1")
        notifier.post(ACCOUNT, THREAD, "e1")

        fold(updated = mapOf("e1" to wireEmail("e1", mailboxIds = mapOf("archive-1" to true))))

        assertEquals(listOf(THREAD), notifier.cancelled)
        assertEquals(DismissReason.LEFT_INBOX, notifier.reasonFor("e1"))
    }

    @Test
    fun aDestroyedMessageLosesItsNotification() = runTest {
        inboxOf("e1")
        notifier.post(ACCOUNT, THREAD, "e1")

        fold(destroyed = listOf("e1"))

        assertEquals(listOf(THREAD), notifier.cancelled)
        assertEquals(DismissReason.DESTROYED, notifier.reasonFor("e1"))
    }

    @Test
    fun aFoldThatLeavesTheMessageUnreadAndInTheInboxLeavesTheNotificationAlone() = runTest {
        inboxOf("e1")
        notifier.post(ACCOUNT, THREAD, "e1")

        fold(updated = mapOf("e1" to wireEmail("e1", keywords = mapOf(Keywords.FLAGGED to true))))

        assertTrue(notifier.withdrawals.isEmpty(), "nothing about the message resolved it")
        assertTrue(notifier.shows(THREAD))
    }

    /**
     * A push posts its notification whether or not its bounded reconcile
     * got the message into the store, so a message the store does not
     * hold is waiting as often as it is gone. The fold names what it
     * destroyed; anything else keeps its notification.
     */
    @Test
    fun aNotifiedMessageTheStoreDoesNotHoldKeepsItsNotification() = runTest {
        inboxOf("e1")
        notifier.post(ACCOUNT, THREAD, "e-not-yet-fetched")

        fold(updated = mapOf("e1" to wireEmail("e1")))

        assertTrue(notifier.withdrawals.isEmpty())
        assertTrue(notifier.shows(THREAD))
    }

    /**
     * Two messages of one conversation share one notification, so it
     * survives the first of them being read and goes with the last.
     */
    @Test
    fun aThreadsNotificationStandsUntilItsLastNotifiedMessageIsResolved() = runTest {
        inboxOf("e1", "e2")
        notifier.post(ACCOUNT, THREAD, "e1", "e2")

        fold(updated = mapOf("e1" to wireEmail("e1", keywords = mapOf(Keywords.SEEN to true))))

        assertEquals(listOf("e1"), notifier.withdrawals.map { it.emailId })
        assertTrue(notifier.cancelled.isEmpty(), "the unread message still has something to say")
        assertTrue(notifier.shows(THREAD))

        fold(updated = mapOf("e2" to wireEmail("e2", keywords = mapOf(Keywords.SEEN to true))))

        assertEquals(listOf("e1", "e2"), notifier.withdrawals.map { it.emailId })
        assertEquals(listOf(THREAD), notifier.cancelled)
        assertFalse(notifier.shows(THREAD))
    }

    /**
     * A refetch - the server cannot calculate the changes - rewrites the
     * account's mail, and the notifications are measured against what it
     * wrote.
     */
    @Test
    fun aFullFillWithdrawsWhatItsMailboxesNoLongerJustify() = runTest {
        inboxOf("e1")
        notifier.post(ACCOUNT, THREAD, "e1")

        api.emails = mapOf("e1" to wireEmail("e1", keywords = mapOf(Keywords.SEEN to true)))
        api.emailChanges = ChangesOutcome.CannotCalculate
        api.emailState = "email-after-refetch"
        engine.syncAll()

        assertEquals(listOf(THREAD), notifier.cancelled)
        assertEquals(DismissReason.SEEN, notifier.reasonFor("e1"))
    }
}
