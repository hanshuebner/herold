package com.netzhansa.herold.android

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertContentDescriptionEquals
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
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
 * The inbox's lanes as the reader meets them (issue #427,
 * REQ-AND-NAV-30..33), driven against an ephemeral herold:
 *
 *   am instrument ... -e class com.netzhansa.herold.android.InboxLaneAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ...
 *
 * The tab row is the account's lanes with no combined entry, the inbox
 * opens on Primary, a lane holding unread mail badges its count, and
 * reading that mail clears the badge without the row moving.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class InboxLaneAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Test
    fun t10_theTabRowIsTheLanesAloneAndTheInboxOpensOnPrimary() {
        signInAndSync()
        awaitTabs()

        assertTrue(
            "the combined tab is gone",
            compose.onAllNodesWithTag("inbox-tab-all").fetchSemanticsNodes().isEmpty(),
        )
        assertTrue(
            "the lanes the mail carries are tabs",
            tabGeometry().keys.contains(PRIMARY),
        )
        compose.onNodeWithTag("inbox-tab-$PRIMARY").assertIsSelected()
        compose.captureScreen("m4-lanes-open-on-primary")
    }

    @Test
    fun t20_anUnreadLaneBadgesItsCountAndReadingItClearsTheBadgeWithoutMovingTheRow(): Unit = runBlocking {
        signInAndSync()
        awaitTabs()

        // One unread conversation in the Promotions lane, and no other:
        // the badge then reads a count the check knows.
        val subject = "+promo lane badge ${System.nanoTime()}"
        DevInstance.deliverMail(subject = subject, body = "A sale in the promotions lane.")
        val delivered = awaitCategorised(subject, PROMOTIONS)
        readEverythingElseIn(PROMOTIONS, keep = delivered.id)

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag(badgeTag(PROMOTIONS), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag(badgeTag(PROMOTIONS), useUnmergedTree = true)
            .assertContentDescriptionEquals("Promotions, 1 unread")
        compose.onNodeWithTag("inbox-tab-$PRIMARY").assertIsSelected()
        compose.captureScreen("m4-lane-badge-on-primary")
        val badged = tabGeometry()

        // Reading the lane's last unread conversation clears its badge.
        compose.onNodeWithTag("inbox-tab-$PROMOTIONS").performClick()
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(delivered.threadId) }
        compose.onNodeWithTag("thread-row-${delivered.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-back").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-tab-$PRIMARY").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag(badgeTag(PROMOTIONS), useUnmergedTree = true)
                .fetchSemanticsNodes().isEmpty()
        }
        compose.captureScreen("m4-lanes-all-read")

        // The badge lives in a slot every tab keeps, so the row the
        // reader was looking at is where it was (issue #421's rule).
        assertEquals("the tab row must not reflow when a badge goes", badged, tabGeometry())
    }

    // ---- helpers ---------------------------------------------------------

    /**
     * Each tab's size and its place within the row, by category: what a
     * badge appearing or going must leave alone. The places are measured
     * from the leading tab, so the row's own scroll offset is not read
     * as a reflow.
     */
    private fun tabGeometry(): Map<String, Triple<Float, Float, Float>> {
        val bounds = compose.onAllNodes(hasTestTagStartingWith(TAB_TAG))
            .fetchSemanticsNodes()
            .mapNotNull { node ->
                val tag = node.config.getOrNull(SemanticsProperties.TestTag) ?: return@mapNotNull null
                if (tag.startsWith(BADGE_TAG)) null else tag.removePrefix(TAB_TAG) to node.boundsInWindow
            }
            .toMap()
        val leading = bounds.values.minOfOrNull { it.left } ?: 0f
        return bounds.mapValues { (_, rect) ->
            Triple(rect.width, rect.height, rect.left - leading)
        }
    }

    private fun badgeTag(category: String) = "$BADGE_TAG$category"

    private fun awaitTabs() {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tabs").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /**
     * The delivered message once it carries [category]. The classifier
     * runs a pass behind delivery, so the check waits for its keyword
     * and sets it itself when the pass has other ideas.
     */
    private fun awaitCategorised(subject: String, category: String): Email = runBlocking {
        val session = app.container.session.value!!
        var email = awaitInbox(subject)
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (email.category != category && System.currentTimeMillis() < deadline) {
            Thread.sleep(POLL_MS)
            session.syncEngine.syncAll()
            email = app.container.store.emailList().first { it.id == email.id }
        }
        if (email.category != category) {
            session.client.emailSet(
                email.accountId,
                mapOf(
                    email.id to buildJsonObject {
                        email.keywords.filter { Keywords.categoryName(it) != null }.forEach {
                            put("keywords/$it", JsonPrimitive(null as String?))
                        }
                        put("keywords/${Keywords.categoryKeyword(category)}", true)
                    },
                ),
            )
            session.syncEngine.syncAll()
            email = app.container.store.emailList().first { it.id == email.id }
        }
        assertEquals("the lane needs a message carrying its keyword", category, email.category)
        email
    }

    /** Marks every unread message of [category] read, apart from [keep]. */
    private suspend fun readEverythingElseIn(category: String, keep: String) {
        val others = app.container.store.emailList()
            .filter { it.id != keep && it.isUnread && category in it.categories }
        if (others.isEmpty()) return
        app.container.session.value!!.actions.setSeen(others, true)
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
        const val TAB_TAG = "inbox-tab-"
        const val BADGE_TAG = "inbox-tab-badge-"

        /** The lane the inbox opens on (REQ-CAT-03). */
        const val PRIMARY = "primary"

        /** The fake classifier's lane for a subject carrying "+promo". */
        const val PROMOTIONS = "promotions"
    }
}
