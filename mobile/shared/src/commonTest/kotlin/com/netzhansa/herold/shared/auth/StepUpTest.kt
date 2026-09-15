package com.netzhansa.herold.shared.auth

import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.http.HttpHeaders
import io.ktor.http.HttpStatusCode
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The TOTP elevation of a bearer credential (REQ-AND-AUTH-20 against
 * server REQ-AUTH-79): what `POST /api/v1/auth/step-up` answers, how the
 * sheet re-prompts on a refusal, and how a refused call is repeated
 * once the credential is elevated.
 */
class StepUpTest {

    private val baseUrl = "https://mail.example.test"

    /** A provider over one token, counting the refreshes asked of it. */
    private class Tokens(
        var token: String = "hk_access",
        val replacement: String? = null,
    ) : TokenProvider {
        var refreshes = 0

        override suspend fun accessToken(): String = token

        override suspend fun refreshAfterUnauthorized(staleToken: String): String? {
            refreshes++
            val next = replacement ?: return null
            token = next
            return next
        }
    }

    // ---------------------------------------------------------------
    // StepUpClient
    // ---------------------------------------------------------------

    @Test
    fun aValidCodeElevatesTheCredentialAndCarriesTheExpiry() = runTest {
        var sentAuthorization: String? = null
        var sentBody: String? = null
        val http = FakeHttp.client { request ->
            assertEquals("/api/v1/auth/step-up", request.url.encodedPath)
            sentAuthorization = request.headers[HttpHeaders.Authorization]
            sentBody = (request.body as io.ktor.http.content.OutgoingContent.ByteArrayContent)
                .bytes().decodeToString()
            respondJson("""{"elevation_expires_at":"2026-09-15T10:15:00Z"}""")
        }

        val outcome = StepUpClient(http, baseUrl, Tokens()).elevate("123456")

        assertEquals(StepUpOutcome.Elevated("2026-09-15T10:15:00Z"), outcome)
        assertEquals("Bearer hk_access", sentAuthorization)
        assertEquals("""{"totp_code":"123456"}""", sentBody)
    }

    @Test
    fun aWrongCodeIsRefusedWithTheServersLine() = runTest {
        val http = FakeHttp.client { _ ->
            respondJson(
                """{"type":"about:blank","title":"TOTP code is invalid or expired","status":401}""",
                HttpStatusCode.Unauthorized,
            )
        }
        val tokens = Tokens(replacement = "hk_refreshed")

        val outcome = StepUpClient(http, baseUrl, tokens).elevate("000000")

        assertEquals(StepUpOutcome.Refused("TOTP code is invalid or expired"), outcome)
        // The token was not the problem, so no refresh was burned on it.
        assertEquals(0, tokens.refreshes)
    }

    @Test
    fun aRefusedTokenIsRefreshedOnceAndTheSameCodeGoesOutAgain() = runTest {
        var attempts = 0
        val seen = mutableListOf<String?>()
        val http = FakeHttp.client { request ->
            seen += request.headers[HttpHeaders.Authorization]
            attempts++
            if (attempts == 1) {
                respondJson(
                    """{"type":"about:blank","title":"authentication required","status":401}""",
                    HttpStatusCode.Unauthorized,
                )
            } else {
                respondJson("""{"elevation_expires_at":"2026-09-15T10:15:00Z"}""")
            }
        }
        val tokens = Tokens(replacement = "hk_refreshed")

        val outcome = StepUpClient(http, baseUrl, tokens).elevate("123456")

        assertEquals(StepUpOutcome.Elevated("2026-09-15T10:15:00Z"), outcome)
        assertEquals(1, tokens.refreshes)
        assertEquals(listOf<String?>("Bearer hk_access", "Bearer hk_refreshed"), seen.toList())
    }

    @Test
    fun tooManyAttemptsAreReportedAsARateLimit() = runTest {
        val http = FakeHttp.client {
            respondJson(
                """{"title":"too many TOTP attempts; please wait","status":429}""",
                HttpStatusCode.TooManyRequests,
            )
        }

        val outcome = StepUpClient(http, baseUrl, Tokens()).elevate("123456")

        assertEquals(StepUpOutcome.RateLimited("too many TOTP attempts; please wait"), outcome)
    }

    @Test
    fun anAccountWithoutAnAuthenticatorIsToldToEnrol() = runTest {
        val http = FakeHttp.client {
            respondJson(
                """{"title":"TOTP enrollment required","status":400,"enroll_required":true}""",
                HttpStatusCode.BadRequest,
            )
        }

        val outcome = StepUpClient(http, baseUrl, Tokens()).elevate("123456")

        assertEquals(StepUpOutcome.EnrollRequired("TOTP enrollment required"), outcome)
    }

    // ---------------------------------------------------------------
    // StepUpCoordinator
    // ---------------------------------------------------------------

