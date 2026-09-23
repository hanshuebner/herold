package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.inbox.InboxAssembler
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.ChangesOutcome
import com.netzhansa.herold.shared.jmap.JmapAccount
import com.netzhansa.herold.shared.jmap.JmapSession
import com.netzhansa.herold.shared.jmap.WireAddress
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.jmap.WireMailbox
import com.netzhansa.herold.shared.jmap.WireThread
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The snooze wake marker (issue #470, server issue #469): the server
 * stamps `snoozeWokeAt`/`snoozeWokeFor` on a message its snooze worker
 * released, and clears them when the message gains `$seen` or is snoozed
 * again. The fold carries both transitions into the local store, and the
 * inbox row derives from them the reminder it names.
 */
class SnoozeWakeMarkerTest {

    private val mailCapability = mapOf(Capability.MAIL to JsonObject(emptyMap()))

    private fun session() = JmapSession(
        capabilities = mailCapability,
        accounts = mapOf("acct-a" to JmapAccount(name = "acct a", accountCapabilities = mailCapability)),
        primaryAccounts = mapOf(Capability.MAIL to "acct-a"),
        username = "alice@example.local",
        apiUrl = "https://mail.example/jmap",
    )

    private fun wireEmail(
        wokeAt: String? = null,
        wokeFor: String? = null,
    ) = WireEmail(
        id = "e1",
        threadId = "t-e1",
        mailboxIds = mapOf("inbox-1" to true),
        keywords = emptyMap(),
        from = listOf(WireAddress(name = "Bob", email = "bob@example.local")),
        subject = "The reminder",
        receivedAt = "2026-09-22T10:00:00Z",
        preview = "waited a while",
        snoozeWokeAt = wokeAt,
        snoozeWokeFor = wokeFor,
    )

    private fun api(email: WireEmail) = FakeJmapApi(session()).apply {
        mailboxes = listOf(WireMailbox(id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX))
        inboxIds = listOf("e1")
        emails = mapOf("e1" to email)
        threads = listOf(WireThread(id = "t-e1", emailIds = listOf("e1")))
    }

    private val accounts = listOf(Account(id = "acct-a", name = "Alice", isPrimary = true))
    private val mailboxes = listOf(
        Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    )

    private suspend fun rowOf(store: FakeLocalStore) =
        InboxAssembler.threadRows(store.emailList(), accounts, mailboxes).single()

    @Test
    fun aFoldCarryingTheMarkerMarksTheConversation() = runTest {
        val store = FakeLocalStore()
        SyncEngine(api(wireEmail(wokeAt = WOKE_AT, wokeFor = WOKE_FOR)), store).syncAll()

        val stored = store.email("acct-a", "e1")!!
        assertEquals(WOKE_AT, stored.snoozeWokeAt)
        assertEquals(WOKE_FOR, stored.snoozeWokeFor)
        assertTrue(stored.wokeFromSnooze)
        assertEquals(WOKE_FOR, rowOf(store).wokeFor)
    }

    @Test
    fun aFoldThatNullsTheMarkerTakesTheMarkerOffTheConversation() = runTest {
        val store = FakeLocalStore()
        val api = api(wireEmail(wokeAt = WOKE_AT, wokeFor = WOKE_FOR))
        val engine = SyncEngine(api, store)
        engine.syncAll()

        // Reading the message clears the marker on the server, which
        // reaches the client as an Email/changes update carrying both
        // properties null.
        api.emails = mapOf("e1" to wireEmail().copy(keywords = mapOf(Keywords.SEEN to true)))
        api.emailChanges = ChangesOutcome.Changed(
            newState = "email-2",
            created = emptyList(),
            updated = listOf("e1"),
            destroyed = emptyList(),
            hasMoreChanges = false,
        )
        engine.syncAll()

        val stored = store.email("acct-a", "e1")!!
        assertNull(stored.snoozeWokeAt)
        assertNull(stored.snoozeWokeFor)
        assertTrue(!stored.wokeFromSnooze)
        assertNull(rowOf(store).wokeFor)
    }

    @Test
    fun aConversationThatWasNeverSnoozedCarriesNoMarker() = runTest {
        val store = FakeLocalStore()
        SyncEngine(api(wireEmail()), store).syncAll()

        val stored = store.email("acct-a", "e1")!!
        assertNull(stored.snoozeWokeFor)
        assertTrue(!stored.wokeFromSnooze)
        assertNull(rowOf(store).wokeFor)
    }

    private companion object {
        const val WOKE_AT = "2026-09-23T08:00:04Z"
        const val WOKE_FOR = "2026-09-23T08:00:00Z"
    }
}
