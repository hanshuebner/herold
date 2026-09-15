package com.netzhansa.herold.shared.auth

import io.ktor.client.HttpClient
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.contentType
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put

/** What `POST /api/v1/auth/step-up` answered. */
sealed interface StepUpOutcome {
    /**
     * The credential is elevated until [expiresAt] (RFC 3339, as the
     * server wrote it). The refused request can go out again.
     */
    data class Elevated(val expiresAt: String) : StepUpOutcome

    /** The code was wrong, or its window had already passed. Ask again. */
    data class Refused(val message: String) : StepUpOutcome

    /** Too many attempts; the server is holding further tries off. */
    data class RateLimited(val message: String) : StepUpOutcome

    /** The principal has no TOTP enrolled, so there is no code to give. */
    data class EnrollRequired(val message: String) : StepUpOutcome

    /** The attempt never reached a verdict. */
    data class Transport(val message: String) : StepUpOutcome
}

private val wireJson = Json { ignoreUnknownKeys = true }

/**
 * The TOTP elevation of a bearer credential (`POST /api/v1/auth/step-up`,
 * server REQ-AUTH-79, issue #357). A device token and an OAuth2 access
 * token are elevated by the same call, for the same window a cookie
 * session gets; the elevation lives on the credential, so the token the
 * client already holds carries it afterwards.
 *
 * The call takes its token straight from [TokenProvider] rather than
 * through [BearerCalls]: a step-up is what answers a refused call, and
 * routing it back through the same recovery would nest one elevation
 * inside another.
 */
class StepUpClient(
    private val httpClient: HttpClient,
    private val baseUrl: String,
    private val tokens: TokenProvider,
) {

    suspend fun elevate(totpCode: String): StepUpOutcome {
        val token = try {
            tokens.accessToken()
        } catch (expired: SessionExpiredException) {
            return StepUpOutcome.Transport(expired.message ?: "the session is gone")
        }
        var response = try {
            post(token, totpCode)
        } catch (t: Throwable) {
            return StepUpOutcome.Transport(t.message ?: "cannot reach the server")
        }
        var text = response.bodyAsText()
        if (response.status.value == UNAUTHORIZED && !refusesTheCode(text)) {
            // The token itself was refused rather than the code. One
            // refresh, then the same code again - it is still inside
            // its own 30-second window.
            val refreshed = try {
                tokens.refreshAfterUnauthorized(token)
            } catch (expired: SessionExpiredException) {
                null
            }
            if (refreshed != null) {
                response = try {
                    post(refreshed, totpCode)
                } catch (t: Throwable) {
                    return StepUpOutcome.Transport(t.message ?: "cannot reach the server")
                }
                text = response.bodyAsText()
            }
        }

        val title = problemString(text, "title").ifBlank { problemString(text, "detail") }
        return when (response.status.value) {
            OK -> {
                val expiresAt = runCatching {
                    wireJson.parseToJsonElement(text).jsonObject["elevation_expires_at"]
                        ?.jsonPrimitive?.content
                }.getOrNull().orEmpty()
                StepUpOutcome.Elevated(expiresAt)
            }

            BAD_REQUEST ->
                if (problemFlag(text, "enroll_required")) {
                    StepUpOutcome.EnrollRequired(
                        title.ifBlank { "This account has no authenticator app enrolled." },
                    )
                } else {
                    StepUpOutcome.Refused(title.ifBlank { "Enter the six-digit code." })
                }

            UNAUTHORIZED -> StepUpOutcome.Refused(
                title.ifBlank { "That code is not valid. Try the current one." },
            )

            TOO_MANY_REQUESTS -> StepUpOutcome.RateLimited(
                title.ifBlank { "Too many attempts. Wait a moment and try again." },
            )

            else -> StepUpOutcome.Transport(
                title.ifBlank { "the step-up failed (${response.status.value})" },
            )
        }
    }

    /**
     * Whether a 401 is the server refusing the code rather than the
     * token. The endpoint answers both with `unauthorized`; only the
     * TOTP refusal names the code.
     */
    private fun refusesTheCode(body: String): Boolean {
        val line = problemString(body, "title") + " " + problemString(body, "detail")
        return line.contains("TOTP", ignoreCase = true)
    }

    private suspend fun post(token: String, totpCode: String): HttpResponse {
        val body = buildJsonObject { put("totp_code", totpCode.trim()) }
        return httpClient.post("${baseUrl.trimEnd('/')}$PATH") {
            header(HttpHeaders.Authorization, "Bearer $token")
            contentType(ContentType.Application.Json)
            setBody(wireJson.encodeToString(JsonObject.serializer(), body))
        }
    }

    private companion object {
        const val PATH = "/api/v1/auth/step-up"
        const val OK = 200
        const val BAD_REQUEST = 400
        const val UNAUTHORIZED = 401
        const val TOO_MANY_REQUESTS = 429
    }
}
