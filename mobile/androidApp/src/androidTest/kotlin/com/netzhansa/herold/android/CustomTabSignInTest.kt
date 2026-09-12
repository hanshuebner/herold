package com.netzhansa.herold.android

import android.content.Intent
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Milestone 2b's sign-in bullet (issue #352): the whole
 * authorization-code flow through the real system browser - the app
 * opens herold's login page in a Custom Tab, the page takes the
 * password and then the six-digit code, and the redirect on the app's
 * private-use scheme brings the shell back with a session
 * (REQ-AND-AUTH-01/02).
 *
 * Driven entirely through UiAutomator rather than a Compose rule: the
 * redirect resumes the shell's activity through a fresh intent, which
 * an ActivityScenario refuses to adopt, and the check is about the
 * whole cross-app round trip rather than a single composition.
 *
 * The emulator's browser must have its first-run screens behind it.
 * The harness does that once before the run (mobile/README.md);
 * [Browser.dismissFirstRun] covers a freshly wiped one.
 */
@RunWith(AndroidJUnit4::class)
class CustomTabSignInTest {

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val device: UiDevice get() = UiDevice.getInstance(instrumentation)

    private val tokenStore: KeystoreTokenStore
        get() = KeystoreTokenStore(instrumentation.targetContext)

    @Test
    fun t70_signInCompletesThroughTheCustomTabIncludingTheTotpStep() {
        val secret = DevInstance.totpSecret ?: return
        grantNotificationPermission()
        runBlocking {
            app.container.unlock.enabled = false
            app.container.signOut()
            // The screen offers the server of the last sign-in; the
            // harness's instance is what this run signs in to.
            tokenStore.setBaseUrl(DevInstance.baseUrl)
        }

        // Something for the list to render once the first sync runs:
        // the TOTP principal's mailbox is otherwise empty.
        DevInstance.deliverMail(
            subject = "seed message for the sign-in bullet",
            body = "The mailbox list needs a row to show.",
            to = DevInstance.totpEmail,
        )

        launchShell()
        // Compose renders the button's label as its own text node, so
        // the shell's control is matched on the app package rather
        // than on a Button class.
        val signIn = device.wait(
            Until.findObject(By.pkg(instrumentation.targetContext.packageName).text("Sign in")),
            TIMEOUT_MS,
        )
        assertNotNull("the sign-in screen never appeared", signIn)
        captureDeviceScreen("70-oauth-sign-in-screen")
        signIn!!.click()

        // The system browser now has the foreground, on herold's own
        // login page.
        Browser.dismissFirstRun(device)
        assertTrue("the browser never showed herold's login page", Browser.awaitFields(device, 2).size >= 2)
        captureDeviceScreen("71-custom-tab-login-page")

        Browser.signIn(device, DevInstance.totpEmail, DevInstance.password)
        // The principal has TOTP enrolled, so the page comes back with
        // the six-digit field added and the form is resent.
        assertTrue("the login page never asked for the code", Browser.awaitTotpField(device))
        captureDeviceScreen("72-custom-tab-totp-step")
        Browser.signIn(device, DevInstance.totpEmail, DevInstance.password, Totp.code(secret))

        // The redirect brought the shell back and the exchange ran.
        val held = awaitTokens()
        assertNotNull("no token after the Custom Tab flow", held)
        assertTrue("expected an hk_ access token", held!!.accessToken.startsWith("hk_"))
        assertNotNull("the grant produced no refresh token", held.refreshToken)
        assertNotNull("the grant announced no expiry", held.expiresAtMillis)

        assertTrue(
            "the mailbox list never rendered after sign-in",
            device.wait(Until.hasObject(By.text("Inbox")), TIMEOUT_MS) &&
                device.wait(Until.hasObject(By.textContains("seed message for the sign-in bullet")), TIMEOUT_MS),
        )
        captureDeviceScreen("73-inbox-after-custom-tab-sign-in")
    }

    private fun awaitTokens(): com.netzhansa.herold.shared.auth.TokenSet? {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val held = runBlocking { tokenStore.tokens() }
            if (held != null) return held
            Thread.sleep(POLL_MS)
        }
        return null
    }

    private fun launchShell() {
        val context = instrumentation.targetContext
        val intent = context.packageManager.getLaunchIntentForPackage(context.packageName)!!
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
        context.startActivity(intent)
        device.waitForIdle()
    }

    private companion object {
        const val TIMEOUT_MS = 45_000L
        const val POLL_MS = 500L
    }
}
