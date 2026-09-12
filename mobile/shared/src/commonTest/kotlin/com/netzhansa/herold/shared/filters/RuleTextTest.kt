package com.netzhansa.herold.shared.filters

import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/** The words the filter list and the editor use (suite REQ-FLT-30/31). */
class RuleTextTest {

    @Test
    fun conditionsReadAsSentences() {
        assertEquals(
            "From is bob@example.local",
            RuleText.describe(RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "bob@example.local")),
        )
        assertEquals(
            "Subject contains report",
            RuleText.describe(RuleCondition(RuleFields.SUBJECT, RuleOps.CONTAINS, "report")),
        )
        assertEquals(
            "From domain matches *.acme.com",
            RuleText.describe(RuleCondition(RuleFields.FROM_DOMAIN, RuleOps.WILDCARD, "*.acme.com")),
        )
        assertEquals(
            "Has an attachment",
            RuleText.describe(RuleCondition(RuleFields.HAS_ATTACHMENT, RuleOps.EQUALS, "true")),
        )
    }

    @Test
    fun actionsNameTheirValueWhenTheyTakeOne() {
        assertEquals(
            "Apply label Acme",
            RuleText.describe(RuleAction(RuleActions.APPLY_LABEL, mapOf("label" to "Acme"))),
        )
        assertEquals(
            "Forward to ops@example.local",
            RuleText.describe(RuleAction(RuleActions.FORWARD, mapOf("to" to "ops@example.local"))),
        )
        assertEquals("Skip the inbox", RuleText.describe(RuleAction(RuleActions.SKIP_INBOX)))
    }

    @Test
    fun aRuleWithoutANameIsTitledByItsConditions() {
        val rule = ManagedRule(
            accountId = "acct-a",
            id = "3",
            conditions = listOf(
                RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "bob@example.local"),
                RuleCondition(RuleFields.SUBJECT, RuleOps.CONTAINS, "report"),
            ),
            actions = listOf(RuleAction(RuleActions.SKIP_INBOX)),
        )
        assertEquals("From is bob@example.local and Subject contains report", RuleText.title(rule))
        assertEquals("Skip the inbox", RuleText.actionLine(rule))
        assertEquals("Acme", RuleText.title(rule.copy(name = "Acme")))
    }

    @Test
    fun validationCatchesWhatTheServerWouldRefuse() {
        val condition = RuleCondition(RuleFields.FROM, RuleOps.EQUALS, "bob@example.local")
        assertEquals(
            "Add at least one condition.",
            RuleText.validate(emptyList(), listOf(RuleAction(RuleActions.SKIP_INBOX))),
        )
        assertEquals(
            "From needs a value.",
            RuleText.validate(listOf(condition.copy(value = "")), listOf(RuleAction(RuleActions.SKIP_INBOX))),
        )
        assertEquals("Add at least one action.", RuleText.validate(listOf(condition), emptyList()))
        assertEquals(
            "Apply label needs a value.",
            RuleText.validate(listOf(condition), listOf(RuleAction(RuleActions.APPLY_LABEL))),
        )
        assertEquals(
            "Move to Trash and Apply label cannot be combined: Trash wins.",
            RuleText.validate(
                listOf(condition),
                listOf(
                    RuleAction(RuleActions.DELETE),
                    RuleAction(RuleActions.APPLY_LABEL, mapOf("label" to "Acme")),
                ),
            ),
        )
        assertNull(RuleText.validate(listOf(condition), listOf(RuleAction(RuleActions.SKIP_INBOX))))
    }

    @Test
    fun aHasAttachmentConditionNeedsNoValue() {
        assertNull(
            RuleText.validate(
                listOf(RuleCondition(RuleFields.HAS_ATTACHMENT, RuleOps.EQUALS, "")),
                listOf(RuleAction(RuleActions.MARK_READ)),
            ),
        )
    }
}
