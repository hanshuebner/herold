package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import kotlin.test.Test
import kotlin.test.assertEquals

class HomeSnapshotTest {

    private fun email(id: String, thread: String, unread: Boolean, at: Long) = Email(
        accountId = "a1",
        id = id,
        threadId = thread,
        subject = "Subject $id",
        preview = "preview",
        receivedAt = at,
        keywords = if (unread) emptySet() else setOf("\$seen"),
        mailboxIds = setOf("inbox"),
    )

    @Test
    fun countsUnreadConversationsAndTakesTheNewest() {
        val snapshot = HomeSnapshot.from(
            emails = listOf(
                email("m1", "t1", unread = true, at = 300),
                email("m2", "t2", unread = false, at = 200),
                email("m3", "t3", unread = true, at = 100),
            ),
            accounts = listOf(Account("a1", "alice@example.local", isPrimary = true)),
            mailboxes = emptyList(),
            limit = 2,
        )
        assertEquals(2, snapshot.unread)
        assertEquals(listOf("t1", "t2"), snapshot.threads.map { it.threadId })
    }

    @Test
    fun anEmptyStoreRendersAnEmptySnapshot() {
        assertEquals(HomeSnapshot.EMPTY, HomeSnapshot.from(emptyList(), emptyList(), emptyList()))
    }
}
