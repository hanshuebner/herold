package com.netzhansa.herold.shared.store

import app.cash.sqldelight.driver.jdbc.sqlite.JdbcSqliteDriver
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlin.test.Test
import kotlin.test.assertEquals

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "junk-1", name = "Spam", role = MailboxRoles.JUNK),
    Mailbox(accountId = "acct-a", id = "trash-1", name = "Trash", role = MailboxRoles.TRASH),
    Mailbox(accountId = "acct-a", id = "label-1", name = "Receipts"),
)

private fun message(id: String, mailboxIds: Set<String>, receivedAt: Long, snoozedUntil: String? = null) = Email(
    accountId = "acct-a",
    id = id,
    threadId = "t-$id",
    subject = "Subject $id",
    receivedAt = receivedAt,
    keywords = emptySet(),
    mailboxIds = mailboxIds,
    snoozedUntil = snoozedUntil,
)

/**
 * The Junk exclusion of the list queries (issue #467), driven through the
 * store the app ships so the SQL itself is under test: a classifier
 * verdict files a message to Junk while its Inbox membership may remain,
 * and the inbox must not show it. Trash carries no rule here - Trash
 * never coexists with another mailbox (issue #460), and a message that
 * holds both is listed.
 */
class SqlDelightLocalStoreJunkExclusionTest {

    private fun store(): SqlDelightLocalStore {
        val driver = JdbcSqliteDriver(JdbcSqliteDriver.IN_MEMORY)
        HeroldDatabase.Schema.create(driver)
        return SqlDelightLocalStore(
            database = HeroldDatabase(driver),
            blobFiles = InMemoryBlobFileStore(),
            dispatcher = Dispatchers.Default,
        )
    }

    private fun seeded(): SqlDelightLocalStore = store().also {
        runBlocking {
            it.upsertMailboxes(mailboxes)
            it.upsertEmails(
                listOf(
                    message("junked", setOf("inbox-1", "junk-1"), receivedAt = 3000),
                    message("trashed", setOf("inbox-1", "trash-1"), receivedAt = 2000),
                    message("plain", setOf("inbox-1"), receivedAt = 1000),
                ),
            )
        }
    }

    @Test
    fun theInboxLeavesOutAMessageThatAlsoSitsInJunk() = runBlocking {
        assertEquals(
            listOf("trashed", "plain"),
            seeded().inboxEmails().first().map { it.id },
            "a message in Inbox and Junk is out of the inbox; one in Inbox and Trash is listed",
        )
    }

    @Test
    fun theJunkDestinationListsTheJunkedMessage() = runBlocking {
        assertEquals(
            listOf("junked"),
            seeded().mailboxEmails(listOf("junk-1")).first().map { it.id },
            "the Junk view lists what is filed there",
        )
    }

    @Test
    fun aLabelLeavesOutAJunkedMessageAndTheTrashViewListsItsOwn() = runBlocking {
        val store = seeded()
        store.upsertEmails(
            listOf(
                message("labelled-junk", setOf("label-1", "junk-1"), receivedAt = 4000),
                message("labelled", setOf("label-1"), receivedAt = 500),
            ),
        )
        assertEquals(
            listOf("labelled"),
            store.mailboxEmails(listOf("label-1")).first().map { it.id },
            "a label view leaves out a message the classifier filed to Junk",
        )
        assertEquals(
            listOf("trashed"),
            store.mailboxEmails(listOf("trash-1")).first().map { it.id },
            "the Trash view lists what is filed there",
        )
    }

    @Test
    fun theSnoozedListLeavesOutAJunkedMessage() = runBlocking {
        val store = seeded()
        store.upsertEmails(
            listOf(
                message("sleeping", setOf("inbox-1"), receivedAt = 900, snoozedUntil = "2026-10-01T08:00:00Z"),
                message(
                    "sleeping-junk",
                    setOf("inbox-1", "junk-1"),
                    receivedAt = 950,
                    snoozedUntil = "2026-09-30T08:00:00Z",
                ),
            ),
        )
        assertEquals(
            listOf("sleeping"),
            store.snoozedEmails().first().map { it.id },
            "the snoozed list leaves out a message the classifier filed to Junk",
        )
    }
}
