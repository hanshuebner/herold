package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.hasTestTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.KeystoreTokenStore
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Milestone 2b's acceptance (issue #352) for what happens to a signed-in
 * session: the silent refresh of an expired access token, forced
 * sign-in when the grant is revoked, and the sessions screen.
 *
 * The Custom Tab itself is [CustomTabSignInTest]'s bullet. These
 * checks need a signed-in app rather than a browser and take
 * [OAuthHarness], which speaks to the same `/oauth2/authorize`
 * endpoints with the same PKCE parameters the app minted.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class OAuthAcceptanceTest {

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
    fun t73_anExpiredAccessTokenIsRefreshedSilentlyAndTheSyncSucceeds() {
        signInThroughTheGrant()
        val before = runBlocking { tokenStore.tokens() }!!

        // The server-side row behind the access token goes away; the
        // refresh token is untouched. herold has no configurable
        // access-token TTL, so this is how the 401 path is reached
        // without waiting an hour.
        AccountApi.expireAccessToken(DevInstance.baseUrl, before.accessToken, DevInstance.oauthClientId)

        val session = app.container.session.value!!
        runBlocking { session.syncEngine.syncAll() }

        val after = runBlocking { tokenStore.tokens() }!!
        assertNotEquals("the access token was not refreshed", before.accessToken, after.accessToken)
        assertNotEquals("the refresh token did not rotate", before.refreshToken, after.refreshToken)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-title").assertIsDisplayed()
        compose.captureScreen("73-inbox-after-silent-refresh")
    }

    @Test
    fun t74_aRevokedGrantReturnsToTheSignInScreen() {
        signInThroughTheGrant()
        val token = runBlocking { tokenStore.currentToken() }!!
        val grantId = AccountApi.ownGrantId(DevInstance.baseUrl, token, DevInstance.oauthClientId)

        // Revoking the family drops the refresh token and the access
        // token with it, so the next call cannot be recovered.
        assertEquals(
            204,
            AccountApi.revokeCredential(DevInstance.baseUrl, token, "oauth2_grant", grantId),
        )

        val session = app.container.session.value!!
        runCatching { runBlocking { session.syncEngine.syncAll() } }

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-submit").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("signin-error").assertIsDisplayed()
        assertEquals(null, runBlocking { tokenStore.currentToken() })
        compose.captureScreen("74-forced-sign-in-after-revoke")
    }

    @Test
    fun t75_theSessionsScreenMarksThisDeviceAndARemoteRevokeSignsTheAppOut() {
        signInThroughTheGrant()
        val token = runBlocking { tokenStore.currentToken() }!!

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
            compose.onAllNodesWithTag("session-this-device").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("session-this-device").assertIsDisplayed()
        compose.captureScreen("75-sessions-this-device")

        // A second session of the same principal - a device token the
        // harness mints - revokes this device's grant.
        val grantId = AccountApi.ownGrantId(DevInstance.baseUrl, token, DevInstance.oauthClientId)
        val other = runBlocking { DevInstance.deviceToken(DevInstance.totpEmail) }
        assertEquals(
            204,
            AccountApi.revokeCredential(DevInstance.baseUrl, other, "oauth2_grant", grantId),
        )

        val session = app.container.session.value!!
        runCatching { runBlocking { session.syncEngine.syncAll() } }

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("signin-submit").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("76-signed-out-by-remote-revoke")
    }

    /**
     * Signs in the way the app does, with herold's login page driven
     * from the test process instead of the browser.
     */
    private fun signInThroughTheGrant() {
        val secret = DevInstance.totpSecret
        runBlocking {
            val authorizeUrl = app.container.beginSignIn(DevInstance.baseUrl)
            val redirect = OAuthHarness.authorize(
                authorizeUrl = authorizeUrl,
                email = DevInstance.totpEmail,
                password = DevInstance.password,
                totpSecret = secret,
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
