package com.netzhansa.herold.shared.jmap

import com.netzhansa.herold.shared.auth.TokenStore
import com.netzhansa.herold.shared.domain.Mailbox
import io.ktor.client.HttpClient
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.add
import kotlinx.serialization.json.addJsonArray
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray

/** Raised on any transport or shape failure this Phase 0 slice cannot recover from. */
class JmapClientException(message: String) : Exception(message)

private val wireJson = Json { ignoreUnknownKeys = true }

/**
 * Typed JMAP-over-bearer-token client
 * (docs/design/android/architecture/02-jmap-client.md). Phase 0 narrows this
 * to what the authenticate-and-render-mailbox-list slice needs: the
 * device-token credential exchange, one JMAP session fetch, and one
 * `Mailbox/get` call. The full batching/back-reference model, blob
 * upload/download, and EventSource are later phases; the sync engine
 * (`03-sync-and-state.md`), not this class, will own state strings and the
 * outbox once Phase 1 lands.
 */
class JmapClient(
    private val httpClient: HttpClient,
    private val baseUrl: String,
    private val tokenStore: TokenStore,
) {
    /**
     * Bearer-token bootstrap via herold's device-token exchange
     * (`POST /api/v1/auth/device-token`, docs/design/android/
     * notes/server-prerequisites.md #199). This is the Phase 0 stand-in for
     * the full OAuth2 authorization-code + PKCE grant
     * (REQ-AND-AUTH-01/02) -- both mint the same `hk_...` bearer token; the
     * browser-driven grant is a later increment. The token is stored via the
     * `auth/` seam ([TokenStore]) and never logged.
     */
    suspend fun signIn(email: String, password: String): String {
        val requestBody = buildJsonObject {
            put("email", email)
            put("password", password)
        }
        val response = httpClient.post("$baseUrl/api/v1/auth/device-token") {
            contentType(ContentType.Application.Json)
            setBody(wireJson.encodeToString(requestBody))
        }
        val text = response.bodyAsText()
        requireSuccess(response, text, "device-token exchange")

        val token = wireJson.parseToJsonElement(text).jsonObject["token"]?.jsonPrimitive?.content
            ?: throw JmapClientException("device-token response missing 'token' field: $text")

        tokenStore.store(token)
        return token
    }

    /**
     * Opens a JMAP session (`GET /.well-known/jmap`) and calls `Mailbox/get`
     * for the account's primary mail account, returning the mailbox list as
     * domain models (`../architecture/01-system-overview.md` "Bootstrap"
     * steps 3-4, narrowed to a single read for this slice).
     */
    suspend fun fetchMailboxes(): List<Mailbox> {
        val token = tokenStore.currentToken()
            ?: throw JmapClientException("fetchMailboxes called before signIn: no bearer token stored")

        val sessionResponse = httpClient.get("$baseUrl/.well-known/jmap") {
            header(HttpHeaders.Authorization, "Bearer $token")
        }
        val sessionText = sessionResponse.bodyAsText()
        requireSuccess(sessionResponse, sessionText, "JMAP session fetch")

        val sessionJson = wireJson.parseToJsonElement(sessionText).jsonObject
        val apiUrl = sessionJson["apiUrl"]?.jsonPrimitive?.content
            ?: throw JmapClientException("session descriptor missing 'apiUrl': $sessionText")
        val accountId = sessionJson["primaryAccounts"]?.jsonObject
            ?.get("urn:ietf:params:jmap:mail")?.jsonPrimitive?.content
            ?: throw JmapClientException("session descriptor missing a primary mail account: $sessionText")

        val requestBody = buildJsonObject {
            putJsonArray("using") {
                add(JsonPrimitive("urn:ietf:params:jmap:core"))
                add(JsonPrimitive("urn:ietf:params:jmap:mail"))
            }
            putJsonArray("methodCalls") {
                addJsonArray {
                    add(JsonPrimitive("Mailbox/get"))
                    addJsonObject {
                        put("accountId", accountId)
                    }
                    add(JsonPrimitive("c1"))
                }
            }
        }

        val apiResponse = httpClient.post(apiUrl) {
            header(HttpHeaders.Authorization, "Bearer $token")
            contentType(ContentType.Application.Json)
            setBody(wireJson.encodeToString(requestBody))
        }
        val apiText = apiResponse.bodyAsText()
        requireSuccess(apiResponse, apiText, "Mailbox/get")

        val methodResponses = wireJson.parseToJsonElement(apiText).jsonObject["methodResponses"]?.jsonArray
            ?: throw JmapClientException("Mailbox/get response missing 'methodResponses': $apiText")
        val mailboxGetResult = methodResponses
            .map { it.jsonArray }
            .firstOrNull { it.getOrNull(0)?.jsonPrimitive?.content == "Mailbox/get" }
            ?.getOrNull(1)?.jsonObject
            ?: throw JmapClientException("Mailbox/get call absent from response: $apiText")

        val list = mailboxGetResult["list"]?.jsonArray ?: return emptyList()
        return list.map { element ->
            val mailbox = element.jsonObject
            Mailbox(
                id = mailbox["id"]?.jsonPrimitive?.content.orEmpty(),
                name = mailbox["name"]?.jsonPrimitive?.content.orEmpty(),
                unreadEmails = mailbox["unreadEmails"]?.jsonPrimitive?.content?.toIntOrNull() ?: 0,
                totalEmails = mailbox["totalEmails"]?.jsonPrimitive?.content?.toIntOrNull() ?: 0,
            )
        }
    }

    private fun requireSuccess(response: HttpResponse, body: String, what: String) {
        if (!response.status.isSuccess()) {
            throw JmapClientException("$what failed: ${response.status} $body")
        }
    }
}