    @Test
    fun theSheetStaysUpOnAWrongCodeAndTheNextOneElevates() = runTest {
        var attempts = 0
        val http = FakeHttp.client {
            attempts++
            if (attempts == 1) {
                respondJson(
                    """{"title":"TOTP code is invalid or expired","status":401}""",
                    HttpStatusCode.Unauthorized,
                )
            } else {
                respondJson("""{"elevation_expires_at":"2026-09-15T10:15:00Z"}""")
            }
        }
        val coordinator = StepUpCoordinator(StepUpClient(http, baseUrl, Tokens()), now = { 0L })

        val elevated = async { coordinator.elevate() }
        coordinator.prompt.first { it != null }
        coordinator.submit("000000")
        val refused = coordinator.prompt.first { it?.error != null }
        assertEquals("TOTP code is invalid or expired", refused?.error)

        coordinator.submit("123456")
        assertTrue(elevated.await())
        assertNull(coordinator.prompt.value)
        assertEquals(2, attempts)
    }

    @Test
    fun dismissingTheSheetLeavesTheOperationUndone() = runTest {
        var attempts = 0
        val http = FakeHttp.client {
            attempts++
            respondJson("""{"elevation_expires_at":"2026-09-15T10:15:00Z"}""")
        }
        val coordinator = StepUpCoordinator(StepUpClient(http, baseUrl, Tokens()), now = { 0L })

        val elevated = async { coordinator.elevate() }
        coordinator.prompt.first { it != null }
        coordinator.cancel()

        assertFalse(elevated.await())
        assertEquals(0, attempts)
        assertNull(coordinator.prompt.value)
    }

    @Test
    fun anElevationThatJustSucceededAnswersTheNextRefusalWithoutAsking() = runTest {
        var attempts = 0
        val http = FakeHttp.client {
            attempts++
            respondJson("""{"elevation_expires_at":"2026-09-15T10:15:00Z"}""")
        }
        var clock = 0L
        val coordinator = StepUpCoordinator(StepUpClient(http, baseUrl, Tokens()), now = { clock })

        val first = async { coordinator.elevate() }
        coordinator.prompt.first { it != null }
        coordinator.submit("123456")
        assertTrue(first.await())

        // A second call refused in the same breath rides the elevation
        // that just happened.
        clock = 1_000L
        assertTrue(coordinator.elevate())
        assertEquals(1, attempts)
        assertNull(coordinator.prompt.value)

        // Past the coalescing window the sheet comes back up.
        clock = 60_000L
        val later = async { coordinator.elevate() }
        assertEquals(StepUpPrompt(), coordinator.prompt.first { it != null })
        coordinator.submit("123456")
        assertTrue(later.await())
        assertEquals(2, attempts)
    }

    // ---------------------------------------------------------------
    // BearerCalls
    // ---------------------------------------------------------------

    @Test
    fun aRefusedCallGoesOutAgainOnceTheCredentialIsElevated() = runTest {
        var attempts = 0
        val http = FakeHttp.client {
            attempts++
            if (attempts == 1) {
                respondJson(
                    """{"title":"step-up required","status":403,"step_up_required":true,""" +
                        """"elevation_scope":"self-service"}""",
                    HttpStatusCode.Forbidden,
                )
            } else {
                respondJson("""{"id":7,"label":"key","key":"hk_new"}""", HttpStatusCode.Created)
            }
        }
        var elevations = 0
        val gate = object : StepUpGate {
            override suspend fun elevate(): Boolean {
                elevations++
                return true
            }
        }

        val answered = BearerCalls(Tokens(), gate).call { token ->
            http.get("$baseUrl/api/v1/principals/1/api-keys") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }

        assertEquals(201, answered.status)
        assertEquals(1, elevations)
        assertEquals(2, attempts)
    }

    @Test
    fun aRefusedCallStandsWhenTheSheetIsDismissed() = runTest {
        var attempts = 0
        val http = FakeHttp.client {
            attempts++
            respondJson(
                """{"title":"step-up required","status":403,"step_up_required":true}""",
                HttpStatusCode.Forbidden,
            )
        }

        val answered = BearerCalls(Tokens(), StepUpGate.REFUSED).call { token ->
            http.get("$baseUrl/api/v1/principals/1/api-keys") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }

        assertTrue(answered.stepUpRequired)
        assertEquals(1, attempts)
    }

    @Test
    fun anExpiredTokenIsRefreshedBeforeTheCallIsJudged() = runTest {
        var attempts = 0
        val http = FakeHttp.client {
            attempts++
            if (attempts == 1) {
                respondJson("""{"title":"authentication required"}""", HttpStatusCode.Unauthorized)
            } else {
                respondJson("""{"items":[]}""")
            }
        }
        val tokens = Tokens(replacement = "hk_refreshed")

        val answered = BearerCalls(tokens).call { token ->
            http.get("$baseUrl/api/v1/auth/credentials") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }

        assertTrue(answered.isSuccess)
        assertEquals(1, tokens.refreshes)
    }

    @Test
    fun theCurrentGrantIsTheOneTheServerMarks() = runTest {
        val http = FakeHttp.client {
            respondJson(
                """{"items":[
                   {"kind":"oauth2_grant","id":"fam-old","client_id":"herold-android",
                    "created_at":"2026-09-15T09:00:00Z","is_current":false},
                   {"kind":"oauth2_grant","id":"fam-this","client_id":"herold-android",
                    "created_at":"2026-09-14T09:00:00Z","is_current":true},
                   {"kind":"session","id":"sess-1","created_at":"2026-09-15T08:00:00Z","is_current":false}
                ]}""",
            )
        }

        val credentials = CredentialsClient(http, baseUrl, BearerCalls(Tokens()))

        assertEquals("fam-this", credentials.currentGrantId())
    }
}
