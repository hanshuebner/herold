package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.actions.CategoryActions
import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.CategoryDisposition
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
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

/** A category label as the server hands it over: a label with a disposition. */
private fun label(
    name: String,
    disposition: CategoryDisposition,
    priority: Int? = null,
    accountId: String = "acct-a",
) = Mailbox(
    accountId = accountId,
    id = "label-${name.lowercase()}",
    name = name,
    disposition = disposition,
    priority = priority,
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
    snoozedUntil: String? = null,
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
    snoozedUntil = snoozedUntil,
)

class InboxAssemblerTest {

    @Test
    fun theSnoozedRowsCarryTheirWakeTimeNextToWakeFirst() {
        val emails = listOf(
            email(
                "e1",
                receivedAt = 1000,
                threadId = "t1",
                keywords = setOf(Keywords.SNOOZED),
                snoozedUntil = "2026-09-14T06:00:00Z",
            ),
            email(
                "e2",
                receivedAt = 3000,
                threadId = "t2",
                keywords = setOf(Keywords.SNOOZED),
                snoozedUntil = "2026-09-12T06:00:00Z",
            ),
        )

        val rows = InboxAssembler.snoozedRows(emails, accounts, mailboxes)

        assertEquals(listOf("t2", "t1"), rows.map { it.threadId })
        assertEquals("2026-09-12T06:00:00Z", rows.first().wakeAt)
    }

