package com.netzhansa.herold.shared.auth

import io.ktor.client.request.HttpRequestData
import io.ktor.http.HttpStatusCode
import io.ktor.utils.io.readRemaining
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runTest
import kotlinx.io.readString
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The refresh path (REQ-AND-AUTH-04): one refresh serves every caller,
 * a refused refresh ends the session, and an undeliverable one leaves
 * the credential alone so being offline is not being signed out.
 */
class SessionAuthenticatorTest {

    private val baseUrl = "https://mail.example.test"

    @Test
    fun aTokenInsideItsLifetimeIsHandedOutUnchanged() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first", "rt_first", expiresAtMillis = 10_000_000))
        var requests = 0
        val authenticator = authenticator(store, now = { 1_000L }) { requests++ }

        assertEquals("hk_first", authenticator.accessToken())
        assertEquals(0, requests)
    }

    @Test
    fun aTokenInsideTheSkewWindowIsRefreshedBeforeItIsUsed() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first", "rt_first", expiresAtMillis = 100_000L))
        var requests = 0
        // 30 seconds to go: inside the one-minute skew, so the request
        // goes out on a token the server will still accept afterwards.
        val authenticator = authenticator(store, now = { 70_000L }) { requests++ }

        assertEquals("hk_next", authenticator.accessToken())
        assertEquals(1, requests)
        assertEquals("rt_next", store.tokens()?.refreshToken)
    }

    @Test
    fun aTokenWithNoAnnouncedExpiryIsNeverRefreshedAhead() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_device"))
        var requests = 0
        val authenticator = authenticator(store, now = { 10_000_000L }) { requests++ }

        assertEquals("hk_device", authenticator.accessToken())
        assertEquals(0, requests)
    }

    @Test
    fun concurrentCallersShareOneRefresh() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first", "rt_first", expiresAtMillis = 100_000L))
        var requests = 0
        val authenticator = authenticator(store, now = { 1_000L }) { requests++ }

        val refreshed = listOf(
            async { authenticator.refreshAfterUnauthorized("hk_first") },
            async { authenticator.refreshAfterUnauthorized("hk_first") },
            async { authenticator.refreshAfterUnauthorized("hk_first") },
        ).awaitAll()

        assertEquals(listOf("hk_next", "hk_next", "hk_next"), refreshed)
        assertEquals(1, requests)
    }

    @Test
    fun aRefusedRefreshClearsTheCredentialAndReportsTheSessionLost() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first", "rt_revoked", expiresAtMillis = 100_000L))
        var lost = 0
        val authenticator = SessionAuthenticator(
            store = store,
            oauth = OAuthClient(
                FakeHttp.client {
                    respondJson("""{"error":"invalid_grant"}""", HttpStatusCode.BadRequest)
                },
            ),
            baseUrl = baseUrl,
            now = { 1_000L },
            onSessionLost = { lost++ },
        )

        assertNull(authenticator.refreshAfterUnauthorized("hk_first"))
        assertNull(store.tokens())
        assertEquals(1, lost)
    }

    @Test
    fun anUndeliverableRefreshKeepsTheCredential() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first", "rt_first", expiresAtMillis = 100_000L))
        var lost = 0
        val authenticator = SessionAuthenticator(
            store = store,
            oauth = OAuthClient(
                FakeHttp.client {
                    respondJson("""{"error":"server_error"}""", HttpStatusCode.BadGateway)
                },
            ),
            baseUrl = baseUrl,
            now = { 1_000L },
            onSessionLost = { lost++ },
        )

        assertFailsWith<SessionExpiredException> { authenticator.refreshAfterUnauthorized("hk_first") }
        assertEquals("hk_first", store.tokens()?.accessToken)
        assertEquals(0, lost)
    }

    @Test
    fun anUndeliverableAheadOfExpiryRefreshStillHandsOutTheHeldToken() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first", "rt_first", expiresAtMillis = 100_000L))
        val authenticator = SessionAuthenticator(
            store = store,
            oauth = OAuthClient(
                FakeHttp.client {
                    respondJson("""{"error":"server_error"}""", HttpStatusCode.BadGateway)
                },
            ),
            baseUrl = baseUrl,
            now = { 70_000L },
        )

        assertEquals("hk_first", authenticator.accessToken())
    }

    @Test
    fun aCredentialWithNoRefreshTokenEndsTheSessionOnA401() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_device"))
        var lost = 0
        val authenticator = SessionAuthenticator(
            store = store,
            oauth = OAuthClient(FakeHttp.client { respondJson("{}") }),
            baseUrl = baseUrl,
            now = { 1_000L },
            onSessionLost = { lost++ },
        )

        assertNull(authenticator.refreshAfterUnauthorized("hk_device"))
        assertNull(store.tokens())
        assertEquals(1, lost)
    }

    @Test
    fun theRefreshRequestCarriesTheGrantTypeClientIdAndToken() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first", "rt_first", expiresAtMillis = 100_000L))
        var body = ""
        val authenticator = SessionAuthenticator(
            store = store,
            oauth = OAuthClient(
                FakeHttp.client { request ->
                    body = request.formBody()
                    respondJson("""{"access_token":"hk_next","refresh_token":"rt_next","expires_in":3600}""")
                },
            ),
            baseUrl = baseUrl,
            now = { 1_000L },
        )

        authenticator.refreshAfterUnauthorized("hk_first")
        assertTrue(body.contains("grant_type=refresh_token"), body)
        assertTrue(body.contains("client_id=herold-android"), body)
        assertTrue(body.contains("refresh_token=rt_first"), body)
        // The server's announced lifetime becomes an absolute instant.
        assertEquals(1_000L + 3_600_000L, store.tokens()?.expiresAtMillis)
    }

    @Test
    fun theGateHoldsTheTokenBackUntilTheAppIsUnlocked() = runTest {
        val store = InMemoryTokenStore()
        store.store(TokenSet("hk_first"))
        val gate = TestGate()
        val authenticator = SessionAuthenticator(
            store = store,
            oauth = OAuthClient(FakeHttp.client { respondJson("{}") }),
            baseUrl = baseUrl,
            now = { 1_000L },
            unlockGate = gate,
        )

        val pending = async { authenticator.accessToken() }
        delay(10)
        assertTrue(pending.isActive, "the token was released while the app was locked")
        gate.unlock()
        assertEquals("hk_first", pending.await())
    }

    /** A [SessionAuthenticator] whose token endpoint always rotates to hk_next. */
    private fun authenticator(
        store: TokenStore,
        now: () -> Long,
        onRequest: () -> Unit,
    ) = SessionAuthenticator(
        store = store,
        oauth = OAuthClient(
            FakeHttp.client {
                onRequest()
                // A pause the concurrent callers can pile up behind.
                delay(20)
                respondJson("""{"access_token":"hk_next","refresh_token":"rt_next","expires_in":3600}""")
            },
        ),
        baseUrl = baseUrl,
        now = now,
    )
}

/** A gate the test opens by hand. */
private class TestGate : UnlockGate {
    private val locked = MutableStateFlow(true)

    override fun isUnlocked(): Boolean = !locked.value

    override suspend fun awaitUnlocked() {
        locked.first { !it }
    }

    fun unlock() {
        locked.value = false
    }
}

private suspend fun HttpRequestData.formBody(): String =
    (body as io.ktor.http.content.OutgoingContent.ByteArrayContent).bytes().decodeToString()
