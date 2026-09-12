package com.netzhansa.herold.android.push

import android.content.Context

/**
 * A short mute of mail notifications, which the Quick Settings tile
 * offers as its fast action (REQ-AND-SYS-21). It is a local quiet
 * period: the subscription stays, pushes keep reconciling the store, and
 * only the shade stays silent, so the mail is there when the mute ends.
 */
object NotificationMute {

    /** How long one tap of the tile quietens the shade. */
    const val DURATION_MS: Long = 60 * 60 * 1000L

    private const val FILE = "herold-ui"
    private const val KEY = "notifications_muted_until"

    fun mutedUntil(context: Context): Long =
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).getLong(KEY, 0L)

    fun isMuted(context: Context, now: Long = System.currentTimeMillis()): Boolean =
        mutedUntil(context) > now

    /** Mutes for [durationMs] and returns the time the quiet ends. */
    fun mute(context: Context, durationMs: Long = DURATION_MS, now: Long = System.currentTimeMillis()): Long {
        val until = now + durationMs
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit().putLong(KEY, until).apply()
        return until
    }

    fun clear(context: Context) {
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit().remove(KEY).apply()
    }
}
