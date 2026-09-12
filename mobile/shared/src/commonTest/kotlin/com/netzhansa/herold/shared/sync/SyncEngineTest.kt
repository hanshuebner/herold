package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.ChangesOutcome
import com.netzhansa.herold.shared.jmap.JmapAccount
import com.netzhansa.herold.shared.jmap.JmapSession
import com.netzhansa.herold.shared.jmap.WireAddress
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.jmap.WireMailbox
import com.netzhansa.herold.shared.jmap.WireThread
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

private val mailCapability = mapOf(Capability.MAIL to kotlinx.serialization.json.JsonObject(emptyMap()))

private fun session(vararg accountIds: String) = JmapSession(
    capabilities = mapOf(Capability.MAIL to kotlinx.serialization.json.JsonObject(emptyMap())),
    accounts = accountIds.associateWith { JmapAccount(name = "acct $it", accountCapabilities = mailCapability) },
    primaryAccounts = mapOf(Capability.MAIL to accountIds.first()),
    username = "alice@example.local",
    apiUrl = "https://mail.example/jmap",
)

private fun wireEmail(
    id: String,
    threadId: String = "t-$id",
    subject: String = "Subject $id",
    receivedAt: String = "2026-09-11T10:00:00Z",
    mailboxIds: Map<String, Boolean> = mapOf("inbox-1" to true),
    keywords: Map<String, Boolean> = emptyMap(),
) = WireEmail(
    id = id,
    threadId = threadId,
    mailboxIds = mailboxIds,
    keywords = keywords,
    from = listOf(WireAddress(name = "Sender $id", email = "sender@example.local")),
    subject = subject,
    receivedAt = receivedAt,
    preview = "preview of $id",
)

class SyncEngineTest {

