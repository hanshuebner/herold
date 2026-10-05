package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.SpamFeedbackApi
import com.netzhansa.herold.shared.jmap.SpamFeedbackKind
import com.netzhansa.herold.shared.jmap.spamFeedbackBody
import com.netzhansa.herold.shared.outbox.ActionPayload
import com.netzhansa.herold.shared.outbox.InMemoryBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.SpamFeedbackPayload
import com.netzhansa.herold.shared.outbox.outboxJson
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

private val mailboxes = listOf(
    Mailbox(accountId = "acct-a", id = "inbox-1", name = "Inbox", role = MailboxRoles.INBOX),
    Mailbox(accountId = "acct-a", id = "junk-1", name = "Spam", role = MailboxRoles.JUNK),
    Mailbox(accountId = "acct-a", id = "archive-1", name = "Archive", role = MailboxRoles.ARCHIVE),
)

private fun junked() = Email(
    accountId = "acct-a",
    id = "e1",
    threadId = "t1",
    subject = "Your meter reading",
    fromEmail = "no-reply@utility.example",
    receivedAt = 1000,
    keywords = setOf(Keywords.JUNK),
    mailboxIds = setOf("junk-1"),
)

private fun inboxed() = Email(
    accountId = "acct-a",
    id = "e2",
    threadId = "t2",
    subject = "Win a prize",
    fromEmail = "sender@spam.example",
    receivedAt = 2000,
    mailboxIds = setOf("inbox-1"),
)

/** The spam-feedback surface as the drain calls it. */
private class FakeSpamFeedbackApi : SpamFeedbackApi {
    val posted = mutableListOf<Pair<String, SpamFeedbackKind>>()
    var failure: Throwable? = null

    override suspend fun postSpamFeedback(emailId: String, kind: SpamFeedbackKind) {
        failure?.let { throw it }
        posted += emailId to kind
    }
}

/**
 * Correcting a spam verdict from the phone (issue #506, suite
 * REQ-FILT-02a / REQ-MAIL-135): the move is an optimistic action behind
 * the outbox like archive and delete, the REQ-FILT-70 feedback record
 * is its own durable entry, and the never-spam rule the sheet offers is
 * built from the Suite's own mapping.
 */
class SpamActionsTest {

    private class Harness(val store: FakeLocalStore, val api: FakeJmapApi) {
        val outbox = Outbox(store)
        val feedback = FakeSpamFeedbackApi()
        val drainer = OutboxDrainer(api, store, outbox, InMemoryBlobSpool(), spamFeedback = feedback)
        val actions = MailActions(store, outbox)
    }

    private suspend fun harness(vararg emails: Email): Harness {
        val store = FakeLocalStore().apply {
            upsertMailboxes(mailboxes)
            upsertEmails(emails.toList())
        }
        return Harness(store, FakeJmapApi())
    }

    @Test
    fun theFeedbackBodyCarriesTheMessageIdAndTheCorrection() {
        val body = spamFeedbackBody("4176", SpamFeedbackKind.HAM)

        assertEquals(setOf("emailId", "kind"), body.keys)
        assertEquals("4176", body.getValue("emailId").jsonPrimitive.content)
        assertEquals("ham", body.getValue("kind").jsonPrimitive.content)
        assertEquals("spam", spamFeedbackBody("1", SpamFeedbackKind.SPAM).getValue("kind").jsonPrimitive.content)
        assertEquals(
            "phishing",
            spamFeedbackBody("1", SpamFeedbackKind.PHISHING).getValue("kind").jsonPrimitive.content,
        )
    }

    @Test
    fun notSpamMovesOutOfJunkIntoTheInboxAndDropsTheJunkKeyword() = runTest {
        val h = harness(junked())

        val pending = h.actions.notSpamLocally(listOf(junked()), mailboxes)
        h.actions.commit(pending)

        val local = h.store.email("acct-a", "e1")!!
        assertEquals(setOf("inbox-1"), local.mailboxIds)
        assertTrue(local.keywords.none { it.equals(Keywords.JUNK, ignoreCase = true) })
        assertTrue(h.api.emailSetCalls.isEmpty(), "the action sends nothing itself")

        val entry = h.outbox.list().single()
        assertEquals(OutboxKind.ACTION, entry.kind)
        val patch = outboxJson.decodeFromString<ActionPayload>(entry.payload).patches.getValue("e1")
        assertEquals(JsonNull, patch["mailboxIds/junk-1"])
        assertEquals(JsonPrimitive(true), patch["mailboxIds/inbox-1"])
        assertEquals(JsonNull, patch["keywords/${Keywords.JUNK}"])
    }

    @Test
    fun notSpamLeavesAMessageThatIsNotInJunkAlone() = runTest {
        val h = harness(inboxed())

        val pending = h.actions.notSpamLocally(listOf(inboxed()), mailboxes)

        assertTrue(pending.isEmpty, "there is no correction to make outside Junk")
        assertEquals(setOf("inbox-1"), h.store.email("acct-a", "e2")!!.mailboxIds)
    }

