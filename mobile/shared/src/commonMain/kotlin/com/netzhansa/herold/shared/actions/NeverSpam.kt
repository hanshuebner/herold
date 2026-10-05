package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps

/** Whether a never-spam rule covers the sender's address or its domain. */
enum class NeverSpamScope { ADDRESS, DOMAIN }

/**
 * What a never-spam choice amounts to against the rules the account
 * already holds.
 */
sealed interface NeverSpamPlan {
    /** No rule carries this condition; [conditions]/[actions] are the new rule. */
    data class Create(
        val name: String,
        val conditions: List<RuleCondition>,
        val actions: List<RuleAction>,
    ) : NeverSpamPlan

    /** A rule with this condition already keeps the sender out of Junk. */
    data class Reuse(val rule: ManagedRule) : NeverSpamPlan

    /** A rule with this condition exists; [nextActions] adds never-spam to it. */
    data class AddAction(val rule: ManagedRule, val nextActions: List<RuleAction>) : NeverSpamPlan
}

/**
 * The never-spam allow rule the "Not spam" sheet offers, as the Suite
 * builds it (`web/apps/suite/src/lib/mail/not-spam.ts`, REQ-FLT-16 /
 * REQ-FILT-02a, issue #382). Pure, so the mapping from a sender and a
 * scope to a `ManagedRule` is measured without a store or a screen, and
 * both clients can be held to the same shapes.
 */
object NeverSpam {

    /**
     * The domain part of [address], lower-cased; "" when the address
     * carries none, which the caller reads as "no domain to scope to".
     */
    fun senderDomain(address: String): String {
        val at = address.trim().lastIndexOf('@')
        val trimmed = address.trim()
        if (at < 0 || at == trimmed.length - 1) return ""
        return trimmed.substring(at + 1).lowercase()
    }

    /** What a rule for [scope] matches on, or null when there is nothing to match. */
    fun condition(scope: NeverSpamScope, senderEmail: String): RuleCondition? {
        val trimmed = senderEmail.trim()
        val value = when (scope) {
            NeverSpamScope.ADDRESS -> trimmed
            NeverSpamScope.DOMAIN -> senderDomain(trimmed)
        }
        if (value.isBlank()) return null
        return RuleCondition(
            field = if (scope == NeverSpamScope.ADDRESS) RuleFields.FROM else RuleFields.FROM_DOMAIN,
            op = RuleOps.EQUALS,
            value = value,
        )
    }

    /** What the rule is called in the filter list. */
    fun ruleName(scope: NeverSpamScope, senderEmail: String): String = when (scope) {
        NeverSpamScope.ADDRESS -> "Never spam: ${senderEmail.trim()}"
        NeverSpamScope.DOMAIN -> "Never spam: ${senderDomain(senderEmail)}"
    }

    /**
     * What to do about a never-spam rule for [scope] and [senderEmail],
     * given the rules the account holds: create one, extend the rule
     * that already carries the same condition, or leave the one that
     * already keeps the sender out of Junk alone. Null when the scope
     * has no value to match on, so the caller writes nothing.
     *
     * Matching an existing rule is what keeps a second "Not spam" on
     * mail from the same domain from writing a second identical rule.
     */
    fun plan(
        rules: List<ManagedRule>,
        scope: NeverSpamScope,
        senderEmail: String,
    ): NeverSpamPlan? {
        val condition = condition(scope, senderEmail) ?: return null
        val existing = rules.firstOrNull { rule ->
            rule.conditions.size == 1 &&
                rule.conditions[0].field == condition.field &&
                rule.conditions[0].op == condition.op &&
                rule.conditions[0].value.equals(condition.value, ignoreCase = true)
        } ?: return NeverSpamPlan.Create(
            name = ruleName(scope, senderEmail),
            conditions = listOf(condition),
            actions = listOf(RuleAction(RuleActions.NEVER_SPAM)),
        )
        if (existing.actions.any { it.kind == RuleActions.NEVER_SPAM }) {
            return NeverSpamPlan.Reuse(existing)
        }
        return NeverSpamPlan.AddAction(
            rule = existing,
            nextActions = existing.actions + RuleAction(RuleActions.NEVER_SPAM),
        )
    }

    /** The order a new rule takes: after every rule the account holds. */
    fun nextOrder(rules: List<ManagedRule>, accountId: String): Int =
        (rules.filter { it.accountId == accountId }.maxOfOrNull { it.order } ?: -1) + 1
}
