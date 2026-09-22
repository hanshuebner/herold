package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.outbox.BugReportPayload
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.outbox.outboxJson
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * What the outbox screen says about a report the server could not take
 * (issue #420). The entry is not failed and not lost: it is listed as
 * waiting for a server that can take it, with the reason the server
 * gave, and the retry that sends it at once is there for a user who
 * knows the server is ready.
 *
 * The entry is written into the local store directly, because the
 * condition it stands for is a server one release behind the phone,
 * which an ephemeral instance built from this tree cannot be.
 */
@RunWith(AndroidJUnit4::class)
class OutboxDeferredEntryAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun ready() {
        grantNotificationPermission()
    }

    @Test
    fun aReportWaitingForTheServerIsListedAsWaitingAndOffersARetry() = runBlocking {
        app.signInAsDevInstancePrincipal()
        compose.waitUntil(TIMEOUT_MS) { app.container.session.value != null }
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { app.container.store.accountList().any { it.isPrimary } }
        }
        val accountId = SeededRows.accountId(app)

        val id = app.container.outbox.enqueueBugReport(
            label = "Bug report: the app closed when a new email arrived",
            payload = BugReportPayload(accountId = accountId, title = "the app closed"),
        )
        app.container.store.updateOutboxState(
            id = id,
            state = OutboxState.DEFERRED,
            attempts = 1,
            lastError = "the bug report was refused: unexpected part \"crash.txt\"",
            permanent = false,
            nextAttemptAt = System.currentTimeMillis() + 15 * 60_000,
        )

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-drawer-open").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-outbox").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-outbox").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("outbox-entry-$id").fetchSemanticsNodes().isNotEmpty()
        }

        compose.onNodeWithTag("outbox-state-$id", useUnmergedTree = true).assertIsDisplayed()
        compose.onNodeWithTag("outbox-retry-$id").assertIsDisplayed()
        compose.captureScreen("95-outbox-entry-waiting-for-the-server")

        val listed = app.container.outbox.list().single { it.id == id }
        assertEquals(OutboxState.DEFERRED, listed.state)
        assertTrue("a waiting entry is still pending", listed.isPending)
        assertTrue(
            "the payload is intact: ${listed.payload}",
            runCatching { outboxJson.decodeFromString<BugReportPayload>(listed.payload) }.isSuccess,
        )

        app.container.outbox.remove(id)
        Unit
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
