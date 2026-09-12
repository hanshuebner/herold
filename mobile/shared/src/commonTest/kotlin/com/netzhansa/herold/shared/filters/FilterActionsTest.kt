package com.netzhansa.herold.shared.filters

import com.netzhansa.herold.shared.actions.FilterActions
import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.RuleOp
import com.netzhansa.herold.shared.outbox.RulePayload
import com.netzhansa.herold.shared.outbox.outboxJson
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The filter-write layer: every rule change lands in the durable outbox
 * with the wire body the drain submits, and the ones whose result the
 * client already knows change the store first (suite REQ-FLT-20,
 * REQ-AND-SYNC-20).
 */
class FilterActionsTest {

    private fun harness(): Triple<FakeLocalStore, Outbox, FilterActions> {
        val store = FakeLocalStore()
        val outbox = Outbox(store)
        return Triple(store, outbox, FilterActions(store, outbox))
    }

    private fun rule(
        id: String,
        order: Int = 0,
        enabled: Boolean = true,
        name: String = "Rule $id",
    ) = ManagedRule(
        accountId = "acct-a",
        id = id,
        name = name,
        enabled = enabled,
        order = order,
        conditions = listOf(RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "bob@example.local")),
        actions = listOf(RuleAction(RuleActions.SKIP_INBOX)),
    )

    private suspend fun payload(outbox: Outbox, id: Long): RulePayload {
        val entry = outbox.entry(id) ?: error("entry $id is not queued")
        assertEquals(OutboxKind.RULE, entry.kind)
        return outboxJson.decodeFromString(RulePayload.serializer(), entry.payload)
    }

    @Test
    fun createQueuesTheWireRuleTheServerExpects() = runTest {
        val (_, outbox, filters) = harness()
        val id = filters.create(
            accountId = "acct-a",
            name = "Acme",
            conditions = listOf(RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "bob@example.local")),
            actions = listOf(
                RuleAction(RuleActions.APPLY_LABEL, mapOf("label" to "Acme")),
                RuleAction(RuleActions.SKIP_INBOX),
            ),
            order = 3,
        )
        val body = payload(outbox, id)
        assertEquals(RuleOp.CREATE, body.op)
        val wire = body.rule ?: error("create carries no rule")
        assertEquals("Acme", wire["name"]?.jsonPrimitive?.content)
        assertEquals(3, wire["order"]?.jsonPrimitive?.content?.toInt())
        assertEquals(true, wire["enabled"]?.jsonPrimitive?.content?.toBoolean())
        val condition = wire["conditions"]!!.jsonArray[0].jsonObject
        assertEquals("from", condition["field"]?.jsonPrimitive?.content)
        assertEquals("equals", condition["op"]?.jsonPrimitive?.content)
        assertEquals("bob@example.local", condition["value"]?.jsonPrimitive?.content)
        val action = wire["actions"]!!.jsonArray[0].jsonObject
        assertEquals("apply-label", action["kind"]?.jsonPrimitive?.content)
        assertEquals("Acme", action["params"]!!.jsonObject["label"]?.jsonPrimitive?.content)
    }

    @Test
    fun disablingARuleChangesTheRowBeforeTheWriteGoesOut() = runTest {
        val (store, outbox, filters) = harness()
        store.upsertManagedRules(listOf(rule("7")))
        val id = filters.setEnabled(rule("7"), false)
        assertFalse(store.managedRuleList().single().enabled)
        val body = payload(outbox, id)
        assertEquals(RuleOp.UPDATE, body.op)
        assertEquals(false, body.updates["7"]?.get("enabled")?.jsonPrimitive?.content?.toBoolean())
    }

    @Test
    fun movingARuleRenumbersTheSetAndSendsOnePatchPerMovedRule() = runTest {
        val (store, outbox, filters) = harness()
        val all = listOf(rule("1", order = 0), rule("2", order = 1), rule("3", order = 2))
        store.upsertManagedRules(all)

        val id = filters.move(all[2], all, up = true) ?: error("the move was not queued")
        assertEquals(listOf("1", "3", "2"), store.managedRuleList().map { it.id })
        val body = payload(outbox, id)
        assertEquals(setOf("2", "3"), body.updates.keys)
        assertEquals(1, body.updates["3"]?.get("order")?.jsonPrimitive?.content?.toInt())
        assertEquals(2, body.updates["2"]?.get("order")?.jsonPrimitive?.content?.toInt())
    }

    @Test
    fun movingTheFirstRuleUpDoesNothing() = runTest {
        val (_, _, filters) = harness()
        val all = listOf(rule("1", order = 0), rule("2", order = 1))
        assertNull(filters.move(all[0], all, up = true))
    }

    @Test
    fun deleteDropsTheRowAndQueuesTheDestroy() = runTest {
        val (store, outbox, filters) = harness()
        store.upsertManagedRules(listOf(rule("7")))
        val id = filters.delete(rule("7"))
        assertTrue(store.managedRuleList().isEmpty())
        val body = payload(outbox, id)
        assertEquals(RuleOp.DESTROY, body.op)
        assertEquals("7", body.ruleId)
    }

    @Test
    fun muteAndBlockQueueTheServerSideCompositions() = runTest {
        val (_, outbox, filters) = harness()
        val mute = payload(outbox, filters.setMuted("acct-a", "t-1", muted = true))
        assertEquals(RuleOp.MUTE, mute.op)
        assertEquals("t-1", mute.threadId)

        val unmute = payload(outbox, filters.setMuted("acct-a", "t-1", muted = false))
        assertEquals(RuleOp.UNMUTE, unmute.op)

        val block = payload(outbox, filters.blockSender("acct-a", "spam@example.invalid"))
        assertEquals(RuleOp.BLOCK, block.op)
        assertEquals("spam@example.invalid", block.address)
    }

    @Test
    fun aMuteRuleIsRecognisedAndKeptOutOfTheFiltersList() {
        val mute = ManagedRule(
            accountId = "acct-a",
            id = "9",
            conditions = listOf(RuleCondition(RuleFields.THREAD_ID, RuleOps.EQUALS, "t-1")),
            actions = listOf(RuleAction(RuleActions.SKIP_INBOX), RuleAction(RuleActions.MARK_READ)),
        )
        assertTrue(FilterActions.isThreadMuteRule(mute, "t-1"))
        assertFalse(FilterActions.isThreadMuteRule(mute, "t-2"))
        assertEquals(listOf("1"), FilterActions.userRules(listOf(rule("1"), mute)).map { it.id })
    }

    @Test
    fun aBlockedSenderRuleIsRecognisedByItsShape() {
        val blocked = ManagedRule(
            accountId = "acct-a",
            id = "4",
            conditions = listOf(RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "spam@example.invalid")),
            actions = listOf(RuleAction(RuleActions.DELETE)),
        )
        assertTrue(FilterActions.isBlockedSenderRule(blocked))
        assertFalse(FilterActions.isBlockedSenderRule(rule("1")))
    }

    @Test
    fun seedConditionsCarrySenderAndStrippedSubject() {
        val seeds = FilterActions.seedConditions("bob@example.local", "Re: Fwd: Quarterly report")
        assertEquals(
            listOf(
                RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "bob@example.local"),
                RuleCondition(RuleFields.SUBJECT, RuleOps.CONTAINS, "Quarterly report"),
            ),
            seeds,
        )
    }

    @Test
    fun seedConditionsFallBackToAnEmptyRowWhenTheMessageCarriesNothing() {
        val seeds = FilterActions.seedConditions("", "")
        assertEquals(listOf(RuleCondition(RuleFields.FROM, RuleOps.CONTAINS, "")), seeds)
    }
}
