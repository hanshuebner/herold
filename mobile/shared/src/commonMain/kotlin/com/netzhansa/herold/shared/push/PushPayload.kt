package com.netzhansa.herold.shared.push

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

private val payloadJson = Json { ignoreUnknownKeys = true }

/**
 * The event class a push payload carries. herold's dispatcher stamps it as
 * the payload's `kind` (`internal/webpush/payload.go`); it selects both the
 * notification channel (REQ-AND-PUSH-10) and the action set.
 */
enum class PushKind(val wire: String) {
    MAIL("mail"),
    CHAT("chat"),
    CALENDAR_INVITE("calendar-invite"),
    CALL("call"),
    REACTION("reaction"),
    ;

    companion object {
        fun fromWire(value: String?): PushKind? = entries.firstOrNull { it.wire == value }
    }
}

/**
 * A decoded push payload: the RFC 8620 section 7.3 `StateChange` envelope
 * plus the bounded preview fields herold adds so a notification renders
 * without a follow-up `/get` (suite REQ-PUSH-40..47, architecture
 * `04-push.md`). Bodies are never carried, only the server's 80-byte
 * preview (suite REQ-PUSH-92).
 *
 * The account id is read from the `changed` map's single key rather than a
 * field of its own — that is where the dispatcher puts it, and the Suite's
 * service worker reads it the same way.
 */
data class PushEnvelope(
    val kind: PushKind?,
    val accountId: String?,
    val changedTypes: List<String>,
    val from: String = "",
    val subject: String = "",
    val preview: String = "",
    val emailId: String? = null,
    val threadId: String? = null,
    val inboxMailboxId: String? = null,
    val conversationId: String? = null,
) {
    companion object {
        /**
         * Parses the JSON the FCM data field `payload` carries. Returns null
         * when the text is not a JSON object, so a malformed message is
         * dropped rather than crashing the messaging service.
         */
        fun parse(text: String): PushEnvelope? {
            val root = runCatching { payloadJson.parseToJsonElement(text).jsonObject }.getOrNull()
                ?: return null
            val changed = (root["changed"] as? JsonObject)
            val accountId = changed?.keys?.firstOrNull()
            val changedTypes = accountId
                ?.let { (changed[it] as? JsonObject)?.keys?.toList() }
                ?: emptyList()
            return PushEnvelope(
                kind = PushKind.fromWire(root.string("kind")),
                accountId = accountId,
                changedTypes = changedTypes,
                from = root.string("from").orEmpty(),
                subject = root.string("subject") ?: root.string("body").orEmpty(),
                preview = root.string("preview").orEmpty(),
                emailId = root.string("emailId") ?: root.string("msgid"),
                threadId = root.string("threadId"),
                inboxMailboxId = root.string("inboxMailboxId"),
                conversationId = root.string("conversationId"),
            )
        }

        /** The FCM data key the dispatcher puts the payload under. */
        const val DATA_KEY = "payload"

        /** Reads the payload out of an FCM message's data map. */
        fun fromData(data: Map<String, String>): PushEnvelope? =
            data[DATA_KEY]?.let { parse(it) }
    }
}

private fun JsonObject.string(key: String): String? =
    this[key]?.let { runCatching { it.jsonPrimitive.content }.getOrNull() }?.takeIf { it.isNotBlank() }
