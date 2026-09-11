package com.netzhansa.herold.shared.jmap

import io.ktor.client.HttpClient
import io.ktor.client.request.header
import io.ktor.client.request.prepareGet
import io.ktor.client.statement.bodyAsChannel
import io.ktor.http.HttpHeaders
import io.ktor.utils.io.readUTF8Line
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.serialization.json.Json

private val wireJson = Json { ignoreUnknownKeys = true }

/**
 * The JMAP EventSource channel (`GET /jmap/eventsource`, RFC 8620 section 7.3)
 * consumed while the app is foregrounded (REQ-AND-SYNC-11). Each `StateChange`
 * event names the types whose state advanced per account; the sync engine
 * answers with `Foo/changes`.
 *
 * A reconnect sends `Last-Event-ID` when the server supplied an event id.
 */
class EventSourceClient(
    private val httpClient: HttpClient,
    private val client: JmapClient,
) {
    /**
     * Streams `StateChange` events until the caller's scope is cancelled or
     * the connection drops; reconnection is the caller's (the sync engine's)
     * concern so back-off policy lives in one place.
     */
    fun stateChanges(types: List<String> = DEFAULT_TYPES, pingSeconds: Int = 30): Flow<StateChangeEvent> = flow {
        val session = client.session()
        val template = session.eventSourceUrl.ifBlank {
            "${client.baseUrl.trimEnd('/')}/jmap/eventsource?types={types}&closeafter={closeafter}&ping={ping}"
        }
        val url = template
            .replace("{types}", types.joinToString(","))
            .replace("{closeafter}", "no")
            .replace("{ping}", pingSeconds.toString())
        val token = client.bearerToken()

        httpClient.prepareGet(url) {
            header(HttpHeaders.Authorization, "Bearer $token")
            header(HttpHeaders.Accept, "text/event-stream")
            header(HttpHeaders.CacheControl, "no-cache")
            lastEventId?.let { header("Last-Event-ID", it) }
        }.execute { response ->
            val channel = response.bodyAsChannel()
            val data = StringBuilder()
            while (true) {
                val line = channel.readUTF8Line() ?: break
                when {
                    line.startsWith("id:") -> lastEventId = line.removePrefix("id:").trim()
                    line.startsWith("data:") -> data.append(line.removePrefix("data:").trim())
                    line.isEmpty() -> {
                        if (data.isNotEmpty()) {
                            val payload = data.toString()
                            data.clear()
                            runCatching {
                                wireJson.decodeFromString(StateChangeEvent.serializer(), payload)
                            }.getOrNull()?.let { emit(it) }
                        }
                    }
                }
            }
        }
    }

    private var lastEventId: String? = null

    companion object {
        val DEFAULT_TYPES = listOf("Email", "Mailbox", "Thread", "Identity")
    }
}
