package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import kotlinx.coroutines.runBlocking
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
 * The native six-digit step-up sheet against the server's bearer
 * step-up path (issue #401; REQ-AND-AUTH-20/22 over server REQ-AUTH-79).
 *
 * Minting an app password is on the server's self-service elevation
 * list, so for `admin@example.local` - the dev instance's TOTP-enrolled
 * principal - the first attempt comes back `step_up_required`. The sheet
 * answers it: a wrong code keeps the sheet up with the server's refusal,
 * the code generated from `ADMIN_TOTP_SECRET` elevates the access token,
 * and the operation that raised the sheet completes on its own.
 *
 * The sessions screen's "this device" comes from the server's
 * `is_current` (server issue #356), which the test cross-checks against
 * an independent read of `GET /api/v1/auth/credentials`.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class StepUpAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private val tokenStore: KeystoreTokenStore
        get() = KeystoreTokenStore(InstrumentationRegistry.getInstrumentation().targetContext)

    @Before
    fun signedOut() {
        grantNotificationPermission()
        app.container.unlock.enabled = false
        runBlocking { app.container.signOut() }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-submit").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @Test
    fun t76_thisDeviceComesFromTheServersIsCurrent() {
        signInThroughTheGrant()
        val token = runBlocking { tokenStore.currentToken() }!!
        val grantId = runBlocking { tokenStore.grantId() }
        assertNotNull("the sign-in did not record which grant is this device", grantId)

        // What the server itself marks, read outside the app.
        val marked = AccountApi.credentials(DevInstance.baseUrl, token)
            .filter { it.optBoolean("is_current") }
        assertEquals("the server marked no single current credential: $marked", 1, marked.size)
        assertEquals("oauth2_grant", marked.single().optString("kind"))
        assertEquals(grantId, marked.single().optString("id"))

        openSessions()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("session-oauth2_grant-$grantId").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("session-this-device").assertIsDisplayed()
        compose.captureScreen("76-sessions-this-device-is-current")
    }

    @Test
    fun t77_anElevatedOperationRaisesTheSheetAndCompletesOnTheSecondCode() {
        val secret = requireNotNull(DevInstance.totpSecret) {
            "pass -e heroldTotpSecret <ADMIN_TOTP_SECRET> so the sheet has a code to answer with"
        }
        signInThroughTheGrant()
        val token = runBlocking { tokenStore.currentToken() }!!
        val before = AccountApi.apiKeys(DevInstance.baseUrl, token).size

        openSessions()
        compose.onNodeWithTag("sessions-new-app-password").performClick()

        // The server refused until the credential is elevated.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("stepup-code").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("77-stepup-sheet")

        // A wrong code keeps the sheet up with the server's refusal.
        compose.onNodeWithTag("stepup-code").performTextInput("000000")
        compose.onNodeWithTag("stepup-submit").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("stepup-error").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("78-stepup-wrong-code")

        compose.onNodeWithTag("stepup-code").performTextInput(Totp.code(secret))
        compose.onNodeWithTag("stepup-submit").performClick()

        // The operation the sheet interrupted went out again on its own.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("app-password-secret").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("app-password-dialog").assertIsDisplayed()
        compose.captureScreen("79-app-password-created")

        val after = AccountApi.apiKeys(DevInstance.baseUrl, token)
        assertEquals("the server holds no new key for the account", before + 1, after.size)
        assertTrue(
            "the new key does not carry the label the phone sent: $after",
            after.any { it.label.startsWith("Phone app password") },
        )
    }

    /** Settings -> Sessions, from the inbox the sign-in landed on. */
    private fun openSessions() {
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-settings").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-settings").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("settings-sessions").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("settings-screen").performScrollToNode(hasTestTag("settings-sessions"))
        compose.onNodeWithTag("settings-sessions").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("sessions-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /**
     * Signs in the way the app does, with herold's login page driven
     * from the test process instead of the browser.
     */
    private fun signInThroughTheGrant() {
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
        compose.waitUntil(SIGN_IN_TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val SIGN_IN_TIMEOUT_MS = 60_000L
    }
}
