package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class DrawerModelTest {

    private fun box(
        account: String,
        id: String,
        name: String,
        role: String? = null,
        parentId: String? = null,
        unread: Int = 0,
    ) = Mailbox(
        accountId = account,
        id = id,
        name = name,
        role = role,
        parentId = parentId,
        unreadEmails = unread,
    )

    private val mailboxes = listOf(
        box("a", "1", "Inbox", MailboxRoles.INBOX, unread = 3),
        box("a", "2", "Sent", MailboxRoles.SENT),
        box("a", "3", "Drafts", MailboxRoles.DRAFTS, unread = 1),
        box("a", "4", "Archive", MailboxRoles.ARCHIVE),
        box("a", "5", "Junk", MailboxRoles.JUNK),
        box("a", "6", "Trash", MailboxRoles.TRASH),
        box("a", "10", "Work", unread = 2),
        box("a", "11", "Invoices", parentId = "10"),
        box("a", "12", "Acme"),
        box("b", "20", "Inbox", MailboxRoles.INBOX, unread = 4),
        box("b", "21", "Acme", unread = 5),
    )

    @Test
    fun theSystemFoldersFollowTheSuitesSidebarOrder() {
        assertEquals(
            listOf("Sent", "Drafts", "All Mail", "Spam", "Trash"),
            DrawerModel.folders(mailboxes).map { it.title },
        )
    }

    @Test
    fun aFolderCarriesTheUnreadCountOfTheAccountsInScope() {
        assertEquals(7, DrawerModel.inboxUnread(mailboxes))
        assertEquals(3, DrawerModel.inboxUnread(mailboxes, accountScope = "a"))
        assertEquals(1, DrawerModel.folders(mailboxes).first { it.role == MailboxRoles.DRAFTS }.unread)
    }

    @Test
    fun theLabelTreeNestsChildrenUnderTheirParent() {
        val labels = DrawerModel.labels(mailboxes)
        assertEquals(listOf("Acme", "Work", "Work/Invoices"), labels.map { it.title })
        assertEquals(listOf(0, 0, 1), labels.map { it.depth })
        assertEquals("Invoices", labels.last().name)
    }

    @Test
    fun aLabelOfTheSameNameInTwoAccountsIsOneRow() {
        val acme = DrawerModel.labels(mailboxes).first { it.title == "Acme" }
        assertEquals(setOf("a" to "12", "b" to "21"), acme.mailboxes.map { it.accountId to it.id }.toSet())
        assertEquals(5, acme.unread)

        val scoped = DrawerModel.labels(mailboxes, accountScope = "a").first { it.title == "Acme" }
        assertEquals(listOf("12"), scoped.ids)
    }

    @Test
    fun aDestinationResolvesBackToItsMailboxes() {
        val archive = DrawerModel.folders(mailboxes).first { it.role == MailboxRoles.ARCHIVE }
        assertEquals(
            archive.mailboxes,
            DrawerModel.mailboxesFor(archive.destination, mailboxes),
        )
        val nested = DrawerModel.labels(mailboxes).first { it.title == "Work/Invoices" }
        assertEquals(
            listOf("11"),
            DrawerModel.mailboxesFor(nested.destination, mailboxes).map { it.id },
        )
    }

    @Test
    fun aDestinationSurvivesItsKey() {
        listOf(
            MailDestination.Inbox,
            MailDestination.Snoozed,
            DrawerModel.folders(mailboxes).first { it.role == MailboxRoles.JUNK }.destination,
            DrawerModel.labels(mailboxes).first { it.title == "Work/Invoices" }.destination,
        ).forEach { destination ->
            assertEquals(destination, DrawerModel.destination(DrawerModel.key(destination)))
        }
        assertTrue(DrawerModel.destination("nonsense") == MailDestination.Inbox)
    }
}
