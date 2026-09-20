package com.netzhansa.herold.android

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * A lane that goes while the reader stands on it (issue #447):
 *
 *   am instrument ... -e class com.netzhansa.herold.android.InboxLaneShrinkAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ...
 *
 * Archiving the last conversation of a category empties that category
 * out of the inbox, so its tab goes with it. The reported crash was in
 * that frame: the tab row held a selection from the row of two and the
 * measured positions of the row of one, and indexed past the end of
 * them. The check drives a two-lane inbox down to one lane with the
 * second lane selected, and reads the inbox afterwards.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class InboxLaneShrinkAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private lateinit var labelState: LabelState

    /**
     * The lanes under test are the classifier's own, so the class takes
     * the account's labels as it found them and leaves the pinned budget
     * to the categories the mail carries (issue #414).
     */
    @Before
    fun laneStateIsTheClassifiers(): Unit = runBlocking {
        val client = DevInstance.serverClient()
        labelState = LabelState.take(client, client.session().mailAccountId!!)
        labelState.dropLabels(setOf(PRIMARY, UPDATES))
        labelState.unpinAll()
    }

    @After
    fun restoreLabels(): Unit = runBlocking { labelState.restore() }

    @Test
    fun t45_archivingALanesLastConversationLeavesTheInboxOnARemainingLane(): Unit = runBlocking {
        signInAndSync()

        // Two lanes, and one conversation in the second: the category
        // the mail carries is what gives it its tab, so archiving that
        // conversation is what takes the tab away.
        deliverInto(PRIMARY, "lane shrink primary ${System.nanoTime()}")
        val last = deliverInto(UPDATES, "+updates lane shrink ${System.nanoTime()}")
        emptyLane(UPDATES, keep = last.id)
        awaitTab(UPDATES)

        // The reader stands on that lane.
        compose.onNodeWithTag("inbox-tab-$UPDATES").performClick()
        compose.waitUntil(TIMEOUT_MS) { selectedTab() == UPDATES }
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(last.threadId) }

        // ... and archives its only conversation with a finger.
        Gestures.swipeAcrossNode(compose, "thread-swipe-${last.threadId}")

        // The lane goes with the conversation, and the inbox is still
        // up, standing on a lane the row holds.
        compose.waitUntil(TIMEOUT_MS) { UPDATES !in tabs() }
        compose.onNodeWithTag("inbox-list").assertIsDisplayed()
        val settled = selectedTab()
        assertTrue(
            "the inbox settled on $settled, which the row ${tabs()} does not hold",
            settled != null && settled in tabs(),
        )
        assertEquals("the inbox settles on the lane it opens on", PRIMARY, settled)
        compose.captureScreen("m4-lane-shrink-settles-on-primary")

        // The tab is derived state: what the lane held is in the archive,
        // still carrying its category, so nothing durable went with it.
        val archived = app.container.store.emailList().first { it.id == last.id }
        assertTrue(
            "the archived conversation lost its category: ${archived.categories}",
            UPDATES in archived.categories,
        )

        // The lane the reader picked is still their pick, so mail putting
        // it back puts the selection back on it in the frame the tab
        // appears - the row of one measured, the row of two selected.
        deliverInto(UPDATES, "+updates lane back ${System.nanoTime()}")
        awaitTab(UPDATES)
        compose.onNodeWithTag("inbox-list").assertIsDisplayed()
        compose.captureScreen("m4-lane-shrink-and-back")
    }

    // ---- helpers ---------------------------------------------------------

    /** The categories the tab row carries, in the order it shows them. */
    private fun tabs(): List<String> =
        compose.onAllNodes(hasTestTagStartingWith(TAB_TAG))
            .fetchSemanticsNodes()
            .mapNotNull { node ->
                val tag = node.config.getOrNull(SemanticsProperties.TestTag) ?: return@mapNotNull null
                if (tag.startsWith(BADGE_TAG)) null else tag.removePrefix(TAB_TAG)
            }

    /** The category whose tab the row marks selected. */
    private fun selectedTab(): String? =
        compose.onAllNodes(hasTestTagStartingWith(TAB_TAG))
            .fetchSemanticsNodes()
            .firstOrNull { it.config.getOrNull(SemanticsProperties.Selected) == true }
            ?.config?.getOrNull(SemanticsProperties.TestTag)
            ?.removePrefix(TAB_TAG)

    private fun awaitTab(category: String) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tab-$category").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /**
     * A message delivered into [category]: the instance's classifier
     * gives a plain subject the `primary` category and one carrying
     * "+updates" the `updates` one, and the check sets the keyword
     * itself when the pass has other ideas.
     */
    private suspend fun deliverInto(category: String, subject: String): Email {
        DevInstance.deliverMail(subject = subject, body = "A message for the $category lane.")
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
        return email
    }

    /**
     * Archives every inbox conversation of [category] apart from [keep],
     * so the lane holds the one conversation this check archives: mail
     * an earlier class delivered into the same lane would otherwise keep
     * the tab alive past the swipe.
     */
    private suspend fun emptyLane(category: String, keep: String) {
        val others = app.container.store.inboxEmails().first()
            .filter { it.id != keep && category in it.categories }
        if (others.isEmpty()) return
        val session = app.container.session.value!!
        session.actions.archive(others, app.container.store.mailboxList())
        session.syncEngine.syncAll()
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
        app.signInAsDevInstancePrincipal()
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

        /** The fake classifier's lane for a subject carrying "+updates". */
        const val UPDATES = "updates"
    }
}
