package com.netzhansa.herold.shared.jmap

import com.netzhansa.herold.shared.auth.TokenStore
import io.ktor.client.HttpClient
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsBytes
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.add
import kotlinx.serialization.json.addJsonArray
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject

private val wireJson = Json {
    ignoreUnknownKeys = true
    encodeDefaults = true
    explicitNulls = false
}

/**
 * Typed JMAP-over-bearer-token transport
 * (docs/design/android/architecture/02-jmap-client.md). It batches method
 * calls into one `POST /jmap`, routes responses back by call id, and attaches
 * `Authorization: Bearer <token>` to every request including blob download
 * and EventSource. It owns no state strings and no reconciliation - that is
 * the sync engine.
 *
 * The session descriptor is fetched once and cached; capabilities are pinned
 * from it for the lifetime of the client.
 */
class JmapClient(
    private val httpClient: HttpClient,
    val baseUrl: String,
    private val tokenStore: TokenStore,
) : JmapApi {

    private val sessionMutex = Mutex()
    private var cachedSession: JmapSession? = null

    override suspend fun session(): JmapSession = sessionMutex.withLock {
        cachedSession ?: fetchSession().also { cachedSession = it }
    }

    /** Drops the cached descriptor so the next call re-reads capabilities and accounts. */
    suspend fun refreshSession(): JmapSession = sessionMutex.withLock {
        fetchSession().also { cachedSession = it }
    }

    private suspend fun fetchSession(): JmapSession {
        val response = authorizedGet("${baseUrl.trimEnd('/')}/.well-known/jmap")
        val text = response.bodyAsText()
        requireSuccess(response, text, "JMAP session")
        return wireJson.decodeFromString(JmapSession.serializer(), text)
    }

    override suspend fun mailboxGet(accountId: String, ids: List<String>?): GetResult<WireMailbox> {
        val args = buildJsonObject {
            put("accountId", accountId)
            if (ids == null) put("ids", JsonPrimitive(null as String?)) else putJsonArray("ids") {
                ids.forEach { add(it) }
            }
        }
        val result = call("Mailbox/get", args, listOf(Capability.CORE, Capability.MAIL))
        return result.toGetResult(WireMailbox.serializer())
    }

    override suspend fun mailboxChanges(accountId: String, sinceState: String): ChangesOutcome =
        changes("Mailbox/changes", accountId, sinceState, listOf(Capability.CORE, Capability.MAIL))

    override suspend fun emailQueryInbox(accountId: String, mailboxId: String, limit: Int): List<String> {
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonObject("filter") { put("inMailbox", mailboxId) }
            putJsonArray("sort") {
                addJsonObject {
                    put("property", "receivedAt")
                    put("isAscending", false)
                }
            }
            put("position", 0)
            put("limit", limit)
            put("calculateTotal", false)
        }
        val result = call("Email/query", args, listOf(Capability.CORE, Capability.MAIL))
        return result["ids"]?.jsonArray?.map { it.jsonPrimitive.content } ?: emptyList()
    }

    override suspend fun emailGet(
        accountId: String,
        ids: List<String>,
        withBody: Boolean,
    ): GetResult<WireEmail> {
        if (ids.isEmpty()) return GetResult(state = "", list = emptyList())
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonArray("ids") { ids.forEach { add(it) } }
            putJsonArray("properties") {
                METADATA_PROPERTIES.forEach { add(it) }
                if (withBody) BODY_PROPERTIES.forEach { add(it) }
            }
            if (withBody) {
                put("fetchHTMLBodyValues", true)
                put("fetchTextBodyValues", true)
                put("maxBodyValueBytes", MAX_BODY_VALUE_BYTES)
            }
        }
        val result = call("Email/get", args, listOf(Capability.CORE, Capability.MAIL, Capability.SNOOZE))
        return result.toGetResult(WireEmail.serializer())
    }

    override suspend fun emailChanges(accountId: String, sinceState: String): ChangesOutcome =
        changes("Email/changes", accountId, sinceState, listOf(Capability.CORE, Capability.MAIL))

    override suspend fun threadGet(accountId: String, ids: List<String>): GetResult<WireThread> {
        if (ids.isEmpty()) return GetResult(state = "", list = emptyList())
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonArray("ids") { ids.forEach { add(it) } }
        }
        val result = call("Thread/get", args, listOf(Capability.CORE, Capability.MAIL))
        return result.toGetResult(WireThread.serializer())
    }

    override suspend fun threadChanges(accountId: String, sinceState: String): ChangesOutcome =
        changes("Thread/changes", accountId, sinceState, listOf(Capability.CORE, Capability.MAIL))

    override suspend fun identityGet(accountId: String): GetResult<WireIdentity> {
        val args = buildJsonObject {
            put("accountId", accountId)
            put("ids", JsonPrimitive(null as String?))
        }
        val result = call("Identity/get", args, listOf(Capability.CORE, Capability.SUBMISSION))
        return result.toGetResult(WireIdentity.serializer())
    }

    override suspend fun identityChanges(accountId: String, sinceState: String): ChangesOutcome =
        changes("Identity/changes", accountId, sinceState, listOf(Capability.CORE, Capability.SUBMISSION))

    override suspend fun emailSet(accountId: String, update: Map<String, JsonObject>): SetOutcome {
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonObject("update") {
                update.forEach { (id, patch) -> put(id, patch) }
            }
        }
        val result = call(
            "Email/set",
            args,
            listOf(Capability.CORE, Capability.MAIL, Capability.SNOOZE),
        )
        val updated = result["updated"]?.jsonObject?.keys ?: emptySet()
        val notUpdated = result["notUpdated"]?.jsonObject?.mapValues { (_, value) ->
            value.jsonObject["description"]?.jsonPrimitive?.content
                ?: value.jsonObject["type"]?.jsonPrimitive?.content
                ?: "rejected"
        } ?: emptyMap()
        return SetOutcome(
            newState = result["newState"]?.jsonPrimitive?.content,
            updated = updated,
            notUpdated = notUpdated,
        )
    }

    override suspend fun derivedCategories(accountId: String): List<String> {
        if (!session().hasCapability(Capability.CATEGORISE)) return emptyList()
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonArray("ids") { add("singleton") }
        }
        val result = call("CategorySettings/get", args, listOf(Capability.CORE, Capability.CATEGORISE))
        val row = result["list"]?.jsonArray?.firstOrNull()?.jsonObject ?: return emptyList()
        return row["derivedCategories"]?.jsonArray?.map { it.jsonPrimitive.content } ?: emptyList()
    }

    override suspend fun downloadBlob(
        accountId: String,
        blobId: String,
        type: String,
        name: String,
    ): DownloadedBlob {
        val template = session().downloadUrl.ifBlank {
            "${baseUrl.trimEnd('/')}/jmap/download/{accountId}/{blobId}/{type}/{name}"
        }
        val url = template
            .replace("{accountId}", accountId.urlEncode())
            .replace("{blobId}", blobId.urlEncode())
            .replace("{type}", type.urlEncode())
            .replace("{name}", name.urlEncode())
        val response = authorizedGet(url)
        if (!response.status.isSuccess()) {
            throw JmapException("blob download failed: ${response.status}", status = response.status.value)
        }
        val contentType = response.headers[HttpHeaders.ContentType] ?: type
        return DownloadedBlob(contentType = contentType, bytes = response.bodyAsBytes())
    }

    /** The bearer token for the EventSource connection and other streamed reads. */
    suspend fun bearerToken(): String =
        tokenStore.currentToken() ?: throw JmapException("no bearer token stored", status = 401)

    private suspend fun changes(
        method: String,
        accountId: String,
        sinceState: String,
        using: List<String>,
    ): ChangesOutcome {
        val args = buildJsonObject {
            put("accountId", accountId)
            put("sinceState", sinceState)
            put("maxChanges", MAX_CHANGES)
        }
        val result = try {
            call(method, args, using)
        } catch (e: JmapException) {
            if (e.methodError == "cannotCalculateChanges") return ChangesOutcome.CannotCalculate
            throw e
        }
        return ChangesOutcome.Changed(
            newState = result["newState"]?.jsonPrimitive?.content.orEmpty(),
            created = result.idList("created"),
            updated = result.idList("updated"),
            destroyed = result.idList("destroyed"),
            hasMoreChanges = result["hasMoreChanges"]?.jsonPrimitive?.content?.toBoolean() ?: false,
        )
    }

    /**
     * Issues one method call in its own batch and returns its arguments.
     * Multi-call batches with back-references are assembled by the sync
     * engine through [batch].
     */
    private suspend fun call(method: String, args: JsonObject, using: List<String>): JsonObject {
        val responses = batch(listOf(MethodCall(method, args, "c0")), using)
        return responses.single().args
    }

    /** One method call in a batch: name, arguments, call id (RFC 8620 section 3.2). */
    data class MethodCall(val name: String, val args: JsonObject, val id: String)

    /** One response of a batch, routed back by [id]. */
    data class MethodResponse(val name: String, val args: JsonObject, val id: String)

    suspend fun batch(calls: List<MethodCall>, using: List<String>): List<MethodResponse> {
        val apiUrl = session().apiUrl.ifBlank { "${baseUrl.trimEnd('/')}/jmap" }
        val body = buildJsonObject {
            putJsonArray("using") { using.distinct().forEach { add(it) } }
            put(
                "methodCalls",
                buildJsonArray {
                    calls.forEach { methodCall ->
                        addJsonArray {
                            add(methodCall.name)
                            add(methodCall.args)
                            add(methodCall.id)
                        }
                    }
                },
            )
        }
        val token = bearerToken()
        val response = httpClient.post(apiUrl) {
            header(HttpHeaders.Authorization, "Bearer $token")
            contentType(ContentType.Application.Json)
            setBody(wireJson.encodeToString(JsonObject.serializer(), body))
        }
        val text = response.bodyAsText()
        requireSuccess(response, text, "JMAP request")

        val methodResponses = wireJson.parseToJsonElement(text).jsonObject["methodResponses"]?.jsonArray
            ?: throw JmapException("JMAP response carried no methodResponses")
        val parsed = methodResponses.map { element ->
            val entry = element.jsonArray
            MethodResponse(
                name = entry[0].jsonPrimitive.content,
                args = entry[1].jsonObject,
                id = entry.getOrNull(2)?.jsonPrimitive?.content.orEmpty(),
            )
        }
        parsed.firstOrNull { it.name == "error" }?.let { errorResponse ->
            val type = errorResponse.args["type"]?.jsonPrimitive?.content ?: "unknownMethod"
            throw JmapException(
                "JMAP method error: $type",
                methodError = type,
            )
        }
        return parsed
    }

    private suspend fun authorizedGet(url: String): HttpResponse {
        val token = bearerToken()
        return httpClient.get(url) { header(HttpHeaders.Authorization, "Bearer $token") }
    }

    private fun requireSuccess(response: HttpResponse, body: String, what: String) {
        if (!response.status.isSuccess()) {
            throw JmapException("$what failed: ${response.status} $body", status = response.status.value)
        }
    }

    private fun <T> JsonObject.toGetResult(
        serializer: kotlinx.serialization.KSerializer<T>,
    ): GetResult<T> = GetResult(
        state = this["state"]?.jsonPrimitive?.content.orEmpty(),
        list = (this["list"] as? JsonArray)?.map { wireJson.decodeFromJsonElement(serializer, it) } ?: emptyList(),
        notFound = idList("notFound"),
    )

    private fun JsonObject.idList(key: String): List<String> =
        (this[key] as? JsonArray)?.map { it.jsonPrimitive.content } ?: emptyList()

    companion object {
        private const val MAX_CHANGES = 256
        private const val MAX_BODY_VALUE_BYTES = 512 * 1024

        private val METADATA_PROPERTIES = listOf(
            "id", "blobId", "threadId", "mailboxIds", "keywords", "from", "to",
            "subject", "receivedAt", "size", "preview", "hasAttachment", "snoozedUntil",
        )
        private val BODY_PROPERTIES = listOf("htmlBody", "textBody", "attachments", "bodyValues")
    }
}

private fun String.urlEncode(): String = buildString {
    this@urlEncode.encodeToByteArray().forEach { byte ->
        val value = byte.toInt() and 0xFF
        val char = value.toChar()
        if (char.isLetterOrDigit() || char in "-_.~") {
            append(char)
        } else {
            append('%')
            append(value.toString(16).uppercase().padStart(2, '0'))
        }
    }
}
