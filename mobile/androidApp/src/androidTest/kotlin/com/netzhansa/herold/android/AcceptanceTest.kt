package com.netzhansa.herold.android

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performScrollToIndex
import androidx.compose.ui.test.performScrollToNode
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
        // The app asks for it contextually after the first sync; granted up
        // front the dialog never covers the screen these checks read.
        grantNotificationPermission()
        runBlocking { app.container.signOut() }
        compose.waitUntil(TIMEOUT_MS) { compose.onAllNodesWithTag("signin-submit").fetchSemanticsNodes().isNotEmpty() }
    }

    @Test
    fun t01_signInWithTheDeviceTokenGrantAndKeepTheTokenAcrossRestart() {
        openPasswordFallback()
        compose.onNodeWithTag("signin-email").performTextInput(DevInstance.email)
        compose.onNodeWithTag("signin-password").performTextInput(DevInstance.password)
        compose.captureScreen("01-sign-in")
        compose.onNodeWithTag("signin-password-submit").performClick()

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
        openPasswordFallback()
        compose.onNodeWithTag("signin-email").performTextInput(DevInstance.totpEmail)
        compose.onNodeWithTag("signin-password").performTextInput(DevInstance.password)
        // The principal has TOTP enrolled, so the first submission comes
        // back step_up_required and the six-digit field appears
        // (REQ-AND-AUTH-20).
        compose.onNodeWithTag("signin-password-submit").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-totp").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("signin-totp").performTextInput("000000")
        compose.onNodeWithTag("signin-password-submit").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-password-error").fetchSemanticsNodes().isNotEmpty()
        }
        // The form scrolls once the code field is in it, so the
        // message can sit below the fold.
        compose.onNodeWithTag("signin-password-error").assertExists()
        compose.onAllNodesWithTag("inbox-list").assertCountEquals(0)
        compose.captureScreen("03-wrong-totp-rejected")
    }

    @Test
    fun t02b_aCorrectTotpCodeIsAccepted() {
        val secret = DevInstance.totpSecret
        if (secret == null) {
            // The harness passes the dev instance's printed ADMIN_TOTP_SECRET;
            // without it this check cannot run.
            return
        }
        openPasswordFallback()
        compose.onNodeWithTag("signin-email").performTextInput(DevInstance.totpEmail)
        compose.onNodeWithTag("signin-password").performTextInput(DevInstance.password)
        compose.onNodeWithTag("signin-password-submit").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-totp").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("signin-totp").performTextInput(Totp.code(secret))
        compose.onNodeWithTag("signin-password-submit").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-title").assertIsDisplayed()
        compose.captureScreen("03b-correct-totp-accepted")
    }

    /** Reveals the debug build's device-token form under the Custom Tab button. */
    private fun openPasswordFallback() {
        compose.onNodeWithTag("signin-base-url").performTextClearance()
        compose.onNodeWithTag("signin-base-url").performTextInput(DevInstance.baseUrl)
        compose.onNodeWithTag("signin-password-toggle").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-email").fetchSemanticsNodes().isNotEmpty()
        }
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
    fun t04_openingAThreadRendersItsHtmlBodyAndItsInlineImage() = runBlocking {
        signIn()
        // The check seeds the message it opens, so it does not depend on
        // which of the instance's mail happens to carry an inline image.
        val subject = "inline image ${System.currentTimeMillis()}"
        DevInstance.deliverMailWithInlineImage(subject)
        val newest = awaitInbox(subject)
        compose.waitUntil(TIMEOUT_MS) { threadRowCount() > 0 }

        scrollInboxToThread(newest.threadId)
        compose.onNodeWithTag("thread-row-${newest.threadId}").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(isRenderedMessageBody(), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }

        val opened = app.container.store.email(newest.accountId, newest.id)!!
        assertNotNull("the opened message must have a cached body", opened.bodyHtml ?: opened.bodyText)

        // The inline image is fetched through the JMAP blob endpoint and
        // lands in the blob cache, which is what makes it render offline.
        val inline = opened.attachments.firstOrNull { it.isInline }
        assertNotNull("the seeded message must carry an inline image", inline)
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { app.container.store.cachedBlob(newest.accountId, inline!!.blobId) } != null
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

        scrollInboxToThread(target.threadId)
        compose.onNodeWithTag("thread-swipe-${target.threadId}").performTouchInput { swipeRight() }
        compose.waitUntil(TIMEOUT_MS) { !inboxHoldsThread(target.threadId) }
        assertFalse(
            "the server must have the message out of the inbox after the swipe",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )
        compose.onNodeWithTag("inbox-list").performScrollToIndex(0)
        compose.captureScreen("07-swipe-archived-with-undo")

        compose.onNodeWithText("Undo").performClick()
        compose.waitUntil(TIMEOUT_MS) { inboxHoldsThread(target.threadId) }
        compose.onNodeWithTag("thread-row-${target.threadId}").assertIsDisplayed()
        assertTrue(
            "undo must put the message back in the inbox on the server",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )
        compose.captureScreen("08-undo-restored")
    }

    @Test
    fun t06_aFingerSwipeOffersUndoWhileTheArchiveIsStillInFlight() = runBlocking {
        signIn()
        syncNow()
        compose.waitUntil(TIMEOUT_MS) { threadRowCount() > 0 }

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val target = app.container.store.inboxEmails().first()
            .maxByOrNull { it.receivedAt } ?: error("no inbox message to archive")

        scrollInboxToThread(target.threadId)
        // The reported failure was a real finger on a real device, so the
        // gesture goes through the input dispatcher, not the semantics
        // tree (issue #338).
        Gestures.swipeAcrossNode(compose, "thread-swipe-${target.threadId}")

        compose.waitUntil(TIMEOUT_MS) { !inboxHoldsThread(target.threadId) }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Undo").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithText("Undo").assertIsDisplayed()
        compose.captureScreen("09-gesture-swipe-shows-undo")

        compose.onNodeWithText("Undo").performClick()
        compose.waitUntil(TIMEOUT_MS) { inboxHoldsThread(target.threadId) }
        val inboxId = app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == MailboxRoles.INBOX }.id
        assertTrue(
            "undo must put the message back in the inbox on the server",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )
    }

    @Test
    fun t07_archivingFromTheThreadViewOffersTheUndoOnTheListItReturnsTo() = runBlocking {
        signIn()
        // The check archives a message it delivered itself, so it does not
        // depend on what an earlier test left in the inbox.
        val subject = DevInstance.deliverMail(
            subject = "Thread archive ${System.currentTimeMillis()}",
            body = "Archived from the open conversation.",
        )
        val target = awaitInbox(subject)
        compose.waitUntil(TIMEOUT_MS) { threadRowCount() > 0 }

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val inboxId = app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == MailboxRoles.INBOX }.id

        scrollInboxToThread(target.threadId)
        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }

        // The reported failure: this returned to the list with the thread
        // gone and no way back (issue #345).
        compose.onNodeWithTag("thread-archive").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) { !inboxHoldsThread(target.threadId) }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Undo").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithText("Undo").assertIsDisplayed()
        assertFalse(
            "the server must have the message out of the inbox after the archive",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )
        compose.captureScreen("09b-thread-archive-undo")

        compose.onNodeWithText("Undo").performClick()
        compose.waitUntil(TIMEOUT_MS) { inboxHoldsThread(target.threadId) }
        compose.onNodeWithTag("thread-row-${target.threadId}").assertIsDisplayed()
        assertTrue(
            "undo must put the message back in the inbox on the server",
            DevInstance.serverEmail(server, accountId, target.id)!!.mailboxIds.contains(inboxId),
        )
        compose.captureScreen("09c-thread-archive-undone")
    }

    // ---- helpers -------------------------------------------------------

    private fun signIn() {
        val result = runBlocking {
            app.container.signInWithPassword(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
        }
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Syncs until the message with [subject] is in the local inbox. */
    private fun awaitInbox(subject: String): com.netzhansa.herold.shared.domain.Email = runBlocking {
        repeat(30) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(500)
        }
        error("the seeded message \"$subject\" never reached the inbox")
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

    /**
     * True when the inbox holds a row for [threadId], scrolling the list to
     * it when it sits outside the composed window. A LazyColumn composes
     * only the rows around the viewport, so with an inbox longer than one
     * screen a present row is invisible to a plain tag lookup (issue #335).
     */
    private fun inboxHoldsThread(threadId: String): Boolean {
        if (compose.onAllNodesWithTag("thread-row-$threadId").fetchSemanticsNodes().isNotEmpty()) {
            return true
        }
        return runCatching { scrollInboxToThread(threadId) }.isSuccess
    }

    /** Brings the row for [threadId] into the viewport; fails when the list has none. */
    private fun scrollInboxToThread(threadId: String) {
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
    }

    private fun threadRowCount(): Int =
        compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
            .fetchSemanticsNodes().size

    private companion object {
        const val TIMEOUT_MS = 30_000L
        // herold case-folds keywords, so the lane's identity - and the tab's
        // test tag - is the lower-cased name.
        const val CATEGORY_A = "promotions"
        const val CATEGORY_B = "updates"
    }
}