    @Test
    fun aSnoozedMessageLeavesTheStreamUntilItWakes() {
        val emails = listOf(
            email("e1", receivedAt = 1000, threadId = "t1"),
            email("e2", receivedAt = 3000, threadId = "t2", keywords = setOf(Keywords.SNOOZED)),
        )

        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals(listOf("t1"), rows.map { it.threadId })
    }

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
        val lanes = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.PINNED, priority = 1),
            ),
        )
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
        val lanes = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.BUNDLED, priority = 1),
            ),
        )
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
    fun lanesComeFromTheServersDispositionInPriorityOrder() {
        val lanes = CategoryLanes.from(
            listOf(
                Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
                label("Promotions", CategoryDisposition.BUNDLED, priority = 3),
                label("Hobby", CategoryDisposition.PINNED, priority = 2),
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Newsletters", CategoryDisposition.FILED, priority = 4),
                label("Receipts", CategoryDisposition.WEEKLY, priority = 5),
                label("Projects", CategoryDisposition.NONE),
            ),
        )

        assertEquals(listOf("primary", "hobby"), lanes.pinned, "tabs are the pinned set in priority order")
        assertEquals(listOf("promotions"), lanes.bundled)
        assertEquals(listOf("newsletters", "receipts"), lanes.hidden)
        assertEquals(CategoryDisposition.NONE, lanes.dispositionOf("projects"))
        assertEquals(CategoryDisposition.NONE, lanes.dispositionOf("unknown"))
    }

    @Test
    fun atMostFiveCategoriesBecomeTabs() {
        val lanes = CategoryLanes.from(
            (0..5).map { label("Cat$it", CategoryDisposition.PINNED, priority = it) },
        )

        assertEquals(CategoryLanes.PINNED_LIMIT, lanes.pinned.size)
        assertEquals(listOf("cat0", "cat1", "cat2", "cat3", "cat4"), lanes.pinned)
    }

    @Test
    fun aMessageInSeveralCategoriesShowsOnceUnderItsHighestPriorityOne() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Hobby", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.BUNDLED, priority = 1),
            ),
        )
        val emails = listOf(
            email(
                "e1",
                receivedAt = 3000,
                keywords = setOf(
                    Keywords.categoryKeyword("Hobby"),
                    Keywords.categoryKeyword("Promotions"),
                ),
            ),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals(listOf("hobby", "promotions"), rows.single().categories)
        val stream = InboxAssembler.stream(rows, lanes)
        assertEquals(1, stream.size, "the message appears once")
        assertTrue(stream.single() is InboxItem.Conversation, "its lane is the pinned one, so it stays inline")
        assertEquals(
            listOf("t-e1"),
            InboxAssembler.stream(rows, lanes, selectedCategory = "hobby")
                .filterIsInstance<InboxItem.Conversation>().map { it.row.threadId },
        )
        assertEquals(
            emptyList(),
            InboxAssembler.stream(rows, lanes, selectedCategory = "promotions"),
            "the lower-priority category does not show it a second time",
        )
    }

    @Test
    fun aFiledOrDeferredCategoryNeverEntersTheInboxStream() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Newsletters", CategoryDisposition.FILED, priority = 0),
                label("Receipts", CategoryDisposition.DAILY, priority = 1),
                label("Promotions", CategoryDisposition.BUNDLED, priority = 2),
            ),
        )
        val emails = listOf(
            email("e1", receivedAt = 4000, keywords = setOf(Keywords.categoryKeyword("Newsletters"))),
            email("e2", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Receipts"))),
            email("e3", receivedAt = 2000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e4", receivedAt = 1000),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        val stream = InboxAssembler.stream(rows, lanes)

        assertEquals(2, stream.size, "only the bundle and the uncategorised message remain")
        assertEquals("promotions", (stream.first() as InboxItem.Bundle).row.category)
        assertEquals("t-e4", (stream[1] as InboxItem.Conversation).row.threadId)
    }

    @Test
    fun aCategoryWithNoDispositionGetsNoLane() {
        val lanes = CategoryLanes.from(listOf(label("Hobby", CategoryDisposition.NONE, priority = 0)))
        val emails = listOf(email("e1", receivedAt = 1000, keywords = setOf(Keywords.categoryKeyword("Hobby"))))
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertTrue(lanes.pinned.isEmpty() && lanes.bundled.isEmpty())
        assertEquals(1, InboxAssembler.stream(rows, lanes).size)
        assertTrue(InboxAssembler.stream(rows, lanes).single() is InboxItem.Conversation)
    }

    @Test
    fun anUnrankedCategorySortsBehindTheRankedOnes() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Zeta", CategoryDisposition.PINNED, priority = 1),
                label("Alpha", CategoryDisposition.PINNED, priority = null),
                label("Beta", CategoryDisposition.PINNED, priority = 0),
            ),
        )

        assertEquals(listOf("beta", "zeta", "alpha"), lanes.pinned)
        assertEquals("beta", lanes.resolve(listOf("alpha", "beta")))
        assertNull(lanes.resolve(emptyList()))
    }

    @Test
    fun aLabelOfAnotherAccountLeavesTheScopedLanesAlone() {
        val all = listOf(
            label("Hobby", CategoryDisposition.PINNED, priority = 0, accountId = "acct-a"),
            label("Promotions", CategoryDisposition.PINNED, priority = 0, accountId = "acct-b"),
        )

        assertEquals(listOf("hobby"), CategoryLanes.from(all, accountScope = "acct-a").pinned)
        assertEquals(listOf("hobby", "promotions"), CategoryLanes.from(all).pinned)
    }

    @Test
    fun categoriesCarriedByMessagesAreDiscoveredEvenWithoutClassifierSettings() {
        val emails = listOf(email("e1", receivedAt = 1, keywords = setOf(Keywords.categoryKeyword("Updates"))))

        assertEquals(setOf("updates"), InboxAssembler.observedCategories(emails))
    }

    @Test
    fun aConversationWithAnUnsentAnswerIsMarked() {
        val inbox = listOf(
            Email(
                accountId = "acct-a",
                id = "e1",
                threadId = "t-1",
                subject = "Hello",
                receivedAt = 1000,
                mailboxIds = setOf("inbox-1"),
            ),
        )
        val draft = inbox.single().copy(id = "d1", mailboxIds = setOf("drafts-1"), receivedAt = 2000)

        val plain = InboxAssembler.threadRows(inbox, accounts, mailboxes)
        assertTrue(!plain.single().hasDraft)

        val withDraft = InboxAssembler.threadRows(inbox, accounts, mailboxes, drafts = listOf(draft))
        assertTrue(withDraft.single().hasDraft)
        assertEquals("e1", withDraft.single().latestEmailId, "the draft does not become the row's message")
        assertEquals(listOf("e1"), withDraft.single().emailIds)

        val withQueued = InboxAssembler.threadRows(inbox, accounts, mailboxes, pendingThreads = setOf("t-1"))
        assertTrue(withQueued.single().hasDraft)
    }

    @Test
    fun derivedCategoriesAreTabsOnAnAccountWithNoLabels() {
        val derived = listOf("primary", "social", "promotions", "updates", "forums")

        val lanes = CategoryLanes.from(mailboxes, derived)

        assertEquals(derived, lanes.pinned, "the server's order is the tab order")
        assertEquals(emptyList(), lanes.bundled)
        assertEquals(emptyList(), lanes.hidden)
        assertEquals(CategoryDisposition.PINNED, lanes.dispositionOf("promotions"))
    }

    @Test
    fun derivedCategoriesKeepTheirTabsWhenTheStreamIsAssembled() {
        val lanes = CategoryLanes.from(mailboxes, listOf("primary", "promotions"))
        val emails = listOf(
            email("e1", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e2", receivedAt = 2000),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals(2, InboxAssembler.stream(rows, lanes).size, "a pinned category stays inline")
        assertEquals(
            listOf("t-e1"),
            InboxAssembler.stream(rows, lanes, selectedCategory = "promotions")
                .filterIsInstance<InboxItem.Conversation>().map { it.row.threadId },
        )
    }

    @Test
    fun aLabelledDispositionOverridesTheDerivedDefault() {
        val derived = listOf("primary", "social", "promotions")
        val labels = mailboxes + listOf(
            label("Promotions", CategoryDisposition.BUNDLED, priority = 0),
            label("Social", CategoryDisposition.NONE),
        )

        val lanes = CategoryLanes.from(labels, derived)

        assertEquals(listOf("primary"), lanes.pinned, "only the category with no label keeps the default")
        assertEquals(listOf("promotions"), lanes.bundled)
        assertEquals(CategoryDisposition.NONE, lanes.dispositionOf("social"))
    }

    @Test
    fun aBundledLabelCollapsesADerivedCategory() {
        val derived = listOf("primary", "promotions")
        val lanes = CategoryLanes.from(
            mailboxes + label("Promotions", CategoryDisposition.BUNDLED, priority = 0),
            derived,
        )
        val emails = listOf(
            email("e1", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e2", receivedAt = 2000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        val stream = InboxAssembler.stream(rows, lanes)

        assertEquals(1, stream.size)
        assertEquals("promotions", (stream.single() as InboxItem.Bundle).row.category)
    }

    @Test
    fun atMostFiveDerivedCategoriesBecomeTabs() {
        val lanes = CategoryLanes.from(
            mailboxes + label("Hobby", CategoryDisposition.PINNED, priority = 0),
            listOf("primary", "social", "promotions", "updates", "forums"),
        )

        assertEquals(CategoryLanes.PINNED_LIMIT, lanes.pinned.size)
        assertEquals(
            listOf("hobby", "primary", "social", "promotions", "updates"),
            lanes.pinned,
            "the labelled tab leads, the derived defaults follow in server order",
        )
    }

    @Test
    fun uncategorisedMailFallsToThePrimaryCategory() {
        val lanes = CategoryLanes.from(mailboxes, listOf("primary", "promotions"))
        val emails = listOf(email("e1", receivedAt = 1000))
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals("primary", lanes.resolve(emptyList()))
        assertEquals(
            listOf("t-e1"),
            InboxAssembler.stream(rows, lanes, selectedCategory = "primary")
                .filterIsInstance<InboxItem.Conversation>().map { it.row.threadId },
        )
    }

    @Test
    fun anAccountWithNeitherLabelsNorDerivedCategoriesHasNoLanes() {
        val lanes = CategoryLanes.from(
            listOf(Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX)),
            emptyList(),
        )

        assertTrue(lanes.pinned.isEmpty() && lanes.bundled.isEmpty() && lanes.order.isEmpty())
        assertNull(lanes.resolve(emptyList()))
    }

    @Test
    fun theSettingsListCarriesDerivedCategoriesWithTheirEffectiveLane() {
        val labels = mailboxes + label("Promotions", CategoryDisposition.BUNDLED, priority = 0)

        val entries = CategoryActions.entries(
            labels,
            listOf("primary", "promotions", "updates"),
            accountId = "acct-a",
        )

        val byName = entries.associateBy { it.category }
        assertEquals(CategoryDisposition.BUNDLED, byName.getValue("promotions").disposition)
        assertNotNull(byName.getValue("promotions").label)
        assertEquals(CategoryDisposition.PINNED, byName.getValue("primary").disposition)
        assertNull(byName.getValue("primary").label, "a derived category has no label yet")
        assertEquals(listOf("promotions", "projects", "primary", "updates"), entries.map { it.category })
    }

    @Test
    fun aCategoryTheMailCarriesEarnsATabWhenTheServerNamesNoDerivedSet() {
        val emails = listOf(
            email("e1", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e2", receivedAt = 2000, keywords = setOf(Keywords.categoryKeyword("Primary"))),
        )
        val observed = InboxAssembler.observedCategories(emails)

        val lanes = CategoryLanes.from(mailboxes, emptyList(), observed)

        assertEquals(listOf("primary", "promotions"), lanes.pinned)
    }

    @Test
    fun theClassifiersOrderLeadsTheCategoriesTheMailCarries() {
        val lanes = CategoryLanes.from(
            mailboxes,
            derivedCategories = listOf("primary", "social"),
            observedCategories = setOf("promotions", "social"),
        )

        assertEquals(listOf("primary", "social", "promotions"), lanes.pinned)
    }

    @Test
    fun theTabRowIsTheLanesAloneAndOpensOnPrimary() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Promotions", CategoryDisposition.PINNED, priority = 1),
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Receipts", CategoryDisposition.BUNDLED, priority = 2),
            ),
        )

        assertEquals(listOf("primary", "promotions"), lanes.tabs, "the row carries the lanes and nothing else")
        assertEquals("primary", lanes.select(null), "the inbox opens on the primary-role lane")
        assertEquals("primary", lanes.home)
    }

    @Test
    fun thePrimaryTabLeadsTheRowEvenWhenNoCategoryClaimsIt() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Hobby", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.PINNED, priority = 1),
            ),
        )

        assertEquals(listOf("primary", "hobby", "promotions"), lanes.tabs)
        assertEquals("primary", lanes.select(null))
        assertEquals("primary", lanes.tabOf("unlaned"), "mail with no lane of its own goes to Primary")
    }

    @Test
    fun thePrimaryTabIsThereWhenTheCategoriesOutnumberThePinnedBudget() {
        val lanes = CategoryLanes.from(
            mailboxes,
            derivedCategories = emptyList(),
            observedCategories = listOf(
                "alpha", "beta", "gamma", "delta", "epsilon", "primary", "promotions",
            ),
        )

        assertEquals(CategoryLanes.PINNED_LIMIT, lanes.pinned.size, "the pinned budget is the server's")
        assertEquals("primary", lanes.tabs.first(), "the lane uncategorised mail falls to keeps its tab")
        assertEquals("primary", lanes.select(null))
        assertEquals(
            "primary",
            lanes.tabOf("promotions"),
            "a category the budget left out shows under Primary rather than nowhere",
        )
    }

    @Test
    fun anAccountWithNoLanesGetsNoPrimaryTabEither() {
        val lanes = CategoryLanes.from(mailboxes, emptyList())

        assertTrue(lanes.tabs.isEmpty())
        assertNull(lanes.home)
    }

    @Test
    fun theLeadingTabIsHomeWhenTheAccountBundlesItsPrimaryCategory() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.BUNDLED, priority = 0),
                label("Hobby", CategoryDisposition.PINNED, priority = 1),
            ),
        )

        assertEquals(listOf("hobby"), lanes.tabs)
        assertEquals("hobby", lanes.select(null), "the tab the row highlights is the one the list shows")
        assertEquals("hobby", lanes.tabOf("primary"))
    }

    @Test
    fun aPickedLaneSurvivesAReorderAndAVanishedOneFallsBackToPrimary() {
        val before = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.PINNED, priority = 1),
            ),
        )
        assertEquals("promotions", before.select("promotions"))

        val reordered = CategoryLanes.from(
            listOf(
                label("Promotions", CategoryDisposition.PINNED, priority = 0),
                label("Primary", CategoryDisposition.PINNED, priority = 1),
                label("Hobby", CategoryDisposition.PINNED, priority = 2),
            ),
        )
        assertEquals("promotions", reordered.select("promotions"), "a reorder leaves the reader where they were")

        val without = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.BUNDLED, priority = 1),
            ),
        )
        assertEquals("primary", without.select("promotions"), "a lane that is gone returns the reader to Primary")
    }

    @Test
    fun anAccountWithNoLanesShowsOneUndividedList() {
        val lanes = CategoryLanes.from(mailboxes, emptyList())
        val emails = listOf(
            email("e1", receivedAt = 2000),
            email("e2", receivedAt = 1000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertTrue(lanes.tabs.isEmpty())
        assertNull(lanes.select(null))
        assertEquals(2, InboxAssembler.stream(rows, lanes, lanes.select(null)).size)
        assertEquals(emptyMap(), InboxAssembler.unreadByLane(rows, lanes))
    }

    @Test
    fun theHomeLaneCarriesTheBundlesAndWhateverNoTabClaims() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Hobby", CategoryDisposition.PINNED, priority = 1),
                label("Promotions", CategoryDisposition.BUNDLED, priority = 2),
                label("Projects", CategoryDisposition.NONE),
            ),
        )
        val emails = listOf(
            email("e1", receivedAt = 4000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e2", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Projects"))),
            email("e3", receivedAt = 2000, keywords = setOf(Keywords.categoryKeyword("Hobby"))),
            email("e4", receivedAt = 1000),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        val primary = InboxAssembler.stream(rows, lanes, "primary")
        assertEquals("promotions", (primary.first() as InboxItem.Bundle).row.category)
        assertEquals(
            listOf("t-e2", "t-e4"),
            primary.filterIsInstance<InboxItem.Conversation>().map { it.row.threadId },
            "a category with no lane of its own lands on the home lane",
        )
        assertEquals(
            listOf("t-e3"),
            InboxAssembler.stream(rows, lanes, "hobby")
                .filterIsInstance<InboxItem.Conversation>().map { it.row.threadId },
        )
    }

    @Test
    fun aLanesUnreadCountIsItsUnreadRows() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.PINNED, priority = 1),
                label("Newsletters", CategoryDisposition.FILED, priority = 2),
            ),
        )
        val emails = listOf(
            email("e1", receivedAt = 5000, keywords = emptySet()),
            email("e2", receivedAt = 4000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email(
                "e3",
                receivedAt = 3000,
                keywords = setOf(Keywords.categoryKeyword("Promotions"), Keywords.SEEN),
            ),
            email("e4", receivedAt = 2000, keywords = setOf(Keywords.categoryKeyword("Newsletters"))),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals(
            mapOf("primary" to 1, "promotions" to 1),
            InboxAssembler.unreadByLane(rows, lanes),
            "a read conversation and a filed one count nowhere",
        )
    }

    @Test
    fun aBundlesUnreadMembersCountTowardsTheLaneTheBundleRendersOn() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.BUNDLED, priority = 1),
            ),
        )
        val emails = listOf(
            email("e1", receivedAt = 4000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email("e2", receivedAt = 3000, keywords = setOf(Keywords.categoryKeyword("Promotions"))),
            email(
                "e3",
                receivedAt = 2000,
                keywords = setOf(Keywords.categoryKeyword("Promotions"), Keywords.SEEN),
            ),
            email("e4", receivedAt = 1000, keywords = emptySet()),
        )
        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)

        assertEquals(
            mapOf("primary" to 3),
            InboxAssembler.unreadByLane(rows, lanes),
            "the bundle's two unread conversations count with the home lane's own one",
        )
        val bundle = InboxAssembler.stream(rows, lanes, "primary").filterIsInstance<InboxItem.Bundle>().single()
        assertEquals(2, bundle.row.unreadCount)
    }

    @Test
    fun readingTheLastUnreadConversationEmptiesTheLanesCount() {
        val lanes = CategoryLanes.from(
            listOf(
                label("Primary", CategoryDisposition.PINNED, priority = 0),
                label("Promotions", CategoryDisposition.PINNED, priority = 1),
            ),
        )
        val unread = email("e1", receivedAt = 1000, keywords = setOf(Keywords.categoryKeyword("Promotions")))
        val rows = InboxAssembler.threadRows(listOf(unread), accounts, mailboxes)
        assertEquals(mapOf("promotions" to 1), InboxAssembler.unreadByLane(rows, lanes))

        val read = InboxAssembler.threadRows(
            listOf(unread.copy(keywords = unread.keywords + Keywords.SEEN)),
            accounts,
            mailboxes,
        )
        assertEquals(emptyMap(), InboxAssembler.unreadByLane(read, lanes))
    }
}
