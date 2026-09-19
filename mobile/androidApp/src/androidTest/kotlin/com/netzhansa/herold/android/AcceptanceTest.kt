package com.netzhansa.herold.android

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performScrollToIndex
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeRight
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
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
 *
 * Each check provisions the mail it reads, so the run stands on any
 * instance and in any order. The category-tab check additionally pins the
 * two lanes it reads, since a tab is a label the server holds at
 * disposition "pinned" (issue #399); the labels it found go back as they
 * were when the class is done (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class AcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private lateinit var labelState: LabelState

    @Before
    fun signedOut() {
        // The app asks for it contextually after the first sync; granted up
        // front the dialog never covers the screen these checks read.
        grantNotificationPermission()
        runBlocking {
            app.container.signOut()
            val client = DevInstance.serverClient()
            labelState = LabelState.take(client, client.session().mailAccountId!!)
        }
        compose.waitUntil(TIMEOUT_MS) { compose.onAllNodesWithTag("signin-submit").fetchSemanticsNodes().isNotEmpty() }
    }

    /**
     * The labels go back as they were, and the session goes with them:
     * the two-factor checks sign in as the TOTP-enrolled admin, and a
     * class that finds that session reads the wrong principal's mailbox
     * (issue #414).
     */
    @After
    fun restoreLabelsAndSession() {
        runBlocking {
            labelState.restore()
            app.container.signOut()
        }
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
        val seeded = provisionCategoryLanes()
        syncNow()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-tabs").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-list").assertIsDisplayed()
        assertTrue(
            "the tab row carries the account's lanes and no combined entry (issue #427)",
            compose.onAllNodesWithTag("inbox-tab-all").fetchSemanticsNodes().isEmpty(),
        )
        assertTrue("expected seeded threads in the inbox", threadRowCount() >= 1)
        compose.captureScreen("04-inbox-with-category-tabs")

        // A tab shows its own lane: the message seeded into it is there
        // and the other lane's is not.
        compose.onNodeWithTag("inbox-tab-$CATEGORY_A").performClick()
        compose.waitForIdle()
        val mine = seeded.getValue(CATEGORY_A).threadId
        val other = seeded.getValue(CATEGORY_B).threadId
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(mine) }
        assertTrue(
            "the $CATEGORY_A tab must not show the $CATEGORY_B lane's conversation",
            compose.listLacksThread(other),
        )
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

        compose.scrollListToThread(newest.threadId)
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

        compose.scrollListToThread(target.threadId)
        compose.onNodeWithTag("thread-swipe-${target.threadId}").performTouchInput { swipeRight() }
        compose.waitUntil(TIMEOUT_MS) { compose.listLacksThread(target.threadId) }
        assertTrue(
            "the server must have the message out of the inbox after the swipe",
            serverPlacesEmail(server, accountId, target.id, inboxId, inMailbox = false),
        )
        compose.onNodeWithTag("inbox-list").performScrollToIndex(0)
        compose.captureScreen("07-swipe-archived-with-undo")

        compose.onNodeWithText("Undo").performClick()
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(target.threadId) }
        compose.onNodeWithTag("thread-row-${target.threadId}").assertIsDisplayed()
        assertTrue(
            "undo must put the message back in the inbox on the server",
            serverPlacesEmail(server, accountId, target.id, inboxId, inMailbox = true),
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

        compose.scrollListToThread(target.threadId)
        // The reported failure was a real finger on a real device, so the
        // gesture goes through the input dispatcher, not the semantics
        // tree (issue #338).
        Gestures.swipeAcrossNode(compose, "thread-swipe-${target.threadId}")

        compose.waitUntil(TIMEOUT_MS) { compose.listLacksThread(target.threadId) }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Undo").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithText("Undo").assertIsDisplayed()
        compose.captureScreen("09-gesture-swipe-shows-undo")

        compose.onNodeWithText("Undo").performClick()
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(target.threadId) }
        val inboxId = app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == MailboxRoles.INBOX }.id
        assertTrue(
            "undo must put the message back in the inbox on the server",
            serverPlacesEmail(server, accountId, target.id, inboxId, inMailbox = true),
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

        compose.scrollListToThread(target.threadId)
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
        compose.waitUntil(TIMEOUT_MS) { compose.listLacksThread(target.threadId) }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Undo").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithText("Undo").assertIsDisplayed()
        assertTrue(
            "the server must have the message out of the inbox after the archive",
            serverPlacesEmail(server, accountId, target.id, inboxId, inMailbox = false),
        )
        compose.captureScreen("09b-thread-archive-undo")

        compose.onNodeWithText("Undo").performClick()
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(target.threadId) }
        compose.onNodeWithTag("thread-row-${target.threadId}").assertIsDisplayed()
        assertTrue(
            "undo must put the message back in the inbox on the server",
            serverPlacesEmail(server, accountId, target.id, inboxId, inMailbox = true),
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

    /**
     * Waits for the server to hold [emailId] in [mailboxId], or to have it
     * out of there when [inMailbox] is false.
     *
     * An archive and its undo write the local store first and run the
     * `Email/set` underneath, so the row moves before the round trip lands
     * (issue #338); reading the server the moment the list changes reads it
     * mid-flight (issue #379).
     */
    private suspend fun serverPlacesEmail(
        server: JmapClient,
        accountId: String,
        emailId: String,
        mailboxId: String,
        inMailbox: Boolean,
    ): Boolean {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (true) {
            val held = DevInstance.serverEmail(server, accountId, emailId)!!.mailboxIds.contains(mailboxId)
            if (held == inMailbox) return true
            if (System.currentTimeMillis() >= deadline) return false
            delay(SERVER_POLL_MS)
        }
    }

    private fun syncNow() {
        runBlocking { app.container.session.value!!.syncEngine.syncAll() }
        compose.waitForIdle()
    }

    /**
     * Delivers one message per lane the check asserts on and returns once
     * each carries its category, so the tab row holds those lanes whatever
     * the rest of the inbox already holds (issue #392).
     *
     * The subjects carry the markers the in-tree fake classifier reads
     * (`internal/testfakes/fakeclassify`: "+promo" -> promotions,
     * "+updates" -> updates, anything else -> primary), so the lane comes
     * from the instance's own classification path. On an instance with no
     * classifier the keyword is written afterwards with the same
     * `Email/set` on `$category-*` the suite's picker fires (REQ-CAT-20),
     * clearing whatever category the message carries: a message shows in
     * the lane of its highest-priority category, so a second one would
     * shadow the lane under test.
     */
    private fun provisionCategoryLanes(): Map<String, com.netzhansa.herold.shared.domain.Email> = runBlocking {
        val session = app.container.session.value!!
        // The lanes are the server's: a category is a tab because its
        // label carries disposition "pinned" (issue #399). The two lanes
        // this check reads are pinned here, through the same Mailbox/set
        // the settings screen writes.
        val accountId = session.client.session().mailAccountId!!
        // The server pins at most five lanes, so the budget is cleared
        // before the two this check reads are asked for (issue #414).
        labelState.unpinAll(keep = setOf(CATEGORY_A, CATEGORY_B))
        val labels = session.client.mailboxGet(accountId, null).list
        listOf(CATEGORY_A, CATEGORY_B).forEachIndexed { rank, category ->
            val existing = labels.firstOrNull { it.role == null && it.name.equals(category, true) }
            val outcome = if (existing == null) {
                session.client.mailboxSet(
                    accountId,
                    create = mapOf(
                        category to buildJsonObject {
                            put("name", category)
                            put("disposition", "pinned")
                            put("priority", rank)
                        },
                    ),
                )
            } else {
                session.client.mailboxSet(
                    accountId,
                    update = mapOf(
                        existing.id to buildJsonObject {
                            put("disposition", "pinned")
                            put("priority", rank)
                        },
                    ),
                )
            }
            // A refused pin leaves the label at "none" and the tab
            // absent, which reads on the screen as a missing lane rather
            // than as the refusal it is.
            check(outcome.errorMessages.isEmpty()) {
                "pinning the $category lane failed: ${outcome.errorMessages}"
            }
        }
        listOf(CATEGORY_A to PROMOTIONS_MARKER, CATEGORY_B to UPDATES_MARKER).associate { (category, marker) ->
            val subject = "acceptance $category lane $marker ${System.nanoTime()}"
            DevInstance.deliverMail(subject = subject, body = "One message for the $category lane.")
            var email = awaitInbox(subject)
            // Filing and classification are separate passes, so the
            // keyword lands a moment after the message answers a query.
            val deadline = System.currentTimeMillis() + TIMEOUT_MS
            while (email.category != category && System.currentTimeMillis() < deadline) {
                delay(SERVER_POLL_MS)
                session.syncEngine.syncAll()
                email = app.container.store.inboxEmails().first().first { it.id == email.id }
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
                email = app.container.store.inboxEmails().first().first { it.id == email.id }
            }
            assertEquals(
                "the $category lane needs a message carrying its keyword",
                category,
                email.category,
            )
            category to email
        }
    }

    private fun threadRowCount(): Int =
        compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
            .fetchSemanticsNodes().size

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val SERVER_POLL_MS = 250L
        // herold case-folds keywords, so the lane's identity - and the tab's
        // test tag - is the lower-cased name.
        const val CATEGORY_A = "promotions"
        const val CATEGORY_B = "updates"
        // The subject markers the fake classifier maps to those lanes.
        const val PROMOTIONS_MARKER = "+promo"
        const val UPDATES_MARKER = "+updates"
    }
}