    @Test
    fun reportSpamMovesIntoJunkAndPhishingAddsItsOwnKeyword() = runTest {
        val h = harness(inboxed())

        h.actions.commit(h.actions.reportSpamLocally(listOf(inboxed()), mailboxes, phishing = true))

        val local = h.store.email("acct-a", "e2")!!
        assertEquals(setOf("junk-1"), local.mailboxIds)
        assertTrue(local.keywords.contains(Keywords.JUNK))
        assertTrue(local.keywords.contains(Keywords.PHISHING))

        val patch = outboxJson.decodeFromString<ActionPayload>(h.outbox.list().single().payload)
            .patches.getValue("e2")
        assertEquals(JsonNull, patch["mailboxIds/inbox-1"])
        assertEquals(JsonPrimitive(true), patch["mailboxIds/junk-1"])
        assertEquals(JsonPrimitive(true), patch["keywords/${Keywords.PHISHING}"])
    }

    @Test
    fun theFeedbackIsItsOwnDurableEntryAndTheDrainPostsIt() = runTest {
        val h = harness(junked())

        h.actions.commit(h.actions.notSpamLocally(listOf(junked()), mailboxes))
        h.actions.reportSpamFeedback(listOf(junked()), SpamFeedbackKind.HAM)

        val entry = h.outbox.list().last()
        assertEquals(OutboxKind.SPAM_FEEDBACK, entry.kind)
        assertEquals(listOf("e1"), entry.entityIds)
        val payload = outboxJson.decodeFromString<SpamFeedbackPayload>(entry.payload)
        assertEquals(SpamFeedbackPayload("acct-a", "e1", "ham"), payload)
        assertTrue(h.feedback.posted.isEmpty(), "the record goes out on a drain, not on the action")

        h.drainer.drain()

        assertEquals(listOf("e1" to SpamFeedbackKind.HAM), h.feedback.posted)
        assertTrue(h.outbox.list().isEmpty(), "both entries drained")
    }

    @Test
    fun aFeedbackRecordTheServerCannotTakeKeepsItsPlaceForAnotherAttempt() = runTest {
        val h = harness(junked())
        h.feedback.failure = Exception("Unable to resolve host \"mail.example.local\"")

        h.actions.reportSpamFeedback(listOf(junked()), SpamFeedbackKind.HAM)
        h.drainer.drain()

        val entry = h.outbox.list().single()
        assertTrue(entry.isPending, "a record raised offline waits for a connection")

        h.feedback.failure = null
        h.drainer.drain()
        assertEquals(listOf("e1" to SpamFeedbackKind.HAM), h.feedback.posted)
    }

    @Test
    fun theNeverSpamRuleIsScopedToTheAddressOrItsDomain() {
        val address = NeverSpam.plan(emptyList(), NeverSpamScope.ADDRESS, "No-Reply@Utility.Example")
        assertTrue(address is NeverSpamPlan.Create)
        assertEquals(
            listOf(RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "No-Reply@Utility.Example")),
            address.conditions,
        )
        assertEquals(listOf(RuleAction(RuleActions.NEVER_SPAM)), address.actions)

        val domain = NeverSpam.plan(emptyList(), NeverSpamScope.DOMAIN, "no-reply@Utility.Example")
        assertTrue(domain is NeverSpamPlan.Create)
        assertEquals(
            listOf(RuleCondition(RuleFields.FROM_DOMAIN, RuleOps.EQUALS, "utility.example")),
            domain.conditions,
        )

        assertNull(
            NeverSpam.plan(emptyList(), NeverSpamScope.DOMAIN, "postmaster"),
            "an address with no domain scopes no rule",
        )
    }

    @Test
    fun aSenderThatAlreadyHasARuleIsNotGivenASecondOne() {
        val existing = ManagedRule(
            accountId = "acct-a",
            id = "r1",
            name = "Never spam: utility.example",
            order = 3,
            conditions = listOf(RuleCondition(RuleFields.FROM_DOMAIN, RuleOps.EQUALS, "utility.example")),
            actions = listOf(RuleAction(RuleActions.NEVER_SPAM)),
        )

        assertEquals(
            NeverSpamPlan.Reuse(existing),
            NeverSpam.plan(listOf(existing), NeverSpamScope.DOMAIN, "no-reply@UTILITY.example"),
        )

        val labelling = existing.copy(actions = listOf(RuleAction(RuleActions.APPLY_LABEL, mapOf("label" to "Bills"))))
        val plan = NeverSpam.plan(listOf(labelling), NeverSpamScope.DOMAIN, "no-reply@utility.example")
        assertTrue(plan is NeverSpamPlan.AddAction)
        assertEquals(
            listOf(RuleAction(RuleActions.APPLY_LABEL, mapOf("label" to "Bills")), RuleAction(RuleActions.NEVER_SPAM)),
            plan.nextActions,
        )
        assertEquals(4, NeverSpam.nextOrder(listOf(existing), "acct-a"))
    }

    @Test
    fun theNeverSpamRuleGoesOutThroughTheOutbox() = runTest {
        val h = harness(junked())
        val filters = FilterActions(h.store, h.outbox)
        val plan = NeverSpam.plan(emptyList(), NeverSpamScope.ADDRESS, "no-reply@utility.example")!!

        filters.applyNeverSpam("acct-a", plan, order = 0)

        val entry = h.outbox.list().single()
        assertEquals(OutboxKind.RULE, entry.kind)

        h.drainer.drain()

        val created = h.api.ruleSetCalls.single().first.values.single()
        assertEquals(
            JsonPrimitive("no-reply@utility.example"),
            created.getValue("conditions").jsonArray.single().jsonObject.getValue("value"),
        )
        assertEquals(
            JsonPrimitive(RuleActions.NEVER_SPAM),
            created.getValue("actions").jsonArray.single().jsonObject.getValue("kind"),
        )
    }
}
