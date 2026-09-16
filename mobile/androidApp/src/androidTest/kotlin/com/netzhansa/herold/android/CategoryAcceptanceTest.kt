package com.netzhansa.herold.android

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTouchInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.jmap.WireMailbox
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Category disposition and priority (issue #399, server issue #333,
 * suite REQ-CAT-01/04/05/10/11), driven against an ephemeral herold:
 *
 *   am instrument ... -e class com.netzhansa.herold.android.CategoryAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ...
 *
 * The dispositions under test are set over JMAP by an independent
 * client, so what the tabs, the bundle and the absent conversation prove
 * is that the phone reads the server's setting rather than deciding for
 * itself. The last check writes one from the phone and reads it back
 * through that same independent client.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class CategoryAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Test
    fun t05_derivedCategoriesAreTabsWithoutALabelAndCollapseWhenGivenOne(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        // The production shape of issue #404: the classifier's categories
        // ride on the messages and the account has no label for any of
        // them, so nothing on the wire carries a disposition.
        dropLabels(client, accountId)
        val promo = "+promo derived ${System.nanoTime()}"
        val updates = "+updates derived ${System.nanoTime()}"
        DevInstance.deliverMail(subject = promo, body = "A sale.")
        DevInstance.deliverMail(subject = updates, body = "A receipt.")
        awaitInbox(promo)
        awaitInbox(updates)
        // The classifier's two categories are the only ones the account
        // carries, so the pinned budget is not spent on a category an
        // earlier check assigned by hand.
        clearOtherCategories(client, accountId, setOf(promo, updates))
        app.container.session.value!!.syncEngine.syncAll()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tab-$DERIVED_PROMOTIONS").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "a derived category with no label is still a tab",
            compose.onAllNodesWithTag("inbox-tab-$DERIVED_UPDATES").fetchSemanticsNodes().isNotEmpty(),
        )
        compose.captureScreen("m4-derived-tabs")

        // Settings lists the same categories, and choosing a lane for one
        // creates the label the disposition lives on.
        openCategories()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("category-row-$DERIVED_PROMOTIONS").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m4-derived-category-settings")
        compose.onNodeWithTag("category-row-$DERIVED_PROMOTIONS").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("category-choice-bundled").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("category-choice-bundled").performClick()
        compose.waitForIdle()

        val created = awaitServerLabel(client, accountId, DERIVED_PROMOTIONS) { it.disposition == "bundled" }
        assertEquals("bundled", created.disposition)

        backToInbox()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tab-$DERIVED_PROMOTIONS").fetchSemanticsNodes().isEmpty()
        }
        compose.onNodeWithTag("inbox-list")
            .performScrollToNode(hasTestTag("bundle-row-$DERIVED_PROMOTIONS"))
        compose.captureScreen("m4-derived-bundle")
    }

    @Test
    fun t06_aTabWithNoConversationsSaysSoRatherThanShowingNothing(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        // A pinned category no message carries: the stream under its tab
        // is empty, and an empty stream is a screen with words on it
        // rather than a blank one (issue #405).
        ensureLabel(client, accountId, EMPTY_LANE, "pinned", 0)
        app.container.session.value!!.syncEngine.syncAll()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tab-$EMPTY_LANE").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-tab-$EMPTY_LANE").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-empty").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m4-empty-lane")
        compose.onNodeWithTag("inbox-tab-all").performClick()
        compose.waitForIdle()
    }

    @Test
    fun t10_theServersDispositionsDecideTheInboxLanes(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val labels = provisionCategories(client, accountId)
        val messages = provisionMail(client, accountId)
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitForIdle()

        // The tabs are the pinned labels, in the server's priority order.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tab-$PIN_FIRST").fetchSemanticsNodes().isNotEmpty()
        }
        val first = compose.onNodeWithTag("inbox-tab-$PIN_FIRST").fetchSemanticsNode().boundsInWindow
        val second = compose.onNodeWithTag("inbox-tab-$PIN_SECOND").fetchSemanticsNode().boundsInWindow
        assertTrue(
            "priority 0 ($PIN_FIRST at ${first.left}) must sit ahead of priority 1 " +
                "($PIN_SECOND at ${second.left})",
            first.left < second.left,
        )
        assertTrue(
            "a label the server leaves at none is not a tab",
            compose.onAllNodesWithTag("inbox-tab-$PLAIN").fetchSemanticsNodes().isEmpty(),
        )
        assertTrue(
            "a filed label is not a tab",
            compose.onAllNodesWithTag("inbox-tab-$FILED").fetchSemanticsNodes().isEmpty(),
        )

        // The bundled category is one collapsed row, and the filed one is
        // out of the stream altogether.
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("bundle-row-$BUNDLED"))
        compose.captureScreen("m4-inbox-lanes")
        val filedThread = messages.getValue(FILED).threadId
        assertTrue(
            "a filed category's conversation must not be in the inbox stream",
            compose.onAllNodesWithTag("thread-row-$filedThread").fetchSemanticsNodes().isEmpty(),
        )

        // The tab filters to its own lane.
        compose.onNodeWithTag("inbox-tab-$PIN_FIRST").performClick()
        compose.waitForIdle()
        val pinnedThread = messages.getValue(PIN_FIRST).threadId
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-$pinnedThread").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "the tab shows only its own category",
            compose.onAllNodesWithTag("thread-row-${messages.getValue(PLAIN).threadId}")
                .fetchSemanticsNodes().isEmpty(),
        )
        compose.captureScreen("m4-pinned-tab")
        assertEquals(5, labels.size)
    }

    @Test
    fun t20_aDispositionChangedOnThePhoneIsReadBackByAnIndependentClient(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val labels = provisionCategories(client, accountId)
        app.container.session.value!!.syncEngine.syncAll()

        openCategories()
        compose.onNodeWithTag("categories-screen").performScrollToNode(hasTestTag("category-row-$PLAIN"))
        compose.onNodeWithTag("category-row-$PLAIN").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("category-choice-bundled").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("category-choice-bundled").performClick()
        compose.waitForIdle()
        compose.captureScreen("m4-category-settings")

        val id = labels.getValue(PLAIN)
        val server = awaitServerMailbox(client, accountId, id) { it.disposition == "bundled" }
        assertEquals("bundled", server.disposition)
    }

    @Test
    fun t30_draggingThePinnedOrderRewritesThePrioritiesOnTheServer(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val labels = provisionCategories(client, accountId)
        app.container.session.value!!.syncEngine.syncAll()

        openCategories()
        compose.onNodeWithTag("categories-screen").performScrollToNode(hasTestTag("category-drag-$PIN_SECOND"))
        val row = compose.onNodeWithTag("category-pinned-$PIN_SECOND").fetchSemanticsNode()
        val rowHeight = row.size.height.toFloat()
        compose.onNodeWithTag("category-drag-$PIN_SECOND").performTouchInput {
            down(center)
            // Past the touch slop first, then a whole row upwards.
            moveBy(Offset(0f, -rowHeight / 4f))
            moveBy(Offset(0f, -rowHeight))
            up()
        }
        compose.waitForIdle()

        val moved = awaitServerMailbox(client, accountId, labels.getValue(PIN_SECOND)) { it.priority == 0 }
        assertEquals(0, moved.priority)
        val demoted = awaitServerMailbox(client, accountId, labels.getValue(PIN_FIRST)) { it.priority == 1 }
        assertEquals("the server renumbers the label that did not move", 1, demoted.priority)

        // The list the drain refreshed shows the order the server holds.
        compose.waitUntil(TIMEOUT_MS) {
            val first = compose.onAllNodesWithTag("category-pinned-$PIN_SECOND").fetchSemanticsNodes()
                .firstOrNull()?.boundsInRoot?.top ?: return@waitUntil false
            val second = compose.onAllNodesWithTag("category-pinned-$PIN_FIRST").fetchSemanticsNodes()
                .firstOrNull()?.boundsInRoot?.top ?: return@waitUntil false
            first < second
        }
        compose.captureScreen("m4-category-reorder")
    }

    @Test
    fun t40_aSixthPinnedCategoryIsRefusedWithTheServersReason(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        provisionCategories(client, accountId)
        // Five pinned labels, so the sixth the phone asks for is the one
        // the server refuses (REQ-CAT-11).
        val extra = (1..3).map { ensureLabel(client, accountId, "$PIN_FIRST-extra$it", "pinned", 9 + it) }
        val sixth = ensureLabel(client, accountId, PLAIN, "none", null)
        app.container.session.value!!.syncEngine.syncAll()

        openCategories()
        compose.onNodeWithTag("categories-screen").performScrollToNode(hasTestTag("category-row-$PLAIN"))
        compose.onNodeWithTag("category-row-$PLAIN").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("category-choice-pinned").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("category-choice-pinned").performClick()

        val entry = awaitOutboxFailure()
        assertTrue(
            "the refusal must name the pinned limit, not the wire word: ${entry.lastError}",
            entry.lastError.orEmpty().contains("pinned"),
        )
        compose.captureScreen("m4-too-many-pinned")
        // The store is back on the server's truth: the label is not pinned.
        val server = awaitServerMailbox(client, accountId, sixth) { it.disposition == "none" }
        assertEquals("none", server.disposition)
        assertEquals(3, extra.size)
    }

    // ---- helpers ---------------------------------------------------------

    /**
     * The four labels the checks read, with the dispositions the server
     * is told to hold: two pinned (priorities 0 and 1), one bundled, one
     * filed, plus one left at `none`. Every other label of the account is
     * unpinned, so the five-pin budget belongs to this run.
     */
    private suspend fun provisionCategories(
        client: JmapClient,
        accountId: String,
    ): Map<String, String> {
        val mine = mapOf(
            PIN_FIRST to Triple("pinned", 0, true),
            PIN_SECOND to Triple("pinned", 1, true),
            BUNDLED to Triple("bundled", 2, true),
            FILED to Triple("filed", 3, true),
        )
        val existing = client.mailboxGet(accountId, null).list
        val clear = existing.filter { it.role == null && it.disposition != "none" && it.name.lowercase() !in mine }
        if (clear.isNotEmpty()) {
            client.mailboxSet(
                accountId,
                update = clear.associate { it.id to buildJsonObject { put("disposition", "none") } },
            )
        }
        val ids = mine.mapValues { (name, spec) -> ensureLabel(client, accountId, name, spec.first, spec.second) }
        return ids + (PLAIN to ensureLabel(client, accountId, PLAIN, "none", null))
    }

    /** Creates [name] if the account has no such label, then sets its category properties. */
    private suspend fun ensureLabel(
        client: JmapClient,
        accountId: String,
        name: String,
        disposition: String,
        priority: Int?,
    ): String {
        val existing = client.mailboxGet(accountId, null).list
            .firstOrNull { it.role == null && it.name.equals(name, ignoreCase = true) }
        val patch = buildJsonObject {
            put("disposition", disposition)
            if (priority == null) put("priority", JsonPrimitive(null as String?)) else put("priority", priority)
        }
        if (existing == null) {
            val outcome = client.mailboxSet(
                accountId,
                create = mapOf(
                    "cat" to buildJsonObject {
                        put("name", name)
                        put("disposition", disposition)
                        if (priority != null) put("priority", priority)
                    },
                ),
            )
            val created = outcome.created["cat"]
                ?: error("the label $name was not created: ${outcome.errorMessages}")
            return created.id
        }
        val outcome = client.mailboxSet(accountId, update = mapOf(existing.id to patch))
        check(outcome.errorMessages.isEmpty()) { "setting $name to $disposition failed: ${outcome.errorMessages}" }
        return existing.id
    }

    /** One message per category, tagged by the independent client. */
    private fun provisionMail(client: JmapClient, accountId: String): Map<String, Email> = runBlocking {
        listOf(PIN_FIRST, PIN_SECOND, BUNDLED, FILED, PLAIN).associateWith { category ->
            val subject = "category $category ${System.nanoTime()}"
            DevInstance.deliverMail(subject = subject, body = "One message for $category.")
            val delivered = awaitInbox(subject)
            client.emailSet(
                accountId,
                mapOf(
                    delivered.id to buildJsonObject {
                        delivered.keywords.filter { Keywords.categoryName(it) != null }.forEach {
                            put("keywords/$it", JsonPrimitive(null as String?))
                        }
                        put("keywords/${Keywords.categoryKeyword(category)}", true)
                    },
                ),
            )
            delivered
        }
    }

    /** Walks back from Settings > Categories to the message list. */
    private fun backToInbox() {
        if (compose.onAllNodesWithTag("categories-back").fetchSemanticsNodes().isNotEmpty()) {
            compose.onNodeWithTag("categories-back").performClick()
            compose.waitUntil(TIMEOUT_MS) {
                compose.onAllNodesWithTag("settings-back").fetchSemanticsNodes().isNotEmpty()
            }
        }
        if (compose.onAllNodesWithTag("settings-back").fetchSemanticsNodes().isNotEmpty()) {
            compose.onNodeWithTag("settings-back").performClick()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Opens Settings > Categories from wherever the shell is. */
    private fun openCategories() {
        while (compose.onAllNodesWithTag("inbox-drawer-open").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-settings").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-settings").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("settings-categories").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("settings-screen")
            .performScrollToNode(hasTestTag("settings-categories"))
        compose.onNodeWithTag("settings-categories").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("categories-screen").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /**
     * Strips the `$category-*` keywords from every cached message other
     * than [keep], so the categories under test are the account's whole
     * set.
     */
    private suspend fun clearOtherCategories(
        client: JmapClient,
        accountId: String,
        keep: Set<String>,
    ) {
        val patches = app.container.store.emailList()
            .filter { it.subject !in keep && it.categories.isNotEmpty() }
            .associate { email ->
                email.id to buildJsonObject {
                    email.keywords.filter { Keywords.categoryName(it) != null }.forEach {
                        put("keywords/$it", JsonPrimitive(null as String?))
                    }
                }
            }
        if (patches.isEmpty()) return
        client.emailSet(accountId, patches)
    }

    /** Destroys every label of the account, leaving only the system mailboxes. */
    private suspend fun dropLabels(client: JmapClient, accountId: String) {
        val labels = client.mailboxGet(accountId, null).list.filter { it.role == null }
        if (labels.isEmpty()) return
        client.mailboxSet(accountId, destroy = labels.map { it.id })
    }

    /** The server's own view of a label found by name, polled until it matches. */
    private suspend fun awaitServerLabel(
        client: JmapClient,
        accountId: String,
        name: String,
        matches: (WireMailbox) -> Boolean,
    ): WireMailbox {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        var seen: WireMailbox? = null
        while (System.currentTimeMillis() < deadline) {
            seen = client.mailboxGet(accountId, null).list
                .firstOrNull { it.role == null && it.name.equals(name, ignoreCase = true) }
            if (seen != null && matches(seen)) return seen
            Thread.sleep(POLL_MS)
        }
        error("the server never held $name as asked; it holds ${seen?.disposition}")
    }

    /** The server's own view of one mailbox, polled until it matches. */
    private suspend fun awaitServerMailbox(
        client: JmapClient,
        accountId: String,
        id: String,
        matches: (WireMailbox) -> Boolean,
    ): WireMailbox {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        var seen: WireMailbox? = null
        while (System.currentTimeMillis() < deadline) {
            seen = client.mailboxGet(accountId, listOf(id)).list.firstOrNull()
            if (seen != null && matches(seen)) return seen
            Thread.sleep(POLL_MS)
        }
        error("the server never matched; it holds ${seen?.disposition}/${seen?.priority}")
    }

    /** The entry the drain left failed, with the reason the user reads. */
    private suspend fun awaitOutboxFailure(): com.netzhansa.herold.shared.outbox.OutboxEntry {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            app.container.outbox.list().firstOrNull { it.lastError != null }?.let { return it }
            app.container.session.value!!.drainer.drain()
            Thread.sleep(POLL_MS)
        }
        error("the refusal never reached the outbox: ${app.container.outbox.list()}")
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        val session = app.container.session.value!!
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            session.syncEngine.syncAll()
            app.container.store.emailList().firstOrNull { it.subject == subject }?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the message \"$subject\" never arrived")
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

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 250L
        // herold case-folds keywords, so a category's identity - and the
        // tag of its tab - is the label's lower-cased name.
        const val PIN_FIRST = "catpinned"
        const val PIN_SECOND = "catsecond"
        const val BUNDLED = "catbundled"
        const val FILED = "catfiled"
        const val PLAIN = "catplain"

        // The classifier's own categories, as the fake classifier
        // assigns them from a subject (`+promo`, `+updates`).
        const val DERIVED_PROMOTIONS = "promotions"
        const val DERIVED_UPDATES = "updates"

        /** A pinned category no delivered message carries. */
        const val EMPTY_LANE = "catempty"
    }
}
