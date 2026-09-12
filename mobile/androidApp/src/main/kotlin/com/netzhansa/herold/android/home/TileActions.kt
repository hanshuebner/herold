package com.netzhansa.herold.android.home

import android.content.Context
import android.content.Intent
import android.net.Uri
import com.netzhansa.herold.android.MainActivity
import com.netzhansa.herold.android.push.NotificationMute
import com.netzhansa.herold.android.ui.settings.TileAction
import com.netzhansa.herold.shared.links.AppLinks

/**
 * What a tap of the Quick Settings tile does and what the tile says it
 * will do (REQ-AND-SYS-21). It is separate from the service so the
 * behaviour is exercised without the shade having to bind the service.
 */
object TileActions {

    /**
     * Toggles the shade's quiet period and returns whether mail
     * notifications are now muted.
     */
    fun toggleMute(context: Context, now: Long = System.currentTimeMillis()): Boolean =
        if (NotificationMute.isMuted(context, now)) {
            NotificationMute.clear(context)
            false
        } else {
            NotificationMute.mute(context, now = now)
            true
        }

    /** The composer, addressed by the internal deep-link scheme. */
    fun composeIntent(context: Context): Intent =
        Intent(context, MainActivity::class.java).apply {
            action = Intent.ACTION_VIEW
            data = Uri.parse(AppLinks.composeUri())
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }

    /** What the tile is labelled with, given the action and the quiet period. */
    fun label(action: TileAction, muted: Boolean): String = when (action) {
        TileAction.COMPOSE -> "Compose"
        TileAction.MUTE_NOTIFICATIONS -> if (muted) "Mail muted" else "Mute mail"
    }
}
