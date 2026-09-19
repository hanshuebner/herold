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
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.search.SearchScope
import kotlinx.coroutines.delay
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
 *
 * The online phases deliver the mail they search for, the way the push
 * and compose suites provision theirs, so a run against a fresh instance
 * stands on its own (issue #393).
 *
 * The back the restoration check pops the thread on is the back key
 * (`Gestures.pressBack`). Its subject is what the search screen holds
 * after the pop - the query and the results it had - and the key and the
 * edge gesture reach the same `OnBackInvokedCallback`, so the pop under
 * test is the same one; the key event is delivered whatever the host's
 * load, where the edge gesture is committed by a detector reading the
 * pointer's velocity.
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
        val seeded = seededMail().first()

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
        val seeded = seededMail().first()

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

        Gestures.pressBack(compose)
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
        // The oldest seed, so the conversation this check takes out of the
        // store is not the one the checks before it opened.
        val seeded = seededMail().last()

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

    /**
     * The seed mail the online checks search for, in the store and in the
     * server's index, newest first.
     *
     * The suite delivers what it is missing over the instance's SMTP
     * listener, so a fresh instance carries the mail these checks need
     * (issue #393). [SEED_COUNT] messages, because t30c takes the oldest
     * out of the store and the cached phase reads one that is still there.
     */
    private suspend fun seededMail(): List<Email> {
        signInAndSync()
        val held = seedRows()
        if (held.size >= SEED_COUNT) return held
        val delivered = (held.size until SEED_COUNT).map {
            DevInstance.deliverMail(
                subject = "$SEED_PREFIX ${System.nanoTime()}",
                body = "A seed message for the search acceptance run.",
            )
        }
        delivered.forEach { DevInstance.awaitFiled(it) }
        awaitIndexed(delivered)
        repeat(SEED_POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            val rows = seedRows()
            if (delivered.all { subject -> rows.any { it.subject == subject } }) return rows
            delay(SEED_POLL_MS)
        }
        error("the seeded messages never reached the store")
    }

    /** The store's seed messages, in the order the inbox holds them. */
    private suspend fun seedRows(): List<Email> =
        app.container.store.inboxEmails().first().filter { it.subject.startsWith(SEED_PREFIX) }

    /**
     * Waits until the server's own search answers with [subjects].
     *
     * A delivered message is filed before it is indexed, so a screen
     * driven at the SMTP acknowledgement searches an index that does not
     * hold the seed yet: the search runs once per submitted query, and the
     * wait for its result row would be measuring the indexing pass.
     */
    private suspend fun awaitIndexed(subjects: List<String>) {
        val search = app.container.session.value!!.search
        repeat(SEED_POLLS) {
            val results = search.search(
                QUERY,
                app.container.store.accountList(),
                app.container.store.mailboxList(),
                null,
            )
            if (subjects.all { subject -> results.hits.any { it.row.subject == subject } }) return
            delay(SEED_POLL_MS)
        }
        error("the instance never indexed the seeded messages for \"$QUERY\"")
    }

    private fun signInAndSync() = runBlocking {
        app.signInAsDevInstancePrincipal()
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

        /** The mail the searches run against, delivered by the suite itself. */
        const val SEED_PREFIX = "seed message"
        const val QUERY = "seed"
        const val SEED_COUNT = 2

        /** How long a delivery is given to reach the index and the store. */
        const val SEED_POLLS = 60
        const val SEED_POLL_MS = 1_000L
    }
}
