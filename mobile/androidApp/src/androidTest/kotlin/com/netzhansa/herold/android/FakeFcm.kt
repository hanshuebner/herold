package com.netzhansa.herold.android

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import java.net.HttpURLConnection
import java.net.URL

/**
 * The in-tree fake FCM every dev instance runs (`scripts/dev-instance.sh`,
 * re #334), read through its status API. It records what herold's
 * dispatcher actually sent, so a test drives the device with the server's
 * own bytes rather than a fixture.
 */
object FakeFcm {

    /** One recorded send: the token it went to and the data map it carried. */
    data class Message(
        val token: String,
        val data: Map<String, String>,
        val priority: String,
    ) {
        /** The JMAP payload the dispatcher put in the `payload` data key. */
        val payload: String? get() = data["payload"]
    }

    /** Drops what the fake has recorded so far. */
    fun clear(addr: String) {
        val connection = URL("http://$addr/messages").openConnection() as HttpURLConnection
        connection.requestMethod = "DELETE"
        connection.inputStream.use { it.readBytes() }
    }

    /** Waits for a recorded send that matches, or null when none arrives. */
    fun await(
        addr: String,
        timeoutMs: Long = TIMEOUT_MS,
        matches: (Message) -> Boolean,
    ): Message? {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            messages(addr).firstOrNull(matches)?.let { return it }
            Thread.sleep(POLL_MS)
        }
        return null
    }

    fun messages(addr: String): List<Message> {
        val text = URL("http://$addr/messages").readText()
        return Json.parseToJsonElement(text).jsonArray.map { element ->
            val row = element.jsonObject
            Message(
                token = row["token"]?.jsonPrimitive?.content.orEmpty(),
                data = row["data"]?.jsonObject.orEmpty()
                    .mapValues { (_, value) -> value.jsonPrimitive.content },
                priority = row["android_priority"]?.jsonPrimitive?.content.orEmpty(),
            )
        }
    }

    private const val TIMEOUT_MS = 30_000L
    private const val POLL_MS = 250L
}
