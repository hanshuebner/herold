package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.diag.CrashStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.diag.CrashRecord
import kotlinx.coroutines.flow.first
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
 * What the shell does with a restored screen after the app died on one
 * (issue #420). The reporting device crashed on a thread, restored that
 * thread on the restart and died again two seconds later; the shell now
 * opens the inbox instead, once, and restores normally after that.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class CrashRestoreAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private val crashes by lazy { CrashStore(app) }

    @Before
    fun signedIn() {
        grantNotificationPermission()
        crashes.clear()
        runBlocking {
            if (app.container.session.value == null) {
                val result = app.container.signInWithPassword(
                    DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
                )
                assertTrue("sign-in failed: $result", result is SignInResult.Success)
            }
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @After
    fun dropTheRecord() {
        crashes.clear()
        crashes.consumeRestoreBlock()
    }

    @Test
    fun t93_aRestoreAfterACrashOpensTheInbox() {
        val subject = "crash restore " + System.currentTimeMillis()
        DevInstance.deliverMail(subject = subject, body = "A conversation to stand on.")
        val threadId = awaitThread(subject)

        compose.scrollListToThread(threadId)
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }

        // What the uncaught-exception handler leaves behind, written as
        // it writes it.
        crashes.write(
            CrashRecord(
                atMs = System.currentTimeMillis(),
                threadName = "main",
                exception = "android.database.sqlite.SQLiteBlobTooBigException",
                message = "Row too big to fit into CursorWindow",
                stack = "android.database.sqlite.SQLiteBlobTooBigException: Row too big\n\tat test",
                route = "thread/{accountId}/{threadId}",
            ),
        )

        compose.activityRule.scenario.recreate()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-list").assertIsDisplayed()
        assertTrue(
            "the thread the app died on must not come back",
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isEmpty(),
        )
        compose.captureScreen("93-inbox-after-a-crash")

        // The trace itself stays for the next bug report; only the
        // marker is spent, so the next restore is a normal one.
        assertTrue("the trace must wait for a report", crashes.isHeld())
        assertTrue("the marker is spent once", !crashes.consumeRestoreBlock())
    }

    private fun awaitThread(subject: String): String = runBlocking {
        repeat(POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it.threadId }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the inbox")
    }

    private companion object {
        const val TIMEOUT_MS = 60_000L
        const val POLLS = 30
        const val POLL_MS = 500L
    }
}
