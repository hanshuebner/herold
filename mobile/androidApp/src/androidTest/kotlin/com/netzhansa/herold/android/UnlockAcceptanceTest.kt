package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.netzhansa.herold.android.auth.IdlePeriod
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Unlock before anything reaches the network (REQ-AND-AUTH-11), driven
 * against the emulator's device credential. The harness sets a screen
 * lock first:
 *
 *     adb shell locksettings set-pin 1234
 *
 * and clears it afterwards (`locksettings clear --old 1234`). A
 * fingerprint would need an enrolment pass through the Settings UI; the
 * device credential is the fallback the same prompt offers and is what
 * a user without a biometric unlocks with.
 *
 * Two phases, one `am instrument` invocation each:
 *
 *   t80  turns unlock on, backgrounds the app and brings it back: the
 *        lock screen is in front of the mail and the prompt takes the
 *        PIN.
 *   t81  run after `adb shell am kill com.netzhansa.herold.android`:
 *        the fresh process starts locked.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class UnlockAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private val device: UiDevice
        get() = UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())

    @Test
    fun t80_withUnlockOnTheAppLocksWhenItComesBackAndThePromptReleasesIt() {
        grantNotificationPermission()
        // An earlier phase may have left the app locked; this one
        // starts from an unlocked, signed-in shell.
        app.container.unlock.enabled = false
        app.container.unlock.unlocked()
        assertTrue(
            "no screen lock on this device; run: adb shell locksettings set-pin $PIN",
            app.container.unlock.available(),
        )
        signIn()

        app.container.unlock.idlePeriod = IdlePeriod.IMMEDIATELY
        app.container.unlock.enabled = true

        device.pressHome()
        device.waitForIdle()
        launchApp()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("lock-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("lock-screen").assertIsDisplayed()
        compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().let {
            assertTrue("the mail was on screen while the app was locked", it.isEmpty())
        }
        compose.captureComposable("lock-screen", "80-locked-behind-the-prompt")

        enterDeviceCredential()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("81-unlocked-inbox")
        // Left on for the next phase, which kills the process and
        // asserts the fresh one starts locked.
    }

    @Test
    fun t81_aFreshProcessStartsLocked() {
        grantNotificationPermission()
        if (!app.container.unlock.enabled) {
            // The phase before this one left unlock on; without it
            // there is nothing to assert.
            return
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("lock-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("lock-screen").assertIsDisplayed()
        compose.captureComposable("lock-screen", "82-locked-on-launch")
        enterDeviceCredential()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        app.container.unlock.enabled = false
    }

    /** Answers the system prompt with the emulator's PIN. */
    private fun enterDeviceCredential() {
        // The prompt offers the biometric first where one is enrolled;
        // on this emulator it goes straight to the credential screen.
        device.wait(Until.hasObject(By.pkg(SYSTEM_UI)), TIMEOUT_MS)
        device.findObject(By.textContains("PIN"))?.click()
        device.waitForIdle()
        PIN.forEach { digit -> device.pressKeyCode(KEYCODE_0 + digit.digitToInt()) }
        device.pressEnter()
        device.waitForIdle()
    }

    private fun launchApp() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val intent = context.packageManager.getLaunchIntentForPackage(context.packageName)!!
            .addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
        context.startActivity(intent)
        device.waitForIdle()
    }

    private fun signIn() {
        if (app.container.session.value != null) return
        runBlocking {
            val authorizeUrl = app.container.beginSignIn(DevInstance.baseUrl)
            val redirect = OAuthHarness.authorize(
                authorizeUrl = authorizeUrl,
                email = DevInstance.totpEmail,
                password = DevInstance.password,
                totpSecret = DevInstance.totpSecret,
            )
            app.container.completeSignIn(redirect)
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val PIN = "1234"
        const val KEYCODE_0 = 7
        const val SYSTEM_UI = "com.android.systemui"
    }
}
