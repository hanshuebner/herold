package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The scroll offset across a restore (issue #439, REQ-AND-NAV-25,
 * REQ-AND-NAV-20): All Mail comes back at the row it was left on when
 * the shell is rebuilt from its saved state, as the open destination
 * and the selected lane already do.
 *
 * The restore is driven by rebuilding the activity, which is the path a
 * killed process comes back on: `onSaveInstanceState` writes the
 * bundle, the activity is destroyed, and a new one is created from that
 * bundle. A kill between two `am instrument` invocations cannot stand in
 * for it here - the runner force-stops the target package at the end of
 * a run and again at the start of the next ("Force stopping ...:
 * finished inst"), and a force-stop takes the task's saved state with
 * it, so nothing of the shell's state reaches the second invocation.
 */
@RunWith(AndroidJUnit4::class)
class ScrollPositionSurvivesRestoreTest {

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

    @Test
    fun t99_allMailComesBackAtItsRowAfterTheShellIsRebuilt(): Unit = runBlocking {
        val accountId = SeededRows.accountId(app)
        val rows = SeededRows.seed(
            app = app,
            accountId = accountId,
            mailboxId = SeededRows.mailboxId(app, accountId, MailboxRoles.ARCHIVE),
            token = TOKEN,
            count = SEEDED,
        )

        openAllMail()
        compose.awaitTag("thread-row-${rows.first().threadId}")
        val left = compose.scrollPastTheFold("mailbox-list", compose.leadingRow("mailbox-list"))
        compose.captureScreen("111-all-mail-before-the-shell-is-rebuilt")

        compose.activityRule.scenario.recreate()

        compose.awaitTag("mailbox-list")
        assertEquals(
            "the rebuilt shell left All Mail at another row",
            left,
            compose.settledLeadingRow("mailbox-list"),
        )
        compose.captureScreen("112-all-mail-after-the-shell-is-rebuilt")
    }

    private fun openAllMail() {
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.awaitTag("drawer-inbox")
        compose.onNodeWithTag("inbox-drawer").performScrollToNode(hasTestTag(ALL_MAIL))
        compose.onNodeWithTag(ALL_MAIL).performClick()
    }

    private companion object {
        /** The token the rows this check stands on carry. */
        const val TOKEN = "Restored439"
        const val SEEDED = 40
        const val ALL_MAIL = "drawer-folder-${MailboxRoles.ARCHIVE}"
    }
}
