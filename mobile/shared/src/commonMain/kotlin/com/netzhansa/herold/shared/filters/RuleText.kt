package com.netzhansa.herold.shared.filters

import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps

/**
 * The human-readable form of a filter rule (suite REQ-FLT-30/31): the
 * filter list and the editor state the conditions and actions in words,
 * and the Sieve the server compiles them into is never shown as the thing
 * being edited.
 *
 * It is pure text assembly, so the phrasing is asserted in `commonTest`
 * rather than read off a screenshot.
 */
object RuleText {
    fun fieldLabel(field: String): String = when (field) {
        RuleFields.FROM -> "From"
        RuleFields.FROM_DOMAIN -> "From domain"
        RuleFields.TO -> "To"
        RuleFields.SUBJECT -> "Subject"
        RuleFields.HAS_ATTACHMENT -> "Has attachment"
        RuleFields.THREAD_ID -> "Conversation"
        else -> field
    }

    fun opLabel(op: String): String = when (op) {
        RuleOps.CONTAINS -> "contains"
        RuleOps.EQUALS -> "is"
        RuleOps.WILDCARD -> "matches"
        else -> op
    }

    fun actionLabel(kind: String): String = when (kind) {
        RuleActions.APPLY_LABEL -> "Apply label"
        RuleActions.SKIP_INBOX -> "Skip the inbox"
        RuleActions.MARK_READ -> "Mark as read"
        RuleActions.DELETE -> "Move to Trash"
        RuleActions.FORWARD -> "Forward to"
        else -> kind
    }

    /** True when the action kind takes a value the editor has to ask for. */
    fun actionTakesValue(kind: String): Boolean =
        kind == RuleActions.APPLY_LABEL || kind == RuleActions.FORWARD

    /** The parameter name an action's value is carried under. */
    fun actionParamName(kind: String): String = when (kind) {
        RuleActions.APPLY_LABEL -> "label"
        RuleActions.FORWARD -> "to"
        else -> "value"
    }

    fun describe(condition: RuleCondition): String = when (condition.field) {
        RuleFields.HAS_ATTACHMENT ->
            if (condition.value.equals("false", ignoreCase = true)) {
                "Has no attachment"
            } else {
                "Has an attachment"
            }

        RuleFields.THREAD_ID -> "This conversation"
        else -> "${fieldLabel(condition.field)} ${opLabel(condition.op)} ${condition.value}"
    }

    fun describe(action: RuleAction): String {
        val value = action.params[actionParamName(action.kind)].orEmpty()
        return if (actionTakesValue(action.kind) && value.isNotBlank()) {
            "${actionLabel(action.kind)} $value"
        } else {
            actionLabel(action.kind)
        }
    }

    /** The conditions of a rule as one line: they AND together (REQ-FLT-02). */
    fun conditionLine(rule: ManagedRule): String =
        rule.conditions.joinToString(" and ") { describe(it) }

    /** The actions of a rule as one line. */
    fun actionLine(rule: ManagedRule): String =
        rule.actions.joinToString(", ") { describe(it) }

    /**
     * What the filter list calls a rule: its name when the user gave it
     * one, and otherwise the conditions it matches on.
     */
    fun title(rule: ManagedRule): String = rule.name.ifBlank {
        conditionLine(rule).ifBlank { "Filter ${rule.id}" }
    }

    /**
     * Why the editor will not save yet, or null when the rule is
     * well-formed. The checks mirror the server's, so a refusal is caught
     * before the write is queued rather than coming back from the drain.
     */
    fun validate(conditions: List<RuleCondition>, actions: List<RuleAction>): String? {
        if (conditions.isEmpty()) return "Add at least one condition."
        conditions.firstOrNull { it.field != RuleFields.HAS_ATTACHMENT && it.value.isBlank() }
            ?.let { return "${fieldLabel(it.field)} needs a value." }
        if (actions.isEmpty()) return "Add at least one action."
        actions.firstOrNull { actionTakesValue(it.kind) && it.params[actionParamName(it.kind)].isNullOrBlank() }
            ?.let { return "${actionLabel(it.kind)} needs a value." }
        val kinds = actions.map { it.kind }.toSet()
        if (RuleActions.DELETE in kinds && RuleActions.APPLY_LABEL in kinds) {
            return "Move to Trash and Apply label cannot be combined: Trash wins."
        }
        return null
    }
}
