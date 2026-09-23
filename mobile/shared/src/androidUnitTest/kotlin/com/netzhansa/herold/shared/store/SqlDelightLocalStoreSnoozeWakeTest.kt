package com.netzhansa.herold.shared.store

import app.cash.sqldelight.driver.jdbc.sqlite.JdbcSqliteDriver
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * The snooze wake marker through the store the app ships (issue #470):
 * the two columns migration 9 adds hold what the server sent, and a later
 * response carrying them null takes the marker off the row - which is how
 * the indication ends when the conversation is read.
 */
class SqlDelightLocalStoreSnoozeWakeTest {

    private fun store(): SqlDelightLocalStore {
        val driver = JdbcSqliteDriver(JdbcSqliteDriver.IN_MEMORY)
        HeroldDatabase.Schema.create(driver)
        return SqlDelightLocalStore(
            database = HeroldDatabase(driver),
            blobFiles = InMemoryBlobFileStore(),
            dispatcher = Dispatchers.Default,
            now = { 1_000_000L },
            tombstones = Tombstones({ 1_000_000L }),
            membershipHolds = MembershipHolds({ 1_000_000L }, 120_000L),
        )
    }

    private fun woken(wokeAt: String?, wokeFor: String?) = Email(
        accountId = "acct-a",
        id = "e1",
        threadId = "t1",
        subject = "The reminder",
        receivedAt = 1000,
        mailboxIds = setOf("inbox-1"),
        snoozeWokeAt = wokeAt,
        snoozeWokeFor = wokeFor,
    )

    @Test
    fun theWakeMarkerRoundTripsAndALaterNullTakesItOff() = runBlocking {
        val store = store()
        store.upsertEmails(listOf(woken("2026-09-23T08:00:04Z", "2026-09-23T08:00:00Z")))

        val marked = store.email("acct-a", "e1")!!
        assertEquals("2026-09-23T08:00:04Z", marked.snoozeWokeAt)
        assertEquals("2026-09-23T08:00:00Z", marked.snoozeWokeFor)

        store.upsertEmails(listOf(woken(null, null)))

        val cleared = store.email("acct-a", "e1")!!
        assertNull(cleared.snoozeWokeAt)
        assertNull(cleared.snoozeWokeFor)
    }
}
