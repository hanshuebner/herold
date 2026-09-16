package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.CategoryDisposition
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.MailboxSetOutcome
import com.netzhansa.herold.shared.jmap.SetErrors
import com.netzhansa.herold.shared.jmap.WireMailbox
import com.netzhansa.herold.shared.outbox.InMemoryBlobSpool
import com.netzhansa.herold.shared.outbox.MailboxPayload
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.outbox.outboxJson
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * Category settings: a disposition change and a drag of the pinned order
 * queue the `Mailbox/set` the server expects, and the drain takes the
 * server's ranked set back into the store - including the refusal of a
 * sixth pinned category (issue #399, suite REQ-CAT-04/05/11).
 */
class CategoryActionsTest {

    private class Harness {
        val store = FakeLocalStore()
        val api = FakeJmapApi()
        val outbox = Outbox(store)
        val categories = CategoryActions(store, outbox)
        val drainer = OutboxDrainer(
            api = api,
            store = store,
            outbox = outbox,
            spool = InMemoryBlobSpool(),
        )
    }

    private fun label(
        name: String,
        disposition: CategoryDisposition = CategoryDisposition.NONE,
        priority: Int? = null,
    ) = Mailbox(
        accountId = "acct-a",
        id = "mb-${name.lowercase()}",
        name = name,
        disposition = disposition,
        priority = priority,
    )

    private suspend fun payload(outbox: Outbox, id: Long): MailboxPayload {
        val entry = outbox.entry(id) ?: error("entry $id is not queued")
        assertEquals(OutboxKind.MAILBOX, entry.kind)
        return outboxJson.decodeFromString(MailboxPayload.serializer(), entry.payload)
    }

    @Test
    fun pinningALabelWritesTheRowAtOnceAndQueuesTheMailboxSet() = runTest {
        val h = Harness()
        val hobby = label("Hobby")
        h.store.upsertMailboxes(listOf(hobby, label("Promotions", CategoryDisposition.PINNED, priority = 0)))

        val id = h.categories.setDisposition(hobby, CategoryDisposition.PINNED, h.store.mailboxList())

        val row = h.store.mailboxList().first { it.id == hobby.id }
        assertEquals(CategoryDisposition.PINNED, row.disposition, "the settings screen shows it at once")
        assertEquals(1, row.priority, "a newly pinned label ranks behind the ranked ones")
        val body = payload(h.outbox, id)
        val patch = body.updates.getValue(hobby.id)
        assertEquals("pinned", patch["disposition"]?.jsonPrimitive?.content)
        assertEquals(1, patch["priority"]?.jsonPrimitive?.content?.toInt())
    }

    @Test
    fun aDispositionThatNeedsNoRankCarriesOnlyTheDisposition() = runTest {
        val h = Harness()
        val hobby = label("Hobby", CategoryDisposition.PINNED, priority = 2)
        h.store.upsertMailboxes(listOf(hobby))

        val id = h.categories.setDisposition(hobby, CategoryDisposition.FILED, h.store.mailboxList())

        val patch = payload(h.outbox, id).updates.getValue(hobby.id)
        assertEquals("filed", patch["disposition"]?.jsonPrimitive?.content)
        assertNull(patch["priority"], "the rank is untouched, so it is not in the patch")
        assertEquals(2, h.store.mailboxList().single().priority)
    }

    @Test
    fun aDragOfThePinnedOrderRenumbersDenselyAndMovesTheChangedLabelsInOneCall() = runTest {
        val h = Harness()
        val pinned = listOf(
            label("Primary", CategoryDisposition.PINNED, priority = 0),
            label("Hobby", CategoryDisposition.PINNED, priority = 1),
            label("Promotions", CategoryDisposition.PINNED, priority = 2),
        )
        h.store.upsertMailboxes(pinned)

        val id = h.categories.reorderPinned(pinned, from = 2, to = 0)
            ?: error("the move changed nothing")

        val order = CategoryActions.pinnedLabels(h.store.mailboxList(), "acct-a").map { it.name }
        assertEquals(listOf("Promotions", "Primary", "Hobby"), order)
        val updates = payload(h.outbox, id).updates
        assertEquals(setOf("mb-promotions", "mb-primary", "mb-hobby"), updates.keys)
        assertEquals(0, updates.getValue("mb-promotions")["priority"]?.jsonPrimitive?.content?.toInt())
        assertEquals(1, updates.getValue("mb-primary")["priority"]?.jsonPrimitive?.content?.toInt())
    }

    @Test
    fun aDragThatChangesNothingQueuesNothing() = runTest {
        val h = Harness()
        val pinned = listOf(
            label("Primary", CategoryDisposition.PINNED, priority = 0),
            label("Hobby", CategoryDisposition.PINNED, priority = 1),
        )
        h.store.upsertMailboxes(pinned)

        assertNull(h.categories.reorderPinned(pinned, from = 1, to = 1))
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun theDrainTakesTheServersRankedSetBack() = runTest {
        val h = Harness()
        val hobby = label("Hobby")
        h.store.upsertMailboxes(listOf(hobby))
        // The server renumbers the ranked labels around the one that
        // moved, so its answer - not the optimistic row - is what the
        // store ends up holding.
        h.api.mailboxes = listOf(
            WireMailbox(id = "mb-hobby", name = "Hobby", disposition = "pinned", priority = 0),
            WireMailbox(id = "mb-promotions", name = "Promotions", disposition = "bundled", priority = 1),
        )
        h.api.mailboxSetOutcome = MailboxSetOutcome(updated = setOf("mb-hobby"))

        h.categories.setDisposition(hobby, CategoryDisposition.PINNED, h.store.mailboxList())
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.submitted)
        val submitted = h.api.mailboxSetCalls.single().getValue("mb-hobby")
        assertEquals("pinned", submitted["disposition"]?.jsonPrimitive?.content)
        val rows = h.store.mailboxList().associateBy { it.id }
        assertEquals(CategoryDisposition.PINNED, rows.getValue("mb-hobby").disposition)
        assertEquals(CategoryDisposition.BUNDLED, rows.getValue("mb-promotions").disposition)
        assertEquals(1, rows.getValue("mb-promotions").priority)
        assertTrue(h.outbox.list().isEmpty(), "a drained entry leaves the queue")
    }

    @Test
    fun aSixthPinnedCategoryIsRefusedInTheUsersTerms() = runTest {
        val h = Harness()
        val hobby = label("Hobby")
        h.store.upsertMailboxes(listOf(hobby))
        h.api.mailboxes = listOf(WireMailbox(id = "mb-hobby", name = "Hobby", disposition = "none"))
        h.api.mailboxSetOutcome = MailboxSetOutcome(
            errorTypes = mapOf("mb-hobby" to SetErrors.TOO_MANY_PINNED),
            errorMessages = mapOf("mb-hobby" to "at most 5 mailboxes may be pinned"),
        )

        h.categories.setDisposition(hobby, CategoryDisposition.PINNED, h.store.mailboxList())
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.rejected)
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, entry.state)
        assertEquals(CategoryActions.TOO_MANY_PINNED, entry.lastError)
        assertEquals(
            CategoryDisposition.NONE,
            h.store.mailboxList().single().disposition,
            "the refusal puts the store back on the server's truth",
        )
    }

    @Test
    fun aDerivedCategoryWithNoLabelGetsOneWithTheChosenDisposition() = runTest {
        val h = Harness()
        h.store.upsertMailboxes(listOf(label("Hobby", CategoryDisposition.PINNED, priority = 0)))

        val id = h.categories.createWithDisposition(
            accountId = "acct-a",
            category = "promotions",
            disposition = CategoryDisposition.BUNDLED,
            all = h.store.mailboxList(),
        )

        val placeholder = CategoryActions.placeholderId("acct-a", "promotions")
        val row = h.store.mailboxList().first { it.id == placeholder }
        assertEquals("promotions", row.name, "the label is named after the category keyword")
        assertEquals(CategoryDisposition.BUNDLED, row.disposition)
        val created = payload(h.outbox, id).creates.getValue(placeholder)
        assertEquals("promotions", created["name"]?.jsonPrimitive?.content)
        assertEquals("bundled", created["disposition"]?.jsonPrimitive?.content)
        assertNull(created["priority"], "only a pinned category takes a rank")
    }

    @Test
    fun theDrainSwapsThePlaceholderForTheServersLabel() = runTest {
        val h = Harness()
        h.api.mailboxes = listOf(
            WireMailbox(id = "mb-promotions", name = "promotions", disposition = "pinned", priority = 0),
        )
        h.api.mailboxSetOutcome = MailboxSetOutcome(
            created = mapOf(
                CategoryActions.placeholderId("acct-a", "promotions") to
                    WireMailbox(id = "mb-promotions", name = "promotions", disposition = "pinned", priority = 0),
            ),
        )

        h.categories.createWithDisposition(
            accountId = "acct-a",
            category = "promotions",
            disposition = CategoryDisposition.PINNED,
            all = emptyList(),
        )
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.submitted)
        val created = h.api.mailboxCreateCalls.single().values.single()
        assertEquals("promotions", created["name"]?.jsonPrimitive?.content)
        assertEquals("pinned", created["disposition"]?.jsonPrimitive?.content)
        assertEquals(
            listOf("mb-promotions"),
            h.store.mailboxList().map { it.id },
            "the placeholder goes when the server's own row arrives",
        )
    }

    @Test
    fun theSettingsListOffersTheAccountsLabelsInPriorityOrder() {
        val all = listOf(
            Mailbox(accountId = "acct-a", id = "inbox", name = "Inbox", role = MailboxRoles.INBOX),
            label("Zeta", CategoryDisposition.NONE),
            label("Hobby", CategoryDisposition.PINNED, priority = 1),
            label("Primary", CategoryDisposition.PINNED, priority = 0),
            Mailbox(accountId = "acct-b", id = "mb-other", name = "Other"),
        )

        assertEquals(
            listOf("Primary", "Hobby", "Zeta"),
            CategoryActions.categoryLabels(all, "acct-a").map { it.name },
        )
        assertEquals(
            listOf("Primary", "Hobby"),
            CategoryActions.pinnedLabels(all, "acct-a").map { it.name },
        )
    }
}
