package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performImeAction
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.search.SearchScope
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The milestone 1c search acceptance (issue #329), in two phases the
 * harness runs around an emulator connectivity toggle:
 *
 *   am instrument ... -e class SearchAcceptanceTest#t30_searchOnlineFindsTheSeededSubject
 *   adb shell svc data disable && adb shell svc wifi disable
 *   am instrument ... -e class SearchAcceptanceTest#t31_searchOfflineFallsBackToTheCache
 *
 * Phase one is `Email/query` with the suite's filter and
 * `SearchSnippet/get` highlights; phase two is the local store, labelled
 * as cached only (REQ-AND-SYNC-13).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class SearchAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
    }

    @Test
    fun t30_searchOnlineFindsTheSeededSubject() = runBlocking {
        signInAndSync()
        val seeded = app.container.store.inboxEmails().first()
            .firstOrNull { it.subject.startsWith(SEED_PREFIX) }
            ?: error("the dev instance holds no \"$SEED_PREFIX ...\" message to search for")

        openSearch()
        compose.onNodeWithTag("search-field").performTextInput(QUERY)
        compose.onNodeWithTag("search-field").performImeAction()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("search-row-${seeded.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("search-row-${seeded.threadId}").assertIsDisplayed()
        compose.onAllNodesWithTag("search-cached-banner").assertCountEquals(0)
        compose.captureScreen("30-search-results")

        // The rows the screen rendered came from the server, with the
        // matched term marked up by SearchSnippet/get (REQ-SRC-30).
        val session = app.container.session.value!!
        val results = session.search.search(
            QUERY,
            app.container.store.accountList(),
            app.container.store.mailboxList(),
            null,
        )
        assertEquals(SearchScope.SERVER, results.scope)
        val hit = results.hits.firstOrNull { it.row.threadId == seeded.threadId }
            ?: error("the server's results do not hold the seeded thread")
        assertTrue(
            "the snippet must mark the matched term, saw ${hit.subjectSnippet} / ${hit.previewSnippet}",
            (hit.subjectSnippet.orEmpty() + hit.previewSnippet.orEmpty()).contains("<mark>$QUERY</mark>"),
        )

        // Tapping a result opens its thread (REQ-SRC-05).
        compose.onNodeWithTag("search-row-${seeded.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("31-search-result-opens-the-thread")
    }

    @Test
    fun t30b_backFromAResultRestoresTheSearchScreen() = runBlocking {
        signInAndSync()
        val seeded = app.container.store.inboxEmails().first()
            .firstOrNull { it.subject.startsWith(SEED_PREFIX) }
            ?: error("the dev instance holds no \"$SEED_PREFIX ...\" message to search for")

        openSearch()
        compose.onNodeWithTag("search-field").performTextInput(QUERY)
        compose.onNodeWithTag("search-field").performImeAction()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("search-row-${seeded.threadId}").fetchSemanticsNodes().isNotEmpty()
        }

        compose.onNodeWithTag("search-row-${seeded.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }

        // The report was a back swipe, so the check swipes too.
        Gestures.swipeBack(compose)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("search-field").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("search-row-${seeded.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithText(QUERY).assertIsDisplayed()
        compose.onNodeWithTag("search-row-${seeded.threadId}").assertIsDisplayed()
        compose.captureScreen("33-back-restores-the-search")
    }

    @Test
    fun t30c_aResultOutsideTheSyncedSetOpensTheWholeThread() = runBlocking {
        signInAndSync()
        val seeded = app.container.store.inboxEmails().first()
            .lastOrNull { it.subject.startsWith(SEED_PREFIX) }
            ?: error("the dev instance holds no \"$SEED_PREFIX ...\" message to search for")

        // Take the thread back out of the store, which is the state the
        // initial inbox fill leaves for mail older than its window or in
        // another mailbox: the server has it, the device does not
        // (issue #339). Nothing changed server-side, so no reconcile pass
        // puts it back.
        app.container.store.deleteEmails(seeded.accountId, listOf(seeded.id))
        app.container.store.deleteThreads(seeded.accountId, listOf(seeded.threadId))
        assertTrue(
            "the thread must be out of the store before the search",
            app.container.store.threadEmailList(seeded.accountId, seeded.threadId).isEmpty(),
        )

        openSearch()
        compose.onNodeWithTag("search-field").performTextInput(QUERY)
        compose.onNodeWithTag("search-field").performImeAction()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("search-row-${seeded.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("search-row-${seeded.threadId}").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-${seeded.id}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-title").assertTextEquals(seeded.subject)
        assertTrue(
            "the fetched thread must be cached like any synced one",
            app.container.store.threadEmailList(seeded.accountId, seeded.threadId).isNotEmpty(),
        )
        compose.captureScreen("34-search-result-outside-the-synced-set")
    }

    @Test
    fun t31_searchOfflineFallsBackToTheCache() = runBlocking {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        val cached = app.container.store.inboxEmails().first()
            .firstOrNull { it.subject.startsWith(SEED_PREFIX) }
            ?: error("phase one must run online first")

        openSearch()
        compose.onNodeWithTag("search-field").performTextInput(QUERY)
        compose.onNodeWithTag("search-field").performImeAction()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("search-cached-banner").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("search-cached-banner").assertIsDisplayed()
        compose.onNodeWithTag("search-row-${cached.threadId}").assertIsDisplayed()
        compose.captureScreen("32-search-cached-only")
    }

    // ---- helpers -------------------------------------------------------

    private fun signInAndSync() = runBlocking {
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

    private fun openSearch() {
        compose.onNodeWithTag("inbox-search").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("search-field").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun androidx.compose.ui.test.SemanticsNodeInteractionCollection.assertCountEquals(count: Int) {
        assertEquals(count, fetchSemanticsNodes().size)
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L

        /** The dev instance's seeded mail, delivered by the acceptance harness. */
        const val SEED_PREFIX = "seed message"
        const val QUERY = "seed"
    }
}
