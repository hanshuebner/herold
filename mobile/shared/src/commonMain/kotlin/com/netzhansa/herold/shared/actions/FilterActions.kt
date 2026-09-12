package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.actions.MailActions.Labels
import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.RuleOp
import com.netzhansa.herold.shared.outbox.RulePayload
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.coroutines.flow.Flow
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import kotlinx.serialization.json.addJsonObject

/**
 * Filter-rule writes (suite REQ-FLT-20, REQ-MAIL-136). Like every other
 * mutation on the phone they go through the durable outbox: the local row
 * changes at once where the client knows the result, the `ManagedRule/set`
 * follows from the drain, and a write made with no connection waits
 * (REQ-AND-SYNC-20..25).
 *
 * A create, a mute and a block have no local row to write - the server
 * assigns the id and composes the rule - so they queue and land when the
 * drain reads the rule set back.
 */
class FilterActions(
    private val store: LocalStore,
    private val outbox: Outbox,
    private val requestDrain: () -> Unit = {},
) {
    /** The rule set as the store holds it, in execution order. */
    val rules: Flow<List<ManagedRule>> = store.managedRules()

    /** Queues a new rule. Returns the outbox entry it is waiting in. */
    suspend fun create(
        accountId: String,
        name: String,
        conditions: List<RuleCondition>,
        actions: List<RuleAction>,
        order: Int,
    ): Long = enqueue(
        accountId = accountId,
        label = Labels.FILTER_CREATE,
        payload = RulePayload(
            op = RuleOp.CREATE,
            accountId = accountId,
            rule = buildJsonObject {
                put("name", name)
                put("enabled", true)
                put("order", order)
                putJsonArray("conditions") {
                    conditions.forEach { condition ->
                        addJsonObject {
                            put("field", condition.field)
                            put("op", condition.op)
                            put("value", condition.value)
                        }
                    }
                }
                putJsonArray("actions") {
                    actions.forEach { action ->
                        addJsonObject {
                            put("kind", action.kind)
                            putJsonObject("params") {
                                action.params.forEach { (key, value) -> put(key, value) }
                            }
                        }
                    }
                }
            },
        ),
    )

    /** Queues an edit of an existing rule and writes it locally at once. */
    suspend fun update(
        rule: ManagedRule,
        name: String,
        conditions: List<RuleCondition>,
        actions: List<RuleAction>,
    ): Long {
        store.upsertManagedRules(
            listOf(rule.copy(name = name, conditions = conditions, actions = actions)),
        )
        return enqueue(
            accountId = rule.accountId,
            label = Labels.FILTER_UPDATE,
            payload = RulePayload(
                op = RuleOp.UPDATE,
                accountId = rule.accountId,
                updates = mapOf(
                    rule.id to buildJsonObject {
                        put("name", name)
                        putJsonArray("conditions") {
                            conditions.forEach { condition ->
                                addJsonObject {
                                    put("field", condition.field)
                                    put("op", condition.op)
                                    put("value", condition.value)
                                }
                            }
                        }
                        putJsonArray("actions") {
                            actions.forEach { action ->
                                addJsonObject {
                                    put("kind", action.kind)
                                    putJsonObject("params") {
                                        action.params.forEach { (key, value) -> put(key, value) }
                                    }
                                }
                            }
                        }
                    },
                ),
            ),
        )
    }

    /** Turns a rule on or off; the row changes before the write goes out. */
    suspend fun setEnabled(rule: ManagedRule, enabled: Boolean): Long {
        store.upsertManagedRules(listOf(rule.copy(enabled = enabled)))
        return enqueue(
            accountId = rule.accountId,
            label = if (enabled) Labels.FILTER_ENABLE else Labels.FILTER_DISABLE,
            payload = RulePayload(
                op = RuleOp.UPDATE,
                accountId = rule.accountId,
                updates = mapOf(rule.id to buildJsonObject { put("enabled", enabled) }),
            ),
        )
    }

    /**
     * Moves [rule] one place towards the front or the back of its
     * account's rule set. The whole account's rules are renumbered from
     * zero so the order is total even when the server-side values were
     * equal, and the moved pair goes out as one `ManagedRule/set`.
     */
    suspend fun move(rule: ManagedRule, all: List<ManagedRule>, up: Boolean): Long? {
        val ordered = all.filter { it.accountId == rule.accountId }
            .sortedWith(compareBy({ it.order }, { it.id }))
            .toMutableList()
        val index = ordered.indexOfFirst { it.id == rule.id }
        if (index < 0) return null
        val target = if (up) index - 1 else index + 1
        if (target !in ordered.indices) return null
        val moved = ordered.removeAt(index)
        ordered.add(target, moved)

        val renumbered = ordered.mapIndexed { position, row -> row.copy(order = position) }
        store.upsertManagedRules(renumbered)
        val patches = renumbered.filter { row ->
            all.firstOrNull { it.id == row.id }?.order != row.order
        }.associate { row -> row.id to buildJsonObject { put("order", row.order) } }
        if (patches.isEmpty()) return null
        return enqueue(
            accountId = rule.accountId,
            label = Labels.FILTER_REORDER,
            payload = RulePayload(
                op = RuleOp.UPDATE,
                accountId = rule.accountId,
                updates = patches,
            ),
        )
    }

    /** Deletes a rule; the row goes at once and the destroy follows. */
    suspend fun delete(rule: ManagedRule): Long {
        store.deleteManagedRules(rule.accountId, listOf(rule.id))
        return enqueue(
            accountId = rule.accountId,
            label = Labels.FILTER_DELETE,
            payload = RulePayload(
                op = RuleOp.DESTROY,
                accountId = rule.accountId,
                ruleId = rule.id,
            ),
        )
    }

    /** Mutes or unmutes a conversation (`Thread/mute` / `Thread/unmute`). */
    suspend fun setMuted(accountId: String, threadId: String, muted: Boolean): Long = enqueue(
        accountId = accountId,
        label = if (muted) Labels.MUTE else Labels.UNMUTE,
        payload = RulePayload(
            op = if (muted) RuleOp.MUTE else RuleOp.UNMUTE,
            accountId = accountId,
            threadId = threadId,
        ),
    )

    /** Blocks a sender (`BlockedSender/set`): the server's delete rule. */
    suspend fun blockSender(accountId: String, address: String): Long = enqueue(
        accountId = accountId,
        label = Labels.BLOCK,
        payload = RulePayload(
            op = RuleOp.BLOCK,
            accountId = accountId,
            address = address,
        ),
    )

    private suspend fun enqueue(
        accountId: String,
        label: String,
        payload: RulePayload,
    ): Long {
        val id = outbox.enqueueRule(accountId, label, payload)
        requestDrain()
        return id
    }

    companion object {
        /**
         * True when [rule] is the mute rule of [threadId]: the shape
         * `Thread/mute` writes, a single `thread-id` condition
         * (suite `managed-rules.svelte.ts`).
         */
        fun isThreadMuteRule(rule: ManagedRule, threadId: String): Boolean =
            rule.enabled &&
                rule.conditions.size == 1 &&
                rule.conditions[0].field == RuleFields.THREAD_ID &&
                rule.conditions[0].value == threadId

        /** True when [rule] is the shape `BlockedSender/set` writes. */
        fun isBlockedSenderRule(rule: ManagedRule): Boolean =
            rule.conditions.size == 1 &&
                rule.conditions[0].field == RuleFields.FROM &&
                rule.conditions[0].op == RuleOps.EQUALS &&
                rule.actions.size == 1 &&
                rule.actions[0].kind == RuleActions.DELETE

        /**
         * The rules the filters screen lists: the user's own, without the
         * per-conversation mutes the overflow menu writes, which would
         * otherwise fill the list one row per muted thread.
         */
        fun userRules(rules: List<ManagedRule>): List<ManagedRule> = rules.filterNot { rule ->
            rule.conditions.size == 1 && rule.conditions[0].field == RuleFields.THREAD_ID
        }

        /** The conditions "Create filter from this message" opens with (REQ-FLT-32). */
        fun seedConditions(fromEmail: String, subject: String): List<RuleCondition> = buildList {
            if (fromEmail.isNotBlank()) {
                add(RuleCondition(RuleFields.FROM, RuleOps.EQUALS, fromEmail))
            }
            val stripped = stripReplyPrefixes(subject)
            if (stripped.isNotBlank()) {
                add(RuleCondition(RuleFields.SUBJECT, RuleOps.CONTAINS, stripped))
            }
            if (isEmpty()) add(RuleCondition(RuleFields.FROM, RuleOps.CONTAINS, ""))
        }

        /** "Re: Fwd: Report" -> "Report" (REQ-FLT-32). */
        fun stripReplyPrefixes(subject: String): String {
            var text = subject.trim()
            while (true) {
                val match = REPLY_PREFIX.find(text) ?: break
                text = text.substring(match.value.length).trim()
            }
            return text
        }

        private val REPLY_PREFIX = Regex("^(re|fwd|fw|aw|wg)\\s*(\\[\\d+\\])?\\s*:\\s*", RegexOption.IGNORE_CASE)
    }
}
