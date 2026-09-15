package com.netzhansa.herold.shared.auth

import io.ktor.client.HttpClient
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.contentType
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put

/** A key another mail client signs in with. The secret is shown once. */
data class AppPassword(val id: Long, val label: String, val secret: String)

/** What "create an app password" ended in. */
sealed interface AppPasswordResult {
    data class Created(val password: AppPassword) : AppPasswordResult

    /** The step-up sheet was dismissed, so nothing was created. */
    data object Cancelled : AppPasswordResult

    data class Failed(val message: String) : AppPasswordResult
}

private val wireJson = Json { ignoreUnknownKeys = true }

/**
 * App passwords for the signed-in account: an API key of the caller's
 * own principal (`POST /api/v1/principals/{id}/api-keys`), which another
 * mail client uses over IMAP and SMTP.
 *
 * Minting one is on the server's self-service elevation list
 * (REQ-AUTH-78), so for a TOTP-enrolled account the first attempt comes
 * back `step_up_required` and the six-digit sheet answers it before the
 * call is repeated (REQ-AND-AUTH-20).
 */
class AppPasswordClient(
    private val httpClient: HttpClient,
    private val baseUrl: String,
    private val calls: BearerCalls,
) {

    suspend fun create(label: String): AppPasswordResult {
        val principalId = try {
            principalId()
        } catch (t: Throwable) {
            return AppPasswordResult.Failed(t.message ?: "the account could not be identified")
        }
        val answered = try {
            calls.call { token ->
                httpClient.post("${baseUrl.trimEnd('/')}/api/v1/principals/$principalId/api-keys") {
                    header(HttpHeaders.Authorization, "Bearer $token")
                    contentType(ContentType.Application.Json)
                    setBody(
                        wireJson.encodeToString(
                            JsonObject.serializer(),
                            buildJsonObject { put("label", label) },
                        ),
                    )
                }
            }
        } catch (t: Throwable) {
            return AppPasswordResult.Failed(t.message ?: "cannot reach the server")
        }
        if (answered.stepUpRequired) return AppPasswordResult.Cancelled
        if (!answered.isSuccess) {
            return AppPasswordResult.Failed(
                answered.problemTitle.ifBlank { "the app password was refused (${answered.status})" },
            )
        }
        val body = runCatching { wireJson.parseToJsonElement(answered.body).jsonObject }.getOrNull()
            ?: return AppPasswordResult.Failed("the server's answer could not be read")
        val secret = body["key"]?.jsonPrimitive?.content.orEmpty()
        if (secret.isBlank()) return AppPasswordResult.Failed("the server returned no key")
        return AppPasswordResult.Created(
            AppPassword(
                id = body["id"]?.jsonPrimitive?.longOrNull ?: 0L,
                label = body["label"]?.jsonPrimitive?.content ?: label,
                secret = secret,
            ),
        )
    }

    /** The signed-in principal's numeric id (`GET /api/v1/auth/whoami`). */
    private suspend fun principalId(): Long {
        val answered = calls.call { token ->
            httpClient.get("${baseUrl.trimEnd('/')}/api/v1/auth/whoami") {
                header(HttpHeaders.Authorization, "Bearer $token")
            }
        }
        if (!answered.isSuccess) {
            throw SessionExpiredException("whoami failed (${answered.status})")
        }
        return runCatching {
            wireJson.parseToJsonElement(answered.body).jsonObject["principal_id"]?.jsonPrimitive?.longOrNull
        }.getOrNull() ?: throw SessionExpiredException("whoami carried no principal id")
    }
}
