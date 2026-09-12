package com.netzhansa.herold.shared.auth

import io.ktor.http.HttpStatusCode
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The two halves of the authorization-code flow either side of the
 * browser (REQ-AND-AUTH-01/02).
 */
class OAuthSignInTest {

    private val baseUrl = "https://mail.example.test"

    @Test
    fun beginPersistsTheVerifierSoTheExchangeSurvivesTheBrowser() = runTest {
        val store = InMemoryTokenStore()
        val signIn = signIn(store) { respondJson("{}") }

        val url = signIn.begin("$baseUrl/")
        val pending = store.pending()

        assertTrue(signIn.inProgress())
        assertEquals(baseUrl, pending?.baseUrl)
        assertTrue(url.contains("code_challenge=${Pkce.challenge(pending!!.verifier)}"))
        assertTrue(url.contains("state=${pending.state}"))
    }

    @Test
    fun aCallbackIsExchangedAndTheTokensStored() = runTest {
        val store = InMemoryTokenStore()
        var body = ""
        val signIn = signIn(store) { request ->
            body = (request.body as io.ktor.http.content.OutgoingContent.ByteArrayContent)
                .bytes().decodeToString()
            respondJson("""{"access_token":"hk_a","refresh_token":"rt_a","expires_in":3600}""")
        }
        signIn.begin(baseUrl)
        val pending = store.pending()!!

        val result = signIn.complete(
            "${OAuthClientConfig.DEFAULT_REDIRECT_URI}?code=thecode&state=${pending.state}",
        )

        assertEquals(OAuthSignInResult.Success(TokenSet("hk_a", "rt_a", 3_600_000L), baseUrl), result)
        assertEquals("hk_a", store.tokens()?.accessToken)
        assertNull(store.pending())
        assertTrue(body.contains("grant_type=authorization_code"), body)
        assertTrue(body.contains("code=thecode"), body)
        assertTrue(body.contains("code_verifier=${pending.verifier}"), body)
    }

    @Test
    fun aCodeUnderAnotherStateIsRefused() = runTest {
        val store = InMemoryTokenStore()
        var requests = 0
        val signIn = signIn(store) {
            requests++
            respondJson("""{"access_token":"hk_a"}""")
        }
        signIn.begin(baseUrl)

        val result = signIn.complete("${OAuthClientConfig.DEFAULT_REDIRECT_URI}?code=c&state=forged")

        assertIs<OAuthSignInResult.Failed>(result)
        assertEquals(0, requests)
        assertNull(store.tokens())
        assertNull(store.pending())
    }

    @Test
    fun aDeniedAuthorizationReportsWhatTheServerSaid() = runTest {
        val store = InMemoryTokenStore()
        val signIn = signIn(store) { respondJson("{}") }
        signIn.begin(baseUrl)

        val result = signIn.complete(
            "${OAuthClientConfig.DEFAULT_REDIRECT_URI}?error=access_denied&error_description=nope",
        )

        assertEquals(OAuthSignInResult.Failed("nope"), result)
        assertNull(store.tokens())
    }

    @Test
    fun aRefusedExchangeDropsTheRequest() = runTest {
        val store = InMemoryTokenStore()
        val signIn = signIn(store) {
            respondJson("""{"error":"invalid_grant"}""", HttpStatusCode.BadRequest)
        }
        signIn.begin(baseUrl)
        val pending = store.pending()!!

        val result = signIn.complete(
            "${OAuthClientConfig.DEFAULT_REDIRECT_URI}?code=c&state=${pending.state}",
        )

        assertIs<OAuthSignInResult.Failed>(result)
        assertNull(store.pending())
    }

    @Test
    fun anUndeliverableExchangeKeepsTheRequestForARetry() = runTest {
        val store = InMemoryTokenStore()
        val signIn = signIn(store) {
            respondJson("""{"error":"server_error"}""", HttpStatusCode.ServiceUnavailable)
        }
        signIn.begin(baseUrl)
        val pending = store.pending()!!

        val result = signIn.complete(
            "${OAuthClientConfig.DEFAULT_REDIRECT_URI}?code=c&state=${pending.state}",
        )

        assertIs<OAuthSignInResult.Failed>(result)
        assertTrue(signIn.inProgress())
    }

    @Test
    fun abandoningDropsTheRequest() = runTest {
        val store = InMemoryTokenStore()
        val signIn = signIn(store) { respondJson("{}") }
        signIn.begin(baseUrl)

        signIn.abandon()

        assertFalse(signIn.inProgress())
    }

    private fun signIn(
        store: InMemoryTokenStore,
        handler: suspend io.ktor.client.engine.mock.MockRequestHandleScope.(
            io.ktor.client.request.HttpRequestData,
        ) -> io.ktor.client.request.HttpResponseData,
    ) = OAuthSignIn(
        oauth = OAuthClient(FakeHttp.client(handler)),
        tokens = store,
        pending = store,
        now = { 0L },
    )
}
