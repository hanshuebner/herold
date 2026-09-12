package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.push.PushTransportChoice
import com.netzhansa.herold.shared.auth.SignInResult
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The transport is the user's to see and to change (REQ-AND-PUSH-05):
 * settings says which one carries push on this device and offers the
 * three choices, and picking one takes effect.
 */
@RunWith(AndroidJUnit4::class)
class PushTransportSettingsTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedIn() {
        grantNotificationPermission()
        runBlocking {
            if (app.container.session.value == null) {
                val result = app.container.signInWithPassword(
                    DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
                )
                assertTrue("sign-in failed: $result", result is SignInResult.Success)
            }
            app.container.session.value!!.syncEngine.syncAll()
        }
        app.container.push.choice = PushTransportChoice.AUTOMATIC
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @Test
    fun t94_settingsShowsTheTransportAndTakesAChange() {
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-settings").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-settings").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("push-transport-summary").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("settings-screen")
            .performScrollToNode(hasTestTag("push-transport-unifiedpush"))
        compose.captureScreen("94-push-transport-setting")

        compose.onNodeWithTag("push-transport-unifiedpush").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            app.container.push.choice == PushTransportChoice.UNIFIED_PUSH
        }
        assertEquals(PushTransportChoice.UNIFIED_PUSH, app.container.push.choice)
        compose.captureScreen("95-push-transport-unifiedpush")

        // Back to what the device decides, so a later run starts clean.
        compose.onNodeWithTag("push-transport-automatic").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            app.container.push.choice == PushTransportChoice.AUTOMATIC
        }
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
