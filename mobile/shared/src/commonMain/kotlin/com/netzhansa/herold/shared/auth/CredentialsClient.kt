package com.netzhansa.herold.shared.auth

import io.ktor.client.HttpClient
import io.ktor.client.request.delete
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.http.HttpHeaders
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * One thing that can currently authenticate as the signed-in principal:
 * a browser session, a device token, or an OAuth2 grant of a native
 * client (server `GET /api/v1/auth/credentials`, issue #224; Suite
 * REQ-AS-30..33). The id is opaque and scoped to the kind - never a
 * credential itself.
 */
@Serializable
data class Credential(
    val kind: String,
    val id: String,
    val label: String = "",
    @SerialName("client_id") val clientId: String = "",
    @SerialName("created_at") val createdAt: String = "",
    @SerialName("last_used_at") val lastUsedAt: String = "",
    @SerialName("expires_at") val expiresAt: String = "",
    @SerialName("last_seen_ip") val lastSeenIp: String = "",
    @SerialName("user_agent") val userAgent: String = "",
    @SerialName("is_current") val isCurrent: Boolean = false,
) {
    companion object {
        const val KIND_SESSION = "session"
        const val KIND_DEVICE_TOKEN = "device_token"
        const val KIND_OAUTH2_GRANT = "oauth2_grant"
    }
}

@Serializable
private data class CredentialPage(val items: List<Credential> = emptyList())

private val wireJson = Json { ignoreUnknownKeys = true }

/**
 * The account's active credentials, as the Suite's session management
 * reads them (REQ-AND-AUTH-22 over the same endpoints the Suite uses).
 * Revocation takes effect on the server's very next request, which is
 * what makes revoking this device's own grant sign the app out.
 */
class CredentialsClient(
    private val httpClient: HttpClient,
    private val baseUrl: String,
    private val calls: BearerCalls,
) {

    suspend fun list(): List<Credential> {
        val answered = calls.call { token ->
            httpClient.get("${baseUrl.trimEnd('/')}$PATH") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }
        if (!answered.isSuccess) {
            throw SessionExpiredException("the sessions list failed (${answered.status})")
        }
        return wireJson.decodeFromString(CredentialPage.serializer(), answered.body).items
    }

    /** Revokes one credential. Returns true when the server dropped it. */
    suspend fun revoke(kind: String, id: String): Boolean =
        calls.call { token ->
            httpClient.delete("${baseUrl.trimEnd('/')}$PATH/$kind/$id") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }.isSuccess

    /**
     * The grant this install signed in under, as the server names it:
     * the entry it marks `is_current` for the token the call carried
     * (server issue #356). Read once, right after the code exchange,
     * and kept - a family id survives every refresh-token rotation, so
     * it stays this device's handle for the life of the session.
     */
    suspend fun currentGrantId(): String? =
        list().firstOrNull { it.kind == Credential.KIND_OAUTH2_GRANT && it.isCurrent }?.id

    private companion object {
        const val PATH = "/api/v1/auth/credentials"
    }
}
