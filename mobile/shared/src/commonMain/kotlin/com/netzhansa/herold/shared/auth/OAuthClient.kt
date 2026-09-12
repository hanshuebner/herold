package com.netzhansa.herold.shared.auth

import io.ktor.client.HttpClient
import io.ktor.client.request.forms.submitForm
import io.ktor.client.statement.bodyAsText
import io.ktor.http.Parameters
import io.ktor.http.decodeURLQueryComponent
import io.ktor.http.encodeURLParameter
import io.ktor.http.isSuccess
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

/**
 * How this install identifies itself to herold's OAuth2 grant. The
 * client is public (RFC 8252): it holds no secret, and PKCE is what
 * binds the authorization code to this device. The redirect is the
 * private-use scheme an exported activity claims
 * (docs/design/android/notes/server-contract.md).
 */
data class OAuthClientConfig(
    val clientId: String,
    val redirectUri: String,
) {
    companion object {
        const val DEFAULT_CLIENT_ID = "herold-android"
        const val DEFAULT_REDIRECT_URI = "com.netzhansa.herold:/oauth2/callback"

        val DEFAULT = OAuthClientConfig(DEFAULT_CLIENT_ID, DEFAULT_REDIRECT_URI)
    }
}

/** What came back on the app's redirect URI. */
sealed interface OAuthCallback {
    data class Code(val code: String, val state: String) : OAuthCallback

    /** The authorization server refused (RFC 6749 section 4.1.2.1). */
    data class Denied(val error: String, val description: String?, val state: String) : OAuthCallback

    /** The URI is not a callback this app issued. */
    data object NotACallback : OAuthCallback
}

/** The token endpoint's verdict. */
sealed interface TokenExchange {
    data class Success(val tokens: TokenSet, val scope: String) : TokenExchange

    /** RFC 6749 section 5.2: the grant is gone. The session cannot be recovered. */
    data class Rejected(val error: String, val description: String?) : TokenExchange

    /** The request never reached a verdict. Retryable; the session is untouched. */
    data class Transport(val message: String) : TokenExchange
}

private val wireJson = Json { ignoreUnknownKeys = true }

/**
 * herold's OAuth2 authorization-code + PKCE grant, client half
 * (REQ-AND-AUTH-01/02; server internal/protoadmin/oauth2_native.go).
 * The authorize step runs in the system browser, so this class only
 * builds its URL; the code exchange and the refresh are plain form
 * posts to `POST /oauth2/token`.
 */
class OAuthClient(
    private val httpClient: HttpClient,
    private val config: OAuthClientConfig = OAuthClientConfig.DEFAULT,
) {

    /**
     * The `GET /oauth2/authorize` URL to open in a Custom Tab. The
     * verifier of [pending] stays on the device; only its S256 challenge
     * travels.
     */
    fun authorizeUrl(pending: PendingAuthorization): String {
        val query = listOf(
            "response_type" to "code",
            "client_id" to config.clientId,
            "redirect_uri" to pending.redirectUri,
            "state" to pending.state,
            "code_challenge" to Pkce.challenge(pending.verifier),
            "code_challenge_method" to Pkce.CHALLENGE_METHOD,
        ).joinToString("&") { (key, value) -> "$key=${value.encodeURLParameter()}" }
        return "${pending.baseUrl.trimEnd('/')}/oauth2/authorize?$query"
    }

    /** Exchanges the authorization code of a callback for the token pair. */
    suspend fun exchangeCode(
        baseUrl: String,
        code: String,
        verifier: String,
        redirectUri: String,
        nowMillis: Long,
    ): TokenExchange = post(
        baseUrl,
        Parameters.build {
            append("grant_type", "authorization_code")
            append("client_id", config.clientId)
            append("code", code)
            append("redirect_uri", redirectUri)
            append("code_verifier", verifier)
        },
        nowMillis,
    )

    /** Trades the refresh token for a fresh pair; the server rotates both. */
    suspend fun refresh(baseUrl: String, refreshToken: String, nowMillis: Long): TokenExchange = post(
        baseUrl,
        Parameters.build {
            append("grant_type", "refresh_token")
            append("client_id", config.clientId)
            append("refresh_token", refreshToken)
        },
        nowMillis,
    )

    private suspend fun post(baseUrl: String, form: Parameters, nowMillis: Long): TokenExchange {
        val response = try {
            httpClient.submitForm(
                url = "${baseUrl.trimEnd('/')}/oauth2/token",
                formParameters = form,
            )
        } catch (t: Throwable) {
            return TokenExchange.Transport(t.message ?: "cannot reach the server")
        }
        val text = try {
            response.bodyAsText()
        } catch (t: Throwable) {
            return TokenExchange.Transport(t.message ?: "the token response could not be read")
        }
        val body = runCatching { wireJson.parseToJsonElement(text).jsonObject }.getOrNull()
        if (!response.status.isSuccess()) {
            val error = body?.get("error")?.jsonPrimitive?.content ?: "invalid_grant"
            val description = body?.get("error_description")?.jsonPrimitive?.content
            // A 5xx is the server failing, not the grant being gone: the
            // caller retries rather than dropping the session.
            return if (response.status.value >= 500) {
                TokenExchange.Transport(description ?: "the token endpoint failed (${response.status.value})")
            } else {
                TokenExchange.Rejected(error, description)
            }
        }
        val access = body?.get("access_token")?.jsonPrimitive?.content
        if (access.isNullOrBlank()) {
            return TokenExchange.Transport("the token response carried no access_token")
        }
        val expiresIn = body["expires_in"]?.jsonPrimitive?.intOrNull
        return TokenExchange.Success(
            tokens = TokenSet(
                accessToken = access,
                refreshToken = body["refresh_token"]?.jsonPrimitive?.content?.takeIf { it.isNotBlank() },
                expiresAtMillis = expiresIn?.let { nowMillis + it * 1000L },
            ),
            scope = body["scope"]?.jsonPrimitive?.content.orEmpty(),
        )
    }

    companion object {
        /**
         * Reads the redirect the browser sent the app back on. Parsed by
         * hand rather than through a URL type: a private-use scheme with
         * no authority (`com.netzhansa.herold:/oauth2/callback?...`) is
         * outside what a generic http parser accepts.
         */
        fun parseCallback(uri: String, expectedRedirectUri: String): OAuthCallback {
            val path = uri.substringBefore('?').substringBefore('#')
            if (path != expectedRedirectUri) return OAuthCallback.NotACallback
            val query = uri.substringAfter('?', "").substringBefore('#')
            if (query.isEmpty()) return OAuthCallback.NotACallback
            val params = query.split('&').mapNotNull { pair ->
                if (pair.isEmpty()) return@mapNotNull null
                val key = pair.substringBefore('=').decodeURLQueryComponent()
                val value = pair.substringAfter('=', "").decodeURLQueryComponent(plusIsSpace = true)
                key to value
            }.toMap()
            val state = params["state"].orEmpty()
            params["error"]?.let { error ->
                return OAuthCallback.Denied(error, params["error_description"], state)
            }
            val code = params["code"] ?: return OAuthCallback.NotACallback
            return OAuthCallback.Code(code, state)
        }
    }
}
