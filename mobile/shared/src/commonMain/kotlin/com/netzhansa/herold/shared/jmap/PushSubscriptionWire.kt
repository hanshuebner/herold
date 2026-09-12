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
 * registration sends none and the server's REQ-PUSH-81 defaults apply -
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
 * Which transport herold reaches this install through
 * (`kind` on `PushSubscription/set`, server `internal/store/types_push.go`).
 * FCM carries a device registration token; UnifiedPush carries a
 * distributor-supplied endpoint URL and the RFC 8291 keys the device
 * decrypts with (REQ-AND-PUSH-04).
 */
enum class PushTransport(val wire: String) {
    FCM("fcm"),
    UNIFIED_PUSH("unifiedpush"),
    ;

    companion object {
        fun fromWire(value: String?): PushTransport? = entries.firstOrNull { it.wire == value }
    }
}

/**
 * A `PushSubscription/set { create }` object (REQ-AND-PUSH-01/04,
 * server-contract section Push). [types] mirrors what the Suite registers
 * so every client receives the same event classes, and [identity] is what
 * a rotation changes - the FCM token or the distributor endpoint - which
 * is how the registrar notices it must register anew.
 */
sealed interface PushSubscriptionCreate {
    val deviceClientId: String
    val transport: PushTransport
    val types: List<String>
    val notificationRules: NotificationRules?
    val quietHours: QuietHours?
    val identity: String

    companion object {
        /** The event types the Suite's registration asks for. */
        val DEFAULT_TYPES = listOf("Email", "Message", "Conversation")
    }
}

/** `kind: "fcm"`: a device registration token, no endpoint and no keys. */
data class FcmSubscriptionCreate(
    override val deviceClientId: String,
    val fcmToken: String,
    override val types: List<String> = PushSubscriptionCreate.DEFAULT_TYPES,
    override val notificationRules: NotificationRules? = null,
    override val quietHours: QuietHours? = null,
) : PushSubscriptionCreate {
    override val transport = PushTransport.FCM
    override val identity: String get() = fcmToken

    companion object {
        val DEFAULT_TYPES = PushSubscriptionCreate.DEFAULT_TYPES
    }
}

/**
 * `kind: "unifiedpush"`: the endpoint the distributor handed out plus the
 * RFC 8291 public key and auth secret, in the shape Web Push registers
 * (server `internal/protojmap/push/methods.go` buildEndpointCreateRow).
 * herold POSTs the aes128gcm envelope to [endpoint] with no VAPID header.
 */
data class UnifiedPushSubscriptionCreate(
    override val deviceClientId: String,
    val endpoint: String,
    val p256dh: String,
    val auth: String,
    override val types: List<String> = PushSubscriptionCreate.DEFAULT_TYPES,
    override val notificationRules: NotificationRules? = null,
    override val quietHours: QuietHours? = null,
) : PushSubscriptionCreate {
    override val transport = PushTransport.UNIFIED_PUSH

    /** Both halves: a distributor that re-keys the same URL is a rotation too. */
    override val identity: String get() = "$endpoint|$p256dh"
}

/** The outcome of a `PushSubscription/set` call. */
data class PushSetOutcome(
    val createdId: String?,
    /**
     * The RFC 8620 section 7.2 verification code, when herold returned it on
     * the created object. Echoing it back is what makes the subscription
     * eligible for delivery; the same code also arrives over the push
     * channel as a `PushVerification` handshake.
     */
    val verificationCode: String?,
    val notCreated: String?,
    val destroyed: List<String>,
)

/** Renders a create object as the JMAP wire arguments. */
internal fun PushSubscriptionCreate.toWire(json: kotlinx.serialization.json.Json): JsonObject =
    buildJsonObject {
        put("deviceClientId", deviceClientId)
        put("kind", transport.wire)
        when (val create = this@toWire) {
            is FcmSubscriptionCreate -> put("fcmToken", create.fcmToken)
            is UnifiedPushSubscriptionCreate -> {
                put("url", create.endpoint)
                put(
                    "keys",
                    buildJsonObject {
                        put("p256dh", create.p256dh)
                        put("auth", create.auth)
                    },
                )
            }
        }
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
