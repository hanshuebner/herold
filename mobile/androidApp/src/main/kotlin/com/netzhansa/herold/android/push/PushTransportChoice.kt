package com.netzhansa.herold.android.push

/**
 * What the user asked for in settings (REQ-AND-PUSH-05). Automatic
 * follows the device - Google's transport where Play Services carries it,
 * a distributor otherwise - and the two explicit choices pin it, which is
 * what a de-Googled phone and a test run both need.
 */
enum class PushTransportChoice(val label: String, val stored: String) {
    AUTOMATIC("Automatic", "automatic"),
    FCM("FCM", "fcm"),
    UNIFIED_PUSH("UnifiedPush", "unifiedpush"),
    ;

    companion object {
        fun fromStored(value: String?): PushTransportChoice =
            entries.firstOrNull { it.stored == value } ?: AUTOMATIC
    }
}
