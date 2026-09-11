package com.netzhansa.herold.android

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeRight
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The milestone 1a acceptance run (issue #327), driven against an ephemeral
 * herold from `scripts/dev-instance.sh`. Each test is one acceptance bullet
 * and leaves a screenshot behind (see [captureScreen]).
 *
 * The offline bullet lives in [OfflineAcceptanceTest], which the harness
 * runs with the emulator's radios turned off.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class AcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedOut() {
        runBlocking { app.container.signOut() }
        compose.waitUntil(TIMEOUT_MS) { compose.onAllNodesWithTag("signin-submit").fetchSemanticsNodes().isNotEmpty() }
    }

    @Test
    fun t01_signInWithTheDeviceTokenGrantAndKeepTheTokenAcrossRestart() {
        compose.onNodeWithTag("signin-base-url").performTextClearance()
        compose.onNodeWithTag("signin-base-url").performTextInput(DevInstance.baseUrl)
        compose.onNodeWithTag("signin-email").performTextInput(DevInstance.email)
        compose.onNodeWithTag("signin-password").performTextInput(DevInstance.password)
        compose.captureScreen("01-sign-in")
        compose.onNodeWithTag("signin-submit").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-title").assertIsDisplayed()

        // The token is on disk in Keystore-backed storage, so it is there
        // for the next process (REQ-AND-AUTH-10).
        val persisted = runBlocking {
            KeystoreTokenStore(InstrumentationRegistry.getInstrumentation().targetContext).currentToken()
        }
        assertNotNull("no token in Keystore-backed storage after sign-in", persisted)
        assertTrue("expected an hk_ device token", persisted!!.startsWith("hk_"))

        // Recreating the activity must land on the inbox, not the form.
        compose.activityRule.scenario.recreate()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onAllNodesWithTag("signin-submit").assertCountEquals(0)
        compose.captureScreen("02-inbox-after-restart")
    }

    @Test
    fun t02_aWrongTotpCodeIsRejectedWithAMessage() {
        compose.onNodeWithTag("signin-base-url").performTextClearance()
        compose.onNodeWithTag("signin-base-url").performTextInput(DevInstance.baseUrl)
        compose.onNodeWithTag("signin-email").performTextInput(DevInstance.totpEmail)
        compose.onNodeWithTag("signin-password").performTextInput(DevInstance.password)
        compose.onNodeWithTag("signin-totp").performTextInput("000000")
        compose.onNodeWithTag("signin-submit").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-error").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("signin-error").assertIsDisplayed()
        compose.onAllNodesWithTag("inbox-list").assertCountEquals(0)
        compose.captureScreen("03-wrong-totp-rejected")
    }

    @Test
    fun t03_theInboxShowsSeededThreadsWithCategoryTabsThatFilterTheList() {
        signIn()
        categoriseTwoSeededThreads()
        syncNow()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tabs").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-list").assertIsDisplayed()
        val rowsBefore = threadRowCount()
        assertTrue("expected seeded threads in the inbox, found $rowsBefore", rowsBefore >= 2)
        compose.captureScreen("04-inbox-with-category-tabs")

        compose.onNodeWithTag("inbox-tab-$CATEGORY_A").performClick()
        compose.waitForIdle()
        val rowsInTab = threadRowCount()
        assertTrue("a category tab must narrow the stream ($rowsInTab of $rowsBefore)", rowsInTab in 1 until rowsBefore)
        compose.captureScreen("05-category-tab-filters")
    }

    @Test
    fun t04_openingAThreadRendersItsBody() {
        signIn()
        syncNow()
        compose.waitUntil(TIMEOUT_MS) { threadRowCount() > 0 }

        firstThreadRow().performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("message-body-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("06-thread-view")
    }

    @Test
    fun t05_swipeArchiveRemovesTheRowUndoRestoresItAndTheServerAgrees() = runBlocking {
        signIn()
        syncNow()
        compose.waitUntil(TIMEOUT_MS) { threadRowCount() > 0 }

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val target = app.container.store.inboxEmails().first()
            .maxByOrNull { it.receivedAt } ?: error("no inbox message to archive")
        val inboxId = app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == MailboxRoles.INBOX }.id
        assertTrue(
            "the message must start in the inbox",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )

        compose.onNodeWithTag("thread-swipe-${target.threadId}").performTouchInput { swipeRight() }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isEmpty()
        }
        assertFalse(
            "the server must have the message out of the inbox after the swipe",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )
        compose.captureScreen("07-swipe-archived-with-undo")

        compose.onNodeWithText("Undo").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "undo must put the message back in the inbox on the server",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )
        compose.captureScreen("08-undo-restored")
    }

    // ---- helpers -------------------------------------------------------

    private fun signIn() {
        val result = runBlocking {
            app.container.signIn(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun syncNow() {
        runBlocking { app.container.session.value!!.syncEngine.syncAll() }
        compose.waitForIdle()
    }

    /**
     * Assigns a category keyword to the newest inbox thread, the same
     * `Email/set` on `$category-*` the suite's picker fires (REQ-CAT-20),
     * so the tab lane has something to filter on a dev instance with no
     * classifier configured.
     */
    private fun categoriseTwoSeededThreads() = runBlocking {
        val session = app.container.session.value!!
        session.syncEngine.syncAll()
        val inbox = app.container.store.inboxEmails().first().sortedByDescending { it.receivedAt }
        check(inbox.size >= 2) { "the dev instance must hold at least two inbox messages, saw ${inbox.size}" }
        session.client.emailSet(
            inbox.first().accountId,
            mapOf(
                inbox[0].id to buildJsonObject {
                    put("keywords/${Keywords.categoryKeyword(CATEGORY_A)}", true)
                },
                inbox[1].id to buildJsonObject {
                    put("keywords/${Keywords.categoryKeyword(CATEGORY_B)}", true)
                },
            ),
        )
    }

    private fun threadRowCount(): Int =
        compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
            .fetchSemanticsNodes().size

    private fun firstThreadRow() =
        compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true).onFirst()

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val CATEGORY_A = "Promotions"
        const val CATEGORY_B = "Updates"
    }
}
