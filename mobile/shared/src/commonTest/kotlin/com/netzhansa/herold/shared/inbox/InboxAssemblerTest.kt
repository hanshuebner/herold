package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

private val accounts = listOf(
    Account(id = "acct-a", name = "alice@example.local", isPrimary = true),
    Account(id = "acct-b", name = "vorsitz@cc.example", isPrimary = false, sortOrder = 1),
)

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "label-1", name = "Projects"),
    Mailbox(accountId = "acct-b", id = "inbox-2", name = "Inbox", role = MailboxRoles.INBOX),
)

private fun email(
    id: String,
    accountId: String = "acct-a",
    threadId: String = "t-$id",
    receivedAt: Long,
    subject: String = "Subject $id",
    keywords: Set<String> = setOf(Keywords.SEEN),
    mailboxIds: Set<String> = setOf("inbox-1"),
    sender: String = "Sender $id",
) = Email(
    accountId = accountId,
    id = id,
    threadId = threadId,
    fromName = sender,
    fromEmail = "$id@example.local",
    subject = subject,
    preview = "preview $id",
    receivedAt = receivedAt,
    keywords = keywords,
    mailboxIds = mailboxIds,
)

class InboxAssemblerTest {

    @Test
    fun collapsesMessagesIntoThreadRowsNewestFirstAcrossAccounts() {
        val emails = listOf(
            email("e1", receivedAt = 1000, threadId = "t1"),
            email("e2", receivedAt = 3000, threadId = "t1", keywords = emptySet()),
            email("e3", accountId = "acct-b", receivedAt = 2000, mailboxIds = setOf("inbox-2")),
        )

        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals(listOf("t1", "t-e3"), rows.map { it.threadId })
        val thread = rows.first()
        assertEquals(2, thread.messageCount)
        assertEquals(3000, thread.receivedAt)
        assertTrue(thread.isUnread, "a thread with one unread message reads as unread")
        assertEquals("alice@example.local", thread.accountName)
        assertEquals("vorsitz@cc.example", rows[1].accountName)
    }

    @Test
    fun scopeSwitcherNarrowsToOneAccount() {
        val emails = listOf(
            email("e1", receivedAt = 1000),
            email("e3", accountId = "acct-b", receivedAt = 2000, mailboxIds = setOf("inbox-2")),
        )

        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes, accountScope = "acct-b")

        assertEquals(listOf("acct-b"), rows.map { it.accountId })
    }

    @Test
    fun labelChipsComeFromTheCustomMailboxesTheMessageIsIn() {
        val emails = listOf(email("e1", receivedAt = 1000, mailboxIds = setOf("inbox-1", "label-1")))

        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals(listOf("Projects"), rows.single().labels)
    }

    @Test
    fun aPinnedTabFiltersTheStreamToItsCategory() {
        val lanes = CategoryLanes.from(listOf("Primary", "Promotions"), emptyList())
        val emails = listOf(
            email("e1", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Primary"))),
            email("e2", receivedAt = 2000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e3", receivedAt = 1000),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        val all = InboxAssembler.stream(rows, lanes, selectedCategory = null)
        assertEquals(3, all.size, "the default stream is complete")

        val promotions = InboxAssembler.stream(rows, lanes, selectedCategory = "promotions")
        assertEquals(
            listOf("t-e2"),
            promotions.filterIsInstance<InboxItem.Conversation>().map { it.row.threadId },
        )
    }

    @Test
    fun aBundledCategoryCollapsesToOneRowPositionedByItsNewestMember() {
        val lanes = CategoryLanes(pinned = listOf("primary"), bundled = listOf("promotions"))
        val emails = listOf(
            email("e1", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e2", receivedAt = 2500, keywords = setOf(Keywords.categoryKeyword("Promotions"), Keywords.SEEN)),
            email("e3", receivedAt = 2000),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        val stream = InboxAssembler.stream(rows, lanes, selectedCategory = null)

        assertEquals(2, stream.size)
        val bundle = stream.first() as InboxItem.Bundle
        assertEquals("promotions", bundle.row.category)
        assertEquals(2, bundle.row.threadCount)
        assertEquals(3000, bundle.row.receivedAt)
        assertTrue(stream[1] is InboxItem.Conversation)
    }

    @Test
    fun lanesTakeTheFirstFiveCategoriesAsTabsAndBundleTheRest() {
        val lanes = CategoryLanes.from(
            derived = listOf("Primary", "Social", "Promotions", "Updates", "Forums"),
            observed = listOf("Hobby"),
        )

        assertEquals(listOf("primary", "social", "promotions", "updates", "forums"), lanes.pinned)
        assertEquals(listOf("hobby"), lanes.bundled)
    }

    @Test
    fun categoriesCarriedByMessagesAreDiscoveredEvenWithoutClassifierSettings() {
        val emails = listOf(email("e1", receivedAt = 1, keywords = setOf(Keywords.categoryKeyword("Updates"))))

        assertEquals(setOf("updates"), InboxAssembler.observedCategories(emails))
    }
}
