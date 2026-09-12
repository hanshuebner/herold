package com.netzhansa.herold.android.push

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import androidx.core.app.NotificationManagerCompat
import com.netzhansa.herold.shared.push.PushChannels

/**
 * The per-kind notification channels (REQ-AND-PUSH-10), created at app
 * start so the user can tune importance, sound and vibration per kind in
 * system settings before the first notification ever arrives.
 */
object NotificationChannels {

    fun create(context: Context) {
        val manager = NotificationManagerCompat.from(context)
        DEFINITIONS.forEach { (id, definition) ->
            val (name, importance) = definition
            manager.createNotificationChannel(
                NotificationChannel(id, name, importance).apply {
                    setShowBadge(true)
                },
            )
        }
    }

    /** The channel name as it reads in system settings. */
    fun nameOf(channelId: String): String = DEFINITIONS[channelId]?.first ?: channelId

    private val DEFINITIONS: Map<String, Pair<String, Int>> = mapOf(
        PushChannels.MAIL to ("Mail" to NotificationManager.IMPORTANCE_HIGH),
        PushChannels.CHAT to ("Chat" to NotificationManager.IMPORTANCE_HIGH),
        PushChannels.CALLS to ("Calls" to NotificationManager.IMPORTANCE_HIGH),
        PushChannels.CALENDAR to ("Calendar" to NotificationManager.IMPORTANCE_DEFAULT),
        PushChannels.REACTIONS to ("Reactions" to NotificationManager.IMPORTANCE_LOW),
    )
}
