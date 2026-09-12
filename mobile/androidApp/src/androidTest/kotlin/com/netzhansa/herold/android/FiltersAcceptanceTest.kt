package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Filters over the server's managed rules (issue #361, suite
 * REQ-FLT-20/30/31/32), driven against an ephemeral herold:
 *
 *   am instrument ... -e class com.netzhansa.herold.android.FiltersAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ...
 *
 * The rule is read back through `ManagedRule/get` on an independent
 * client, and the mail it labels and archives is delivered over SMTP, so
 * what passes is the server's behaviour rather than the screen's.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class FiltersAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    /** A label to file matching mail into; unique so reruns do not collide. */
    private val labelName = "Acme"

    @Test
    fun t05_aQueuedRuleWriteDrainsRatherThanSittingInTheOutbox(): Unit = runBlocking {
        signInAndSync()
        val session = app.container.session.value!!
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        clearRules(client, accountId)
        session.filters.create(
            accountId = accountId,
            name = "Drain probe",
            conditions = listOf(
                com.netzhansa.herold.shared.domain.RuleCondition("from", "equals", "probe@vendor.example"),
            ),
            actions = listOf(com.netzhansa.herold.shared.domain.RuleAction("skip-inbox")),
            order = 0,
        )
        session.drainer.drain()
        val left = app.container.outbox.list()
        assertTrue(
            "the rule write did not drain: " + left.map { "${it.state}/${it.lastError}" },
            left.isEmpty(),
        )
    }

    @Test
    fun t10_aFilterCreatedOnThePhoneReachesTheServerAndFilesMail(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        clearRules(client, accountId)

        val sender = "acme-${System.currentTimeMillis()}@vendor.example"
        openFilters()
        compose.onNodeWithTag("filters-new").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("filter-editor").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("filter-name").performTextReplacement("Acme mail")
        compose.onNodeWithTag("condition-field-0").performClick()
        compose.onNodeWithTag("condition-field-0-from").performClick()
        compose.onNodeWithTag("condition-op-0").performClick()
        compose.onNodeWithTag("condition-op-0-equals").performClick()
        compose.onNodeWithTag("condition-value-0").performTextReplacement(sender)

        // Label the message, and add skipping the inbox as a second action.
        compose.onNodeWithTag("action-kind-0").performClick()
        compose.onNodeWithTag("action-kind-0-apply-label").performClick()
        compose.onNodeWithTag("action-value-0").performTextReplacement(labelName)
        compose.onNodeWithTag("filter-add-action").performClick()
        compose.onNodeWithTag("action-kind-1").performClick()
        compose.onNodeWithTag("action-kind-1-skip-inbox").performClick()
        compose.captureScreen("m3a-filter-editor")
        compose.onNodeWithTag("filter-save").performClick()

        val rule = awaitServerRule(client, accountId) { rules -> rules.firstOrNull { it.name == "Acme mail" } }
        assertEquals(sender, rule.conditions.single().value)
        assertEquals(
            setOf("apply-label", "skip-inbox"),
            rule.actions.map { it.kind }.toSet(),
        )
        assertEquals(labelName, rule.actions.first { it.kind == "apply-label" }.params["label"])

        // The row renders from the store, which the drain filled with the
        // server's rule set; assert that first so a miss says which half.
        val stored = runBlocking {
            repeat(POLL_ATTEMPTS) {
                app.container.store.managedRuleList().firstOrNull { it.id == rule.id }
                    ?.let { return@runBlocking it }
                Thread.sleep(POLL_MS)
            }
            null
        }
        assertNotNull("the drain never took the rule into the store", stored)
        compose.waitForIdle()
        compose.captureScreen("m3a-filters-list")
        // The row's own tag: the clickable row merges its children's
        // semantics, so the title node is not addressable on its own.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("filter-${rule.id}").fetchSemanticsNodes().isNotEmpty()
        }

        // A message matching it lands labelled and out of the inbox.
        // herold delivers one copy per Sieve target, so apply-label plus
        // skip-inbox files a copy into the label and a copy into Archive;
        // what the rule promises is that the label holds it and the inbox
        // does not.
        val subject = "Filtered ${System.currentTimeMillis()}"
        DevInstance.deliverMail(subject = subject, from = "Acme <$sender>", body = "Filed by the rule.")
        val filed = awaitAnywhere(subject)
        val boxes = app.container.store.mailboxList()
        val inbox = boxes.firstOrNull { it.accountId == filed.accountId && it.role == "inbox" }
        val label = boxes.firstOrNull { it.accountId == filed.accountId && it.name == labelName }
        assertNotNull("the filter's label was not created: ${boxes.map { it.name }}", label)
        val copies = app.container.store.emailList().filter { it.subject == subject }
        assertTrue(
            "no copy is in $labelName: ${copies.map { it.mailboxIds }}",
            copies.any { it.mailboxIds.contains(label!!.id) },
        )
        assertFalse(
            "skip-inbox did not take: a copy is still in the inbox",
            inbox != null && copies.any { it.mailboxIds.contains(inbox.id) },
        )
    }

    @Test
    fun t20_reorderingAndDisablingARuleRoundTripThroughTheServer(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        clearRules(client, accountId)

        // Two rules, created through the app so the create path is the one
        // under test, then reordered and disabled from the list.
        createRule(name = "First", value = "first@vendor.example")
        createRule(name = "Second", value = "second@vendor.example")
        val first = awaitServerRule(client, accountId) { rules -> rules.firstOrNull { it.name == "First" } }
        val second = awaitServerRule(client, accountId) { rules -> rules.firstOrNull { it.name == "Second" } }

        openFilters()
        compose.onNodeWithTag("filters-list").performScrollToNode(hasTestTag("filter-up-${second.id}"))
        compose.onNodeWithTag("filter-up-${second.id}").performClick()
        val reordered = awaitServerRule(client, accountId) { rules ->
            val moved = rules.firstOrNull { it.id == second.id } ?: return@awaitServerRule null
            val stayed = rules.firstOrNull { it.id == first.id } ?: return@awaitServerRule null
            if (moved.order < stayed.order) moved else null
        }
        assertTrue("the reorder did not reach the server", reordered.id == second.id)

        compose.onNodeWithTag("filter-enabled-${first.id}").performClick()
        val disabled = awaitServerRule(client, accountId) { rules ->
            rules.firstOrNull { it.id == first.id && !it.enabled }
        }
        assertFalse(disabled.enabled)
        compose.captureScreen("m3a-filters-reordered")
    }

    @Test
    fun t30_mutingAConversationWritesTheServersRuleAndTheOverflowFlips(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        clearRules(client, accountId)

        val subject = "Mute me ${System.currentTimeMillis()}"
        DevInstance.deliverMail(subject = subject, body = "A noisy conversation.")
        val message = awaitAnywhere(subject)

        openThread(message.threadId)
        compose.onNodeWithTag("thread-overflow").performClick()
        compose.onNodeWithTag("thread-mute").performClick()

        val mute = awaitServerRule(client, accountId) { rules ->
            rules.firstOrNull { rule ->
                rule.conditions.singleOrNull()?.field == "thread-id" &&
                    rule.conditions.single().value == message.threadId
            }
        }
        assertEquals(
            setOf("skip-inbox", "mark-read"),
            mute.actions.map { it.kind }.toSet(),
        )
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitForIdle()
        compose.onNodeWithTag("thread-overflow").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Unmute conversation").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m3a-thread-overflow")
    }

    // ---- helpers ---------------------------------------------------------

    private fun openThread(threadId: String) {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }


    private fun createRule(name: String, value: String) {
        openFilters()
        compose.onNodeWithTag("filters-new").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("filter-editor").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("filter-name").performTextReplacement(name)
        compose.onNodeWithTag("condition-value-0").performTextReplacement(value)
        compose.onNodeWithTag("filter-save").performClick()
        compose.waitForIdle()
    }

    /** Opens the Filters destination from the drawer, wherever the shell is. */
    private fun openFilters() {
        while (compose.onAllNodesWithTag("inbox-drawer-open").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-filters").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-filters").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("filters-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun signInAndSync() = runBlocking {
        grantNotificationPermission()
        if (app.container.session.value == null) {
            val result = app.container.signInWithPassword(
                DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Drops whatever an earlier run left, so the checks start from nothing. */
    private suspend fun clearRules(client: JmapClient, accountId: String) {
        val existing = client.managedRuleGet(accountId, null).list.map { it.id }
        if (existing.isNotEmpty()) client.managedRuleSet(accountId, destroy = existing)
        app.container.session.value!!.syncEngine.syncAll()
    }

    /** The server's rules, polled until one matches; the rule set is the truth. */
    private suspend fun awaitServerRule(
        client: JmapClient,
        accountId: String,
        pick: (List<ManagedRule>) -> ManagedRule?,
    ): ManagedRule {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        var seen: List<ManagedRule> = emptyList()
        while (System.currentTimeMillis() < deadline) {
            seen = client.managedRuleGet(accountId, null).list.map { wire ->
                ManagedRule(
                    accountId = accountId,
                    id = wire.id,
                    name = wire.name,
                    enabled = wire.enabled,
                    order = wire.order,
                    conditions = wire.conditions.map {
                        com.netzhansa.herold.shared.domain.RuleCondition(it.field, it.op, it.value)
                    },
                    actions = wire.actions.map { action ->
                        com.netzhansa.herold.shared.domain.RuleAction(
                            action.kind,
                            action.params.mapValues { (_, value) ->
                                (value as? kotlinx.serialization.json.JsonPrimitive)?.content ?: value.toString()
                            },
                        )
                    },
                )
            }
            pick(seen)?.let { return it }
            Thread.sleep(POLL_MS)
        }
        error("no rule matched; the server holds ${seen.map { "${it.id}:${it.name}:${it.order}:${it.enabled}" }}")
    }

    /** Syncs until the message is in the store, wherever the filter filed it. */
    private fun awaitAnywhere(subject: String): Email = runBlocking {
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.emailList().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the store")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
    }
}
