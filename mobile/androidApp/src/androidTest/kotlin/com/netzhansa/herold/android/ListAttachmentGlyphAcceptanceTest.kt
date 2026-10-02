package com.netzhansa.herold.android

import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performImeAction
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The attachment glyph a message-list row carries (issue #504, suite
 * `REQ-UI-10`). A reader spots mail with files from the list, without
 * opening each conversation.
 *
 * The two conversations are written straight into the local store,
 * which is what every screen renders from (REQ-AND-SYNC-03), so the
 * check needs no delivered mail: one thread whose second message
 * carries an attachment, one whose messages carry none.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class ListAttachmentGlyphAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedIn() {
        grantNotificationPermission()
        runBlocking {
            app.signInAsDevInstancePrincipal()
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.awaitTag("inbox-list")
    }

    @After
    fun radiosBack() {
        shellOut("svc wifi enable")
        shellOut("svc data enable")
    }

    @Test
    fun t10_anInboxRowStatesThatItsConversationHoldsAFile() = runBlocking<Unit> {
        seed()
        compose.awaitTag("thread-row-$WITH_THREAD")
        compose.scrollListToThread(WITH_THREAD)
        awaitGlyph("thread-attachment-$WITH_THREAD")
        compose.captureScreen("504-row-glyph")

        compose.scrollListToThread(WITHOUT_THREAD)
        compose.waitForIdle()
        assertTrue(
            "the row of a conversation holding no file must carry no glyph",
            glyphs("thread-attachment-$WITHOUT_THREAD").isEmpty(),
        )
    }

    @Test
    fun t20_aSearchResultStatesItTheSameWay() = runBlocking<Unit> {
        seed()

        // The seeded conversations are the client's own, so the results
        // holding them are the cached ones the field answers with when
        // the server cannot be reached (REQ-AND-SYNC-13).
        shellOut("svc wifi disable")
        shellOut("svc data disable")
        awaitOffline(compose)

        compose.onNodeWithTag("inbox-search").performClick()
        compose.awaitTag("search-field")
        compose.onNodeWithTag("search-field").performTextInput(TOKEN)
        compose.onNodeWithTag("search-field").performImeAction()
        compose.awaitTag("search-row-$WITH_THREAD")
        awaitGlyph("search-attachment-$WITH_THREAD")
        compose.captureScreen("504-search-glyph")
        assertTrue(
            "a result whose conversation holds no file must carry no glyph",
            glyphs("search-attachment-$WITHOUT_THREAD").isEmpty(),
        )
    }

    // ---- helpers ---------------------------------------------------------

    /**
     * The glyph's nodes, read from the unmerged tree: a row is one
     * clickable target, so the row's own semantics take in those of
     * everything drawn inside it.
     */
    private fun glyphs(tag: String) =
        compose.onAllNodesWithTag(tag, useUnmergedTree = true).fetchSemanticsNodes()

    private fun awaitGlyph(tag: String) {
        compose.waitUntil(GLYPH_TIMEOUT_MS) { glyphs(tag).isNotEmpty() }
    }

    /**
     * The two conversations, newest first in the list. The attachment
     * sits on the older message of its thread, so the row states what
     * any of its members holds rather than only its newest.
     */
    private suspend fun seed() {
        val accountId = SeededRows.accountId(app)
        val inbox = SeededRows.mailboxId(app, accountId, MailboxRoles.INBOX)
        val now = System.currentTimeMillis()
        fun row(id: String, threadId: String, at: Long, hasAttachment: Boolean) = Email(
            accountId = accountId,
            id = id,
            threadId = threadId,
            fromName = "Seed $id",
            fromEmail = "seed@acceptance.test",
            toLine = DevInstance.email,
            subject = "$TOKEN $id",
            preview = "Seeded for the attachment glyph.",
            receivedAt = at,
            hasAttachment = hasAttachment,
            keywords = setOf(Keywords.SEEN),
            mailboxIds = setOf(inbox),
            bodyText = "Seeded for the attachment glyph.",
        )
        app.container.store.upsertEmails(
            listOf(
                row("$TOKEN-with-older", WITH_THREAD, now - 60_000, hasAttachment = true),
                row("$TOKEN-with-newer", WITH_THREAD, now, hasAttachment = false),
                row("$TOKEN-without", WITHOUT_THREAD, now - 30_000, hasAttachment = false),
            ),
        )
    }

    private companion object {
        /** How long a glyph has to appear before the wait gives up. */
        const val GLYPH_TIMEOUT_MS = 30_000L

        const val TOKEN = "Glyph504"
        const val WITH_THREAD = "$TOKEN-thread-with"
        const val WITHOUT_THREAD = "$TOKEN-thread-without"
    }
}
