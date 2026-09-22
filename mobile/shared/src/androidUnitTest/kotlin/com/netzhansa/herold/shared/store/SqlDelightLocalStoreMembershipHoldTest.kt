package com.netzhansa.herold.shared.store

import app.cash.sqldelight.driver.jdbc.sqlite.JdbcSqliteDriver
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import kotlin.test.Test
import kotlin.test.assertEquals

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
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
 * The response-after-action ordering (issue #473) driven through
 * [SqlDelightLocalStore] itself, over an in-memory host-JVM database -
 * the store the app ships, not only [com.netzhansa.herold.shared.fake.FakeLocalStore]'s
 * in-memory mirror. `upsertEmails`' merge against a membership hold lives
 * in `frozen()`/`heldForMembership`, which only this store has: a test
 * that only ever drives the fake cannot fail the way the shipped code
 * could.
 */
class SqlDelightLocalStoreMembershipHoldTest {

    private class Clock(var now: Long = 1_000_000)

    private fun store(clock: Clock, ttlMs: Long = 120_000L): SqlDelightLocalStore {
        val driver = JdbcSqliteDriver(JdbcSqliteDriver.IN_MEMORY)
        HeroldDatabase.Schema.create(driver)
        val database = HeroldDatabase(driver)
        return SqlDelightLocalStore(
            database = database,
            blobFiles = InMemoryBlobFileStore(),
            dispatcher = Dispatchers.Default,
            now = { clock.now },
            tombstones = Tombstones({ clock.now }),
            membershipHolds = MembershipHolds({ clock.now }, ttlMs),
        )
    }

    @Test
    fun anArchiveHoldsTheRowAgainstAResponseInFlightOnTheRealStore() = runBlocking {
        val store = store(Clock())
        store.upsertMailboxes(mailboxes)
        store.upsertEmails(listOf(seeded()))
        val before = store.email("acct-a", "e1")!!

        store.holdMembership("acct-a", listOf("e1"))
        store.updateMembership(
            accountId = "acct-a",
            id = "e1",
            keywords = before.keywords,
            mailboxIds = setOf("archive-1"),
            snoozedUntil = before.snoozedUntil,
        )
        // The Email/get that was already on its way when the swipe
        // happened answers now, with the pre-archive mailboxes.
        store.upsertEmails(listOf(before))

        assertEquals(
            setOf("archive-1"),
            store.email("acct-a", "e1")!!.mailboxIds,
            "the archive must not flicker back into the inbox",
        )
    }

    @Test
    fun aHeldRowStillTakesTheRestOfAResponseOnTheRealStore() = runBlocking {
        val store = store(Clock())
        store.upsertMailboxes(mailboxes)
        store.upsertEmails(listOf(seeded()))
        val before = store.email("acct-a", "e1")!!

        store.holdMembership("acct-a", listOf("e1"))
        store.updateMembership(
            accountId = "acct-a",
            id = "e1",
            keywords = before.keywords,
            mailboxIds = setOf("archive-1"),
            snoozedUntil = before.snoozedUntil,
        )
        store.upsertEmails(listOf(before.copy(subject = "Updated", preview = "a new preview")))

        val after = store.email("acct-a", "e1")!!
        assertEquals(setOf("archive-1"), after.mailboxIds, "membership stays held")
        assertEquals("Updated", after.subject, "the rest of the row still lands")
        assertEquals("a new preview", after.preview)
    }

    @Test
    fun releasingTheHoldLetsTheNextFetchThroughOnTheRealStore() = runBlocking {
        val store = store(Clock())
        store.upsertMailboxes(mailboxes)
        store.upsertEmails(listOf(seeded()))
        val before = store.email("acct-a", "e1")!!

        store.holdMembership("acct-a", listOf("e1"))
        store.updateMembership(
            accountId = "acct-a",
            id = "e1",
            keywords = before.keywords,
            mailboxIds = setOf("archive-1"),
            snoozedUntil = before.snoozedUntil,
        )
        store.releaseMembershipHold("acct-a", listOf("e1"))
        store.upsertEmails(listOf(before.copy(mailboxIds = setOf("archive-1", "inbox-1"))))

        assertEquals(setOf("archive-1", "inbox-1"), store.email("acct-a", "e1")!!.mailboxIds)
    }

    @Test
    fun theHoldExpiresOnTheRealStore() = runBlocking {
        val clock = Clock()
        val store = store(clock, ttlMs = 1_000)
        store.upsertMailboxes(mailboxes)
        store.upsertEmails(listOf(seeded()))
        val before = store.email("acct-a", "e1")!!

        store.holdMembership("acct-a", listOf("e1"))
        store.updateMembership(
            accountId = "acct-a",
            id = "e1",
            keywords = before.keywords,
            mailboxIds = setOf("archive-1"),
            snoozedUntil = before.snoozedUntil,
        )
        clock.now += 1_001
        store.upsertEmails(listOf(before))

        assertEquals(
            setOf("inbox-1"),
            store.email("acct-a", "e1")!!.mailboxIds,
            "an expired hold must let a later response through",
        )
    }
}
