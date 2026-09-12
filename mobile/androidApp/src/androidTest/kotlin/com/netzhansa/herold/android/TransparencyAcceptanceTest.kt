package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The LLM-transparency surfaces (issue #361, suite G7,
 * REQ-FILT-65..68): the settings page reporting what the instance says
 * about its prompts and models, and "Why is this here?" on a message.
 *
 * The per-message half asserts the rendering of whatever the instance's
 * classification records hold: an ephemeral dev instance runs with no
 * classifier plugin, so the sheet states that the message was not
 * classified, which is the same code path a classified message takes with
 * the sub-objects filled.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class TransparencyAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    @Test
    fun t10_theTransparencyPageReportsTheInstancesPromptsAndCategories(): Unit = runBlocking {
        signInAndSync()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val server = client.llmTransparency(accountId)
        assertNotNull("the instance does not advertise llm-transparency", server)

        openSettings()
        compose.onNodeWithTag("settings-screen").performScrollToNode(hasTestTag("settings-transparency"))
        compose.onNodeWithTag("settings-transparency").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("transparency-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("transparency-disclosure").assertExists()
        compose.onNodeWithTag("transparency-categories").assertExists()
        compose.onNodeWithTag("transparency-category-prompt").assertExists()
        compose.captureScreen("m3a-transparency")
    }

    @Test
    fun t20_whyIsThisHereShowsWhatTheClassifierRecorded(): Unit = runBlocking {
        signInAndSync()
        val subject = "Classified ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            body = "A message to inspect.",
        )
        val message = awaitInbox(subject)
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val records = client.llmInspect(accountId, listOf(message.id))

        openThread(message.threadId)
        compose.onNodeWithTag("thread-overflow").performClick()
        compose.onNodeWithTag("thread-why").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("llm-inspect-none").fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithTag("llm-category-assigned").fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithTag("llm-spam-verdict").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m3a-llm-inspect")
        if (records.isEmpty()) {
            compose.onNodeWithTag("llm-inspect-none").assertExists()
        } else {
            assertTrue(
                "the sheet showed nothing for a message the server has a record for",
                compose.onAllNodesWithTag("llm-category-assigned").fetchSemanticsNodes().isNotEmpty() ||
                    compose.onAllNodesWithTag("llm-spam-verdict").fetchSemanticsNodes().isNotEmpty(),
            )
        }
    }

    // ---- helpers ---------------------------------------------------------

    private fun signInAndSync() = runBlocking {
        grantNotificationPermission()
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

    private fun openSettings() {
        backToInbox()
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-settings").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-settings").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("settings-screen").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun backToInbox() {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
    }

    private fun openThread(threadId: String) {
        backToInbox()
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.emailList().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the store")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
    }
}
