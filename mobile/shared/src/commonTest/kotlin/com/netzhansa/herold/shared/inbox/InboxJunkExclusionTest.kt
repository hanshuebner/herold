package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeLocalStore
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals

private val account = Account(id = "acct-a", name = "Alice", isPrimary = true)

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "junk-1", name = "Spam", role = MailboxRoles.JUNK),
    Mailbox(accountId = "acct-a", id = "trash-1", name = "Trash", role = MailboxRoles.TRASH),
)

private fun message(id: String, mailboxIds: Set<String>, receivedAt: Long) = Email(
    accountId = "acct-a",
    id = id,
    threadId = "t-$id",
    subject = "Subject $id",
    receivedAt = receivedAt,
    keywords = emptySet(),
    mailboxIds = mailboxIds,
)

/**
 * The inbox lane against the Junk rule (issue #467): a classifier verdict
 * files a message to Junk while its Inbox membership may remain, and
 * neither the store's inbox list nor the lane the assembler folds from it
 * shows that message. A message in Inbox and Trash is listed, since Trash
 * never coexists with another mailbox (issue #460) and the pair is a
 * store defect rather than something the list hides.
 */
class InboxJunkExclusionTest {

    private suspend fun seeded(): FakeLocalStore = FakeLocalStore().also {
        it.replaceAccounts(listOf(account))
        it.upsertMailboxes(mailboxes)
        it.upsertEmails(
            listOf(
                message("junked", setOf("inbox-1", "junk-1"), receivedAt = 3000),
                message("trashed", setOf("inbox-1", "trash-1"), receivedAt = 2000),
                message("plain", setOf("inbox-1"), receivedAt = 1000),
            ),
        )
    }

    @Test
    fun theInboxLeavesOutAJunkedMessageAndKeepsATrashedOne() = runTest {
        assertEquals(
            listOf("trashed", "plain"),
            seeded().inboxEmails().first().map { it.id },
        )
    }

    @Test
    fun theLanesFoldedFromTheInboxCarryNoJunkedConversation() = runTest {
        val store = seeded()
        val rows = InboxAssembler.threadRows(
            emails = store.inboxEmails().first(),
            accounts = store.accountList(),
            mailboxes = store.mailboxList(),
        )
        assertEquals(listOf("t-trashed", "t-plain"), rows.map { it.threadId })
    }

    @Test
    fun theJunkDestinationListsTheJunkedMessage() = runTest {
        val store = seeded()
        assertEquals(listOf("junked"), store.mailboxEmails(listOf("junk-1")).first().map { it.id })
    }

    @Test
    fun theHomeSurfacesLeaveOutAJunkedConversation() = runTest {
        val store = seeded()
        val snapshot = HomeSnapshot.from(
            emails = store.inboxEmails().first(),
            accounts = store.accountList(),
            mailboxes = store.mailboxList(),
        )
        assertEquals(listOf("t-trashed", "t-plain"), snapshot.threads.map { it.threadId })
    }
}
