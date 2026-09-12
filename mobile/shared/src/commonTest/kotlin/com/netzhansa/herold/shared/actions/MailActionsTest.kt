package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
    Mailbox(accountId = "acct-a", id = "label-1", name = "Projects"),
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

class MailActionsTest {

    private suspend fun store(email: Email = seeded()): FakeLocalStore =
        FakeLocalStore().apply {
            upsertMailboxes(mailboxes)
            upsertEmails(listOf(email))
        }

    @Test
    fun starWritesTheLocalStoreFirstAndSendsTheKeywordPatch() = runTest {
        val api = FakeJmapApi()
        val store = store()
        val actions = MailActions(api, store)

        val result = actions.setFlagged(listOf(seeded()), true)

        assertEquals(ActionResult.Applied, result)
        assertTrue(store.email("acct-a", "e1")!!.isFlagged)
        assertEquals(JsonPrimitive(true), api.emailSetCalls.single()["e1"]!!["keywords/\$flagged"])
    }

    @Test
    fun archiveMovesOutOfInboxIntoArchiveAndUndoPutsItBack() = runTest {
        val api = FakeJmapApi()
        val store = store()
        val actions = MailActions(api, store)
        val before = store.email("acct-a", "e1")!!

        val (result, snapshot) = actions.archive(listOf(before), mailboxes)

        assertEquals(ActionResult.Applied, result)
        assertEquals(setOf("archive-1"), store.email("acct-a", "e1")!!.mailboxIds)
        val patch = api.emailSetCalls.single().getValue("e1")
        assertEquals(JsonNull, patch["mailboxIds/inbox-1"])
        assertEquals(JsonPrimitive(true), patch["mailboxIds/archive-1"])

        assertEquals(ActionResult.Applied, actions.restore(snapshot))
        assertEquals(setOf("inbox-1"), store.email("acct-a", "e1")!!.mailboxIds)
        val undoPatch = api.emailSetCalls.last().getValue("e1")
        assertEquals(JsonPrimitive(true), undoPatch["mailboxIds/inbox-1"])
        assertEquals(JsonNull, undoPatch["mailboxIds/archive-1"])
    }

    @Test
    fun archiveLocallyMovesTheRowsBeforeAnythingIsSent() = runTest {
        val api = FakeJmapApi()
        val store = store()
        val actions = MailActions(api, store)

        val pending = actions.archiveLocally(listOf(store.email("acct-a", "e1")!!), mailboxes)

        // The undo affordance rides on this: the row is out of the inbox
        // with no request made yet (issue #338).
        assertEquals(setOf("archive-1"), store.email("acct-a", "e1")!!.mailboxIds)
        assertTrue(api.emailSetCalls.isEmpty(), "archiveLocally must not send anything")

        assertEquals(ActionResult.Applied, actions.commit(pending))
        assertEquals(JsonNull, api.emailSetCalls.single().getValue("e1")["mailboxIds/inbox-1"])

        assertEquals(ActionResult.Applied, actions.restore(pending.snapshot))
        assertEquals(setOf("inbox-1"), store.email("acct-a", "e1")!!.mailboxIds)
    }

    @Test
    fun aRejectedCommitPutsTheRowsBackAfterTheUndoWasAlreadyOffered() = runTest {
        val api = FakeJmapApi().apply { setFailure = kotlinx.io.IOException("network unreachable") }
        val store = store()
        val actions = MailActions(api, store)

        val pending = actions.archiveLocally(listOf(store.email("acct-a", "e1")!!), mailboxes)
        assertEquals(setOf("archive-1"), store.email("acct-a", "e1")!!.mailboxIds)

        val result = actions.commit(pending)

        assertTrue(result is ActionResult.Reverted && result.offline)
        assertEquals(setOf("inbox-1"), store.email("acct-a", "e1")!!.mailboxIds)
    }

    @Test
    fun archivingAnAlreadyArchivedRowHasNothingToCommit() = runTest {
        val api = FakeJmapApi()
        val store = store(seeded().copy(mailboxIds = setOf("archive-1")))
        val actions = MailActions(api, store)

        val pending = actions.archiveLocally(listOf(store.email("acct-a", "e1")!!), mailboxes)

        assertTrue(pending.isEmpty)
        assertEquals(ActionResult.Applied, actions.commit(pending))
        assertTrue(api.emailSetCalls.isEmpty())
    }

    @Test
    fun anUnreachableServerRevertsTheOptimisticStateAndReportsOffline() = runTest {
        val api = FakeJmapApi().apply { setFailure = kotlinx.io.IOException("network unreachable") }
        val store = store()
        val actions = MailActions(api, store)

        val (result, _) = actions.archive(listOf(store.email("acct-a", "e1")!!), mailboxes)

        assertTrue(result is ActionResult.Reverted)
        assertTrue((result as ActionResult.Reverted).offline)
        assertEquals(setOf("inbox-1"), store.email("acct-a", "e1")!!.mailboxIds, "the row is put back")
    }

    @Test
    fun aServerRejectionRevertsAndSurfacesTheServersReason() = runTest {
        val api = FakeJmapApi().apply { setRejections = mapOf("e1" to "mailbox is read-only") }
        val store = store()
        val actions = MailActions(api, store)

        val result = actions.setSeen(listOf(store.email("acct-a", "e1")!!), true)

        assertTrue(result is ActionResult.Reverted)
        assertEquals("mailbox is read-only", (result as ActionResult.Reverted).message)
        assertTrue(store.email("acct-a", "e1")!!.isUnread)
    }

    @Test
    fun labellingTogglesTheCustomMailboxMembership() = runTest {
        val api = FakeJmapApi()
        val store = store()
        val actions = MailActions(api, store)
        val label = mailboxes.first { it.id == "label-1" }

        actions.setLabel(listOf(store.email("acct-a", "e1")!!), label, applied = true)
        assertEquals(setOf("inbox-1", "label-1"), store.email("acct-a", "e1")!!.mailboxIds)

        actions.setLabel(listOf(store.email("acct-a", "e1")!!), label, applied = false)
        assertEquals(setOf("inbox-1"), store.email("acct-a", "e1")!!.mailboxIds)
    }

    @Test
    fun snoozeSendsSnoozedUntilAndMarksTheMessageSnoozedLocally() = runTest {
        val api = FakeJmapApi()
        val store = store()
        val actions = MailActions(api, store)

        actions.snooze(listOf(store.email("acct-a", "e1")!!), "2026-09-12T06:00:00Z")

        val stored = store.email("acct-a", "e1")!!
        assertEquals("2026-09-12T06:00:00Z", stored.snoozedUntil)
        assertTrue(stored.isSnoozed)
        assertEquals(
            JsonPrimitive("2026-09-12T06:00:00Z"),
            api.emailSetCalls.single().getValue("e1")["snoozedUntil"],
        )
    }

    @Test
    fun recategorisingReplacesTheCategoryKeyword() = runTest {
        val api = FakeJmapApi()
        val seed = seeded().copy(keywords = setOf(Keywords.categoryKeyword("Primary")))
        val store = store(seed)
        val actions = MailActions(api, store)

        actions.setCategory(listOf(seed), "Promotions")

        assertEquals("promotions", store.email("acct-a", "e1")!!.category)
        val patch = api.emailSetCalls.single().getValue("e1")
        assertEquals(JsonNull, patch["keywords/\$category-primary"])
        assertEquals(JsonPrimitive(true), patch["keywords/\$category-promotions"])
    }
}
