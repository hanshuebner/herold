package com.netzhansa.herold.shared.jmap

import com.netzhansa.herold.shared.auth.SessionExpiredException
import com.netzhansa.herold.shared.auth.StoredTokenProvider
import com.netzhansa.herold.shared.auth.TokenProvider
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
import kotlinx.serialization.json.contentOrNull
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
    private val tokens: TokenProvider,
) : JmapApi {

    /** A client over a token that cannot be refreshed (tooling, tests). */
    constructor(httpClient: HttpClient, baseUrl: String, tokenStore: TokenStore) :
        this(httpClient, baseUrl, StoredTokenProvider(tokenStore))

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

    /**
     * `PushSubscription/set`. The method is session-scoped (RFC 8620
     * section 7.2), so it carries no `accountId`: a subscription belongs to
     * the authenticated principal and covers every account of the session.
     */
    override suspend fun pushSubscriptionSet(
        create: PushSubscriptionCreate?,
        update: Map<String, JsonObject>,
        destroy: List<String>,
    ): PushSetOutcome {
        val args = buildJsonObject {
            if (create != null) {
                putJsonObject("create") { put(PUSH_CREATE_KEY, create.toWire(wireJson)) }
            }
            if (update.isNotEmpty()) {
                putJsonObject("update") { update.forEach { (id, patch) -> put(id, patch) } }
            }
            if (destroy.isNotEmpty()) {
                putJsonArray("destroy") { destroy.forEach { add(it) } }
            }
        }
        val result = call("PushSubscription/set", args, listOf(Capability.CORE))
        val created = (result["created"] as? JsonObject)?.get(PUSH_CREATE_KEY)?.jsonObject
        val notCreated = (result["notCreated"] as? JsonObject)?.get(PUSH_CREATE_KEY)?.jsonObject
        return PushSetOutcome(
            createdId = created?.get("id")?.jsonPrimitive?.content,
            verificationCode = created?.get("verificationCode")?.jsonPrimitive?.content,
            notCreated = notCreated?.let {
                it["description"]?.jsonPrimitive?.content
                    ?: it["type"]?.jsonPrimitive?.content
                    ?: "push registration rejected"
            },
            destroyed = result.idList("destroyed"),
        )
    }

    override suspend fun emailQuery(
        accountId: String,
        filter: JsonObject,
        limit: Int,
        collapseThreads: Boolean,
    ): List<String> {
        val args = buildJsonObject {
            put("accountId", accountId)
            put("filter", filter)
            put("collapseThreads", collapseThreads)
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
        return result.idList("ids")
    }

    override suspend fun searchSnippets(
        accountId: String,
        filter: JsonObject,
        emailIds: List<String>,
    ): List<WireSnippet> {
        if (emailIds.isEmpty()) return emptyList()
        val args = buildJsonObject {
            put("accountId", accountId)
            put("filter", filter)
            putJsonArray("emailIds") { emailIds.forEach { add(it) } }
        }
        val result = call("SearchSnippet/get", args, listOf(Capability.CORE, Capability.MAIL))
        return (result["list"] as? JsonArray)?.map {
            wireJson.decodeFromJsonElement(WireSnippet.serializer(), it)
        } ?: emptyList()
    }

    override suspend fun seenAddresses(accountId: String): List<WireSeenAddress> {
        val args = buildJsonObject {
            put("accountId", accountId)
            put("ids", JsonPrimitive(null as String?))
        }
        val result = call("SeenAddress/get", args, listOf(Capability.CORE, Capability.MAIL))
        return (result["list"] as? JsonArray)?.map {
            wireJson.decodeFromJsonElement(WireSeenAddress.serializer(), it)
        } ?: emptyList()
    }

    override suspend fun uploadBlob(
        accountId: String,
        bytes: ByteArray,
        type: String,
        filename: String?,
    ): UploadedBlob {
        val template = session().uploadUrl.ifBlank {
            "${baseUrl.trimEnd('/')}/jmap/upload/{accountId}"
        }
        val url = template.replace("{accountId}", accountId.urlEncode())
        val response = authorized { token ->
            httpClient.post(url) {
                header(HttpHeaders.Authorization, "Bearer $token")
                header(HttpHeaders.ContentType, type.ifBlank { "application/octet-stream" })
                setBody(bytes)
            }
        }
        val text = response.bodyAsText()
        requireSuccess(response, text, "blob upload")
        return wireJson.decodeFromString(UploadedBlob.serializer(), text)
    }

    override suspend fun emailCreate(accountId: String, email: JsonObject): EmailWriteOutcome {
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonObject("create") { put(DRAFT_CREATE_KEY, email) }
        }
        val result = call("Email/set", args, listOf(Capability.CORE, Capability.MAIL))
        val created = (result["created"] as? JsonObject)?.get(DRAFT_CREATE_KEY)?.jsonObject
        return EmailWriteOutcome(
            id = created?.get("id")?.jsonPrimitive?.content,
            newState = result["newState"]?.jsonPrimitive?.content,
            error = if (created != null) {
                null
            } else {
                (result["notCreated"] as? JsonObject)?.get(DRAFT_CREATE_KEY)?.jsonObject.describe()
                    ?: "the server did not create the draft"
            },
        )
    }

    override suspend fun emailReplace(accountId: String, id: String, email: JsonObject): EmailWriteOutcome {
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonObject("update") { put(id, email) }
        }
        val result = call("Email/set", args, listOf(Capability.CORE, Capability.MAIL))
        val failure = (result["notUpdated"] as? JsonObject)?.get(id)?.jsonObject
        return EmailWriteOutcome(
            id = id,
            newState = result["newState"]?.jsonPrimitive?.content,
            error = failure.describe(),
        )
    }

    override suspend fun emailDestroy(accountId: String, ids: List<String>) {
        if (ids.isEmpty()) return
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonArray("destroy") { ids.forEach { add(it) } }
        }
        call("Email/set", args, listOf(Capability.CORE, Capability.MAIL))
    }

    override suspend fun sendEmail(
        accountId: String,
        email: JsonObject,
        draftId: String?,
        identityId: String,
        envelope: Envelope,
        onSuccessUpdate: JsonObject,
        parentId: String?,
        parentKeyword: String?,
    ): SubmissionOutcome {
        val setArgs = buildJsonObject {
            put("accountId", accountId)
            if (draftId == null) {
                putJsonObject("create") { put(DRAFT_CREATE_KEY, email) }
            }
            putJsonObject("update") {
                if (draftId != null) put(draftId, email)
                if (parentId != null && parentKeyword != null) {
                    put(
                        parentId,
                        buildJsonObject { put("keywords/$parentKeyword", true) },
                    )
                }
            }
        }
        val emailRef = draftId ?: "#$DRAFT_CREATE_KEY"
        val submissionArgs = buildJsonObject {
            put("accountId", accountId)
            putJsonObject("create") {
                putJsonObject(SUBMISSION_CREATE_KEY) {
                    put("emailId", emailRef)
                    put("identityId", identityId)
                    putJsonObject("envelope") {
                        putJsonObject("mailFrom") { put("email", envelope.mailFrom) }
                        putJsonArray("rcptTo") {
                            envelope.rcptTo.forEach { addJsonObject { put("email", it) } }
                        }
                    }
                    put("sendAt", JsonPrimitive(null as String?))
                }
            }
            putJsonObject("onSuccessUpdateEmail") {
                put("#$SUBMISSION_CREATE_KEY", onSuccessUpdate)
            }
        }
        val responses = batch(
            listOf(
                MethodCall("Email/set", setArgs, "s0"),
                MethodCall("EmailSubmission/set", submissionArgs, "s1"),
            ),
            listOf(Capability.CORE, Capability.MAIL, Capability.SUBMISSION),
        )
        val setResult = responses.first { it.id == "s0" }.args
        val createdEmail = (setResult["created"] as? JsonObject)?.get(DRAFT_CREATE_KEY)?.jsonObject
        val notCreatedEmail = (setResult["notCreated"] as? JsonObject)?.get(DRAFT_CREATE_KEY)?.jsonObject
        val notUpdatedEmail = draftId?.let { (setResult["notUpdated"] as? JsonObject)?.get(it)?.jsonObject }
        val emailError = notCreatedEmail.describe() ?: notUpdatedEmail.describe()
        if (emailError != null) return SubmissionOutcome(null, null, emailError)

        val subResult = responses.first { it.id == "s1" }.args
        val created = (subResult["created"] as? JsonObject)?.get(SUBMISSION_CREATE_KEY)?.jsonObject
        val failure = (subResult["notCreated"] as? JsonObject)?.get(SUBMISSION_CREATE_KEY)?.jsonObject
        return SubmissionOutcome(
            submissionId = created?.get("id")?.jsonPrimitive?.content,
            emailId = created?.get("emailId")?.jsonPrimitive?.content
                ?: createdEmail?.get("id")?.jsonPrimitive?.content
                ?: draftId,
            error = failure.describe(),
        )
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

    override suspend fun principalAvatarBlobId(accountId: String, email: String): String? {
        if (email.isBlank()) return null
        val using = listOf(Capability.CORE, Capability.CHAT)
        val responses = try {
            batch(
                listOf(
                    MethodCall(
                        "Principal/query",
                        buildJsonObject {
                            put("accountId", accountId)
                            putJsonObject("filter") { put("emailExact", email) }
                        },
                        "p0",
                    ),
                    MethodCall(
                        "Principal/get",
                        buildJsonObject {
                            put("accountId", accountId)
                            // RFC 8620 section 3.7: the ids of the query
                            // above, so the lookup is one round trip.
                            putJsonObject("#ids") {
                                put("resultOf", "p0")
                                put("name", "Principal/query")
                                put("path", "/ids")
                            }
                            putJsonArray("properties") {
                                add("id")
                                add("email")
                                add("avatarBlobId")
                            }
                        },
                        "p1",
                    ),
                ),
                using,
            )
        } catch (t: Throwable) {
            // No chat capability, no principal directory, no connection -
            // the caller falls back to the initials avatar.
            return null
        }
        val list = responses.firstOrNull { it.id == "p1" }?.args?.get("list") as? JsonArray
        return list?.firstNotNullOfOrNull { element ->
            (element as? JsonObject)?.get("avatarBlobId")?.jsonPrimitive?.contentOrNull?.takeIf { it.isNotBlank() }
        }
    }

    /** The bearer token for the EventSource connection and other streamed reads. */
    suspend fun bearerToken(): String = try {
        tokens.accessToken()
    } catch (expired: SessionExpiredException) {
        throw JmapException(expired.message ?: "no bearer token stored", status = 401)
    }

    /**
     * Sends [request] with a bearer token and, if the server refuses it,
     * refreshes once and sends it again (REQ-AND-AUTH-04). One retry: a
     * 401 on the refreshed token means the session is gone, not that
     * another refresh would help.
     */
    private suspend fun authorized(request: suspend (String) -> HttpResponse): HttpResponse {
        val token = bearerToken()
        val response = request(token)
        if (response.status.value != UNAUTHORIZED) return response
        val refreshed = try {
            tokens.refreshAfterUnauthorized(token)
        } catch (expired: SessionExpiredException) {
            return response
        } ?: return response
        return request(refreshed)
    }

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
        val response = authorized { token ->
            httpClient.post(apiUrl) {
                header(HttpHeaders.Authorization, "Bearer $token")
                contentType(ContentType.Application.Json)
                setBody(wireJson.encodeToString(JsonObject.serializer(), body))
            }
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

    private suspend fun authorizedGet(url: String): HttpResponse = authorized { token ->
        httpClient.get(url) { header(HttpHeaders.Authorization, "Bearer $token") }
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

    /** The human-readable reason a `Foo/set` entry failed, or null when it did not. */
    private fun JsonObject?.describe(): String? {
        if (this == null) return null
        return this["description"]?.jsonPrimitive?.content
            ?: this["type"]?.jsonPrimitive?.content
            ?: "rejected"
    }

    private fun JsonObject.idList(key: String): List<String> =
        (this[key] as? JsonArray)?.map { it.jsonPrimitive.content } ?: emptyList()

    companion object {
        /** The creation key the create/notCreated maps are routed back by. */
        private const val PUSH_CREATE_KEY = "push0"

        /** Creation keys the draft and its submission are routed back by. */
        private const val DRAFT_CREATE_KEY = "draft1"
        private const val SUBMISSION_CREATE_KEY = "sub1"
        private const val MAX_CHANGES = 256
        private const val UNAUTHORIZED = 401
        private const val MAX_BODY_VALUE_BYTES = 512 * 1024

        private val METADATA_PROPERTIES = listOf(
            "id", "blobId", "threadId", "mailboxIds", "keywords", "from", "to", "cc",
            "replyTo", "messageId", "inReplyTo", "references", "sentAt",
            "header:X-Herold-Recipient:asText",
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