    private fun api() = FakeJmapApi(session("acct-a")).apply {
        mailboxes = listOf(
            WireMailbox(id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
            WireMailbox(id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
            WireMailbox(id = "label-1", name = "Projects"),
        )
    }

    @Test
    fun firstPassFillsMailboxesEmailsAndThreadsAndPersistsStateStrings() = runTest {
        val api = api()
        api.inboxIds = listOf("e1", "e2")
        api.emails = mapOf(
            "e1" to wireEmail("e1"),
            "e2" to wireEmail("e2", receivedAt = "2026-09-11T11:00:00Z"),
            "e3" to wireEmail("e3", threadId = "t-e1", mailboxIds = mapOf("archive-1" to true)),
        )
        api.threads = listOf(WireThread(id = "t-e1", emailIds = listOf("e1", "e3")), WireThread(id = "t-e2", emailIds = listOf("e2")))
        val store = FakeLocalStore()

        SyncEngine(api, store).syncAll()

        assertEquals(3, store.mailboxList().size)
        // e3 belongs to a synced thread, so it is fetched even though it is
        // not in the inbox query: an opened thread is complete offline.
        assertEquals(setOf("e1", "e2", "e3"), store.emailList().map { it.id }.toSet())
        assertEquals("mailbox-1", store.syncState("acct-a", SyncTypes.MAILBOX))
        assertEquals("email-1", store.syncState("acct-a", SyncTypes.EMAIL))
        assertEquals("thread-1", store.syncState("acct-a", SyncTypes.THREAD))
        assertEquals(listOf("acct-a"), store.accountList().map { it.id })
    }

    @Test
    fun ensureThreadFetchesAThreadTheFillNeverCovered() = runTest {
        val api = api()
        // The fill sees an inbox of one; the thread the user searches for
        // sits outside it, the way an older or archived conversation does
        // (issue #339).
        api.inboxIds = listOf("e1")
        api.emails = mapOf(
            "e1" to wireEmail("e1"),
            "e9" to wireEmail("e9", threadId = "t-e9", mailboxIds = mapOf("archive-1" to true)),
        )
        api.threads = listOf(
            WireThread(id = "t-e1", emailIds = listOf("e1")),
            WireThread(id = "t-e9", emailIds = listOf("e9")),
        )
        val store = FakeLocalStore()
        val engine = SyncEngine(api, store)
        engine.syncAll()
        assertTrue(store.threadEmailList("acct-a", "t-e9").isEmpty())

        assertTrue(engine.ensureThread("acct-a", "t-e9"))

        assertEquals(listOf("e9"), store.threadEmailList("acct-a", "t-e9").map { it.id })
        assertEquals("Subject e9", store.email("acct-a", "e9")!!.subject)

        // A thread already held is answered from the store.
        val before = api.emailGetCalls.size
        assertTrue(engine.ensureThread("acct-a", "t-e9"))
        assertEquals(before, api.emailGetCalls.size)
    }

    @Test
    fun ensureThreadReportsAThreadItCannotFetch() = runTest {
        val api = api()
        api.inboxIds = emptyList()
        val store = FakeLocalStore()
        val engine = SyncEngine(api, store)

        assertFalse(engine.ensureThread("acct-a", "t-missing"))
        assertTrue(store.threadEmailList("acct-a", "t-missing").isEmpty())
    }

    @Test
    fun secondPassAsksForChangesRatherThanRefetchingEverything() = runTest {
        val api = api()
        api.inboxIds = listOf("e1")
        api.emails = mapOf("e1" to wireEmail("e1"))
        api.threads = listOf(WireThread(id = "t-e1", emailIds = listOf("e1")))
        val store = FakeLocalStore()
        val engine = SyncEngine(api, store)
        engine.syncAll()
        api.emailGetCalls.clear()
        api.inboxQueryCalls = 0

        api.emails = api.emails + ("e9" to wireEmail("e9"))
        api.emailChanges = ChangesOutcome.Changed(
            newState = "email-2",
            created = listOf("e9"),
            updated = emptyList(),
            destroyed = listOf("e1"),
            hasMoreChanges = false,
        )
        engine.syncAll()

        assertEquals(0, api.inboxQueryCalls, "a cold start with persisted state must not re-run the inbox query")
        assertEquals(listOf(listOf("e9")), api.emailGetCalls)
        assertEquals(listOf("e9"), store.emailList().map { it.id })
        assertEquals("email-2", store.syncState("acct-a", SyncTypes.EMAIL))
    }

    @Test
    fun cannotCalculateChangesDropsThatTypeAndRefetchesIt() = runTest {
        val api = api()
        api.inboxIds = listOf("e1")
        api.emails = mapOf("e1" to wireEmail("e1"))
        api.threads = listOf(WireThread(id = "t-e1", emailIds = listOf("e1")))
        val store = FakeLocalStore()
        val engine = SyncEngine(api, store)
        engine.syncAll()

        api.emailChanges = ChangesOutcome.CannotCalculate
        api.inboxIds = listOf("e2")
        api.emails = mapOf("e2" to wireEmail("e2"))
        api.threads = listOf(WireThread(id = "t-e2", emailIds = listOf("e2")))
        api.emailState = "email-after-reset"
        engine.syncAll()

        assertEquals(listOf("e2"), store.emailList().map { it.id }, "the stale rows are dropped, not merged")
        assertEquals("email-after-reset", store.syncState("acct-a", SyncTypes.EMAIL))
    }

    @Test
    fun walksEveryAccountTheSessionAdvertises() = runTest {
        val api = FakeJmapApi(session("acct-a", "acct-b"))
        api.mailboxes = listOf(WireMailbox(id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX))
        api.inboxIds = listOf("e1")
        api.emails = mapOf("e1" to wireEmail("e1"))
        val store = FakeLocalStore()

        SyncEngine(api, store).syncAll()

        assertEquals(listOf("acct-a", "acct-b"), store.accountList().map { it.id })
        assertTrue(store.accountList().first().isPrimary)
        assertEquals(setOf("acct-a", "acct-b"), store.emailList().map { it.accountId }.toSet())
        assertEquals("email-1", store.syncState("acct-b", SyncTypes.EMAIL))
    }

    @Test
    fun aFailingPassLeavesTheStoreIntactAndReportsTheFailure() = runTest {
        val api = FakeJmapApi(session("acct-a")).apply {
            readFailure = com.netzhansa.herold.shared.jmap.JmapException("unauthorized", status = 401)
        }
        val store = FakeLocalStore()

        val status = SyncEngine(api, store).syncAll()

        assertTrue(status is SyncStatus.Failed)
        assertTrue((status as SyncStatus.Failed).unauthorized)
        assertTrue(store.emailList().isEmpty())
        assertNull(store.syncState("acct-a", SyncTypes.EMAIL))
    }

    @Test
    fun loadBodyCachesTheRenderedBodySoTheThreadOpensOffline() = runTest {
        val api = api()
        api.inboxIds = listOf("e1")
        api.emails = mapOf("e1" to wireEmail("e1"))
        api.threads = listOf(WireThread(id = "t-e1", emailIds = listOf("e1")))
        val store = FakeLocalStore()
        val engine = SyncEngine(api, store)
        engine.syncAll()

        api.emails = mapOf(
            "e1" to wireEmail("e1").copy(
                htmlBody = listOf(com.netzhansa.herold.shared.jmap.WireBodyPart(partId = "1", type = "text/html")),
                bodyValues = mapOf("1" to com.netzhansa.herold.shared.jmap.WireBodyValue(value = "<p>hello</p>")),
            ),
        )
        val loaded = engine.loadBody("acct-a", "e1")
        assertEquals("<p>hello</p>", loaded?.bodyHtml)

        // A metadata re-sync must not drop the cached body.
        api.emails = mapOf("e1" to wireEmail("e1"))
        api.emailChanges = ChangesOutcome.Changed("email-3", emptyList(), listOf("e1"), emptyList(), false)
        engine.syncAll()
        assertEquals("<p>hello</p>", store.email("acct-a", "e1")?.bodyHtml)
    }
}
