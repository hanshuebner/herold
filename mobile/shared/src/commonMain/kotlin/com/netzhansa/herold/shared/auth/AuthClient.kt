package com.netzhansa.herold.shared.auth

import io.ktor.client.HttpClient
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put

/** A sign-in that did not produce a token. */
sealed interface SignInFailure {
    /** The server wants a TOTP code: none was sent, or the one sent was wrong. */
    data class TotpRequired(val message: String) : SignInFailure

    /** Wrong credentials, a disabled principal, or a rate limit. */
    data class Rejected(val message: String) : SignInFailure

    /** The request never reached a verdict (no connectivity, bad base URL, 5xx). */
    data class Transport(val message: String) : SignInFailure
}

/** Outcome of the device-token exchange. */
sealed interface SignInResult {
    data class Success(val token: String) : SignInResult

    data class Failure(val reason: SignInFailure) : SignInResult
}

private val wireJson = Json { ignoreUnknownKeys = true }

/**
 * The device-token grant (`POST /api/v1/auth/device-token`, server #199):
 * email, password and, for a principal with TOTP enrolled, the current
 * 6-digit code. It returns a long-lived `hk_...` bearer token, which the
 * caller hands to a Keystore-backed [TokenStore] (REQ-AND-AUTH-10). The token
 * is never logged and never written to the local database.
 */
class AuthClient(
    private val httpClient: HttpClient,
    private val tokenStore: TokenStore,
) {
    suspend fun signIn(
        baseUrl: String,
        email: String,
        password: String,
        totpCode: String? = null,
        deviceLabel: String = "herold Android",
    ): SignInResult {
        val body = buildJsonObject {
            put("email", email)
            put("password", password)
            if (!totpCode.isNullOrBlank()) put("totp_code", totpCode.trim())
            put("device_label", deviceLabel)
        }
        val response = try {
            httpClient.post("${baseUrl.trimEnd('/')}/api/v1/auth/device-token") {
                contentType(ContentType.Application.Json)
                setBody(wireJson.encodeToString(JsonObject.serializer(), body))
            }
        } catch (t: Throwable) {
            return SignInResult.Failure(SignInFailure.Transport(t.message ?: "cannot reach the server"))
        }

        val text = response.bodyAsText()
        if (!response.status.isSuccess()) {
            val problem = runCatching { wireJson.parseToJsonElement(text).jsonObject }.getOrNull()
            val title = problem?.get("title")?.jsonPrimitive?.content ?: "sign-in failed (${response.status.value})"
            val stepUp = problem?.get("step_up_required")?.jsonPrimitive?.booleanOrNull ?: false
            return SignInResult.Failure(
                when {
                    stepUp -> SignInFailure.TotpRequired(title)
                    response.status.value >= 500 -> SignInFailure.Transport(title)
                    else -> SignInFailure.Rejected(title)
                },
            )
        }

        val token = runCatching {
            wireJson.parseToJsonElement(text).jsonObject["token"]?.jsonPrimitive?.content
        }.getOrNull()
        if (token.isNullOrBlank()) {
            return SignInResult.Failure(SignInFailure.Transport("device-token response carried no token"))
        }
        tokenStore.store(token)
        return SignInResult.Success(token)
    }

    /** Clears the stored token; the caller also clears the local database (REQ-AND-AUTH-21). */
    suspend fun signOut() {
        tokenStore.clear()
    }
}
