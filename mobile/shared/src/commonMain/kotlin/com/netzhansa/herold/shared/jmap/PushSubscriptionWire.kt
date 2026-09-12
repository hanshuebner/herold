package com.netzhansa.herold.shared.jmap

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * The per-account quiet-hours pair a push subscription may carry
 * (server `internal/protojmap/push/types.go` jmapQuietHours, suite
 * REQ-PUSH-83). Hours are local to [tz].
 */
@Serializable
data class QuietHours(
    val startHourLocal: Int,
    val endHourLocal: Int,
    val tz: String,
)

/**
 * The notification-rule shape herold's dispatcher parses
 * (`internal/webpush/rules.go` rulesWire). Every field is optional; an
 * omitted field leaves the server's default in place, so the client only
 * sends what the user has configured. Milestone 1b has no rules UI, so the
 * registration sends none and the server's REQ-PUSH-81 defaults apply —
 * the same thing the Suite's registration does.
 */
@Serializable
data class NotificationRules(
    val master: Boolean? = null,
    val perEventType: Map<String, Boolean>? = null,
    val mailCategories: List<String>? = null,
    val quietHours: QuietHours? = null,
    val quietHoursOverride: Map<String, Boolean>? = null,
)

/**
 * A `PushSubscription/set { create }` object for the FCM transport
 * (REQ-AND-PUSH-01, server-contract section Push). `kind: "fcm"` carries a
 * device registration token where Web Push carries `url` + `keys`; herold
 * rejects a create that mixes the two.
 *
 * [types] mirrors what the Suite registers so the two clients receive the
 * same event classes.
 */
data class FcmSubscriptionCreate(
    val deviceClientId: String,
    val fcmToken: String,
    val types: List<String> = DEFAULT_TYPES,
    val notificationRules: NotificationRules? = null,
    val quietHours: QuietHours? = null,
) {
    companion object {
        /** The event types the Suite's registration asks for. */
        val DEFAULT_TYPES = listOf("Email", "Message", "Conversation")
    }
}

/** The outcome of a `PushSubscription/set` call. */
data class PushSetOutcome(
    val createdId: String?,
    val notCreated: String?,
    val destroyed: List<String>,
)

/** Renders a create object as the JMAP wire arguments. */
internal fun FcmSubscriptionCreate.toWire(json: kotlinx.serialization.json.Json): JsonObject =
    buildJsonObject {
        put("deviceClientId", deviceClientId)
        put("kind", "fcm")
        put("fcmToken", fcmToken)
        put(
            "types",
            kotlinx.serialization.json.buildJsonArray {
                types.forEach { add(kotlinx.serialization.json.JsonPrimitive(it)) }
            },
        )
        notificationRules?.let {
            put("notificationRules", json.encodeToJsonElement(NotificationRules.serializer(), it))
        }
        quietHours?.let {
            put("quietHours", json.encodeToJsonElement(QuietHours.serializer(), it))
        }
    }
