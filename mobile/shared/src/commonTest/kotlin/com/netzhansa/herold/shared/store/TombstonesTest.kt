package com.netzhansa.herold.shared.store

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.fake.FakeLocalStore
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull

private fun message(id: String) = Email(
    accountId = "acct-a",
    id = id,
    threadId = "t-1",
    subject = "Hello",
    receivedAt = 1000,
    mailboxIds = setOf("inbox-1"),
)

/**
 * A row the client took away stays away while the requests that were in
 * flight when it went are still landing (issue #371).
 */
class TombstonesTest {

    private class Clock(var now: Long = 1_000)

    @Test
    fun aDeletedRowIsNotWrittenBackByAFetchInFlight() = runTest {
        val store = FakeLocalStore()
        store.upsertEmails(listOf(message("m1")))

        store.deleteEmails("acct-a", listOf("m1"))
        // The Email/get that was already on its way answers now.
        store.upsertEmails(listOf(message("m1")))

        assertNull(store.email("acct-a", "m1"))
    }

    @Test
    fun otherRowsOfTheSameWriteStillLand() = runTest {
        val store = FakeLocalStore()
        store.upsertEmails(listOf(message("m1"), message("m2")))
        store.deleteEmails("acct-a", listOf("m1"))

        store.upsertEmails(listOf(message("m1"), message("m2")))

        assertNull(store.email("acct-a", "m1"))
        assertNotNull(store.email("acct-a", "m2"))
    }

    @Test
    fun theHoldRunsOutSoTheIdIsNotKeptForever() = runTest {
        val clock = Clock()
        val tombstones = Tombstones({ clock.now }, ttlMs = 1_000)
        val store = FakeLocalStore(tombstones)
        store.upsertEmails(listOf(message("m1")))
        store.deleteEmails("acct-a", listOf("m1"))

        clock.now += 1_001
        store.upsertEmails(listOf(message("m1")))

        assertEquals("m1", store.email("acct-a", "m1")?.id)
    }

    @Test
    fun aForgottenIdIsWritableAgain() = runTest {
        val tombstones = Tombstones()
        val store = FakeLocalStore(tombstones)
        store.upsertEmails(listOf(message("m1")))
        store.deleteEmails("acct-a", listOf("m1"))

        // What a refused destroy does: the server still has it.
        tombstones.forget("acct-a", listOf("m1"))
        store.upsertEmails(listOf(message("m1")))

        assertEquals("m1", store.email("acct-a", "m1")?.id)
    }
}
