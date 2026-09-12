package com.netzhansa.herold.android.home

import android.app.PendingIntent
import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import com.netzhansa.herold.android.R
import com.netzhansa.herold.android.push.NotificationMute
import com.netzhansa.herold.android.ui.settings.TileAction
import com.netzhansa.herold.android.ui.settings.TileActionPreference

/**
 * The Quick Settings tile (REQ-AND-SYS-21). It carries the action the
 * user chose in settings: open the composer, or quieten mail
 * notifications for an hour. The mute form is a toggle - a second tap
 * gives the shade back - and the tile's state says which it is in.
 */
class HeroldTileService : TileService() {

    override fun onStartListening() {
        super.onStartListening()
        render()
    }

    override fun onClick() {
        super.onClick()
        when (TileActionPreference.current(this)) {
            TileAction.COMPOSE -> openCompose()
            TileAction.MUTE_NOTIFICATIONS -> {
                TileActions.toggleMute(this)
                render()
            }
        }
    }

    private fun render() {
        val tile: Tile = qsTile ?: return
        val action = TileActionPreference.current(this)
        val muted = NotificationMute.isMuted(this)
        tile.label = TileActions.label(action, muted)
        tile.state = if (action == TileAction.MUTE_NOTIFICATIONS && muted) {
            Tile.STATE_ACTIVE
        } else {
            Tile.STATE_INACTIVE
        }
        tile.icon = Icon.createWithResource(this, R.drawable.ic_notification)
        tile.updateTile()
    }

    private fun openCompose() {
        val intent = TileActions.composeIntent(this)
        val pending = PendingIntent.getActivity(
            this,
            0,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startActivityAndCollapse(pending)
        } else {
            @Suppress("DEPRECATION")
            startActivityAndCollapse(intent)
        }
    }
}
