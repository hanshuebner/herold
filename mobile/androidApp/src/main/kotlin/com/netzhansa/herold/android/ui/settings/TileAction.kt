package com.netzhansa.herold.android.ui.settings

import android.content.Context

/**
 * What the Quick Settings tile does when it is tapped
 * (REQ-AND-SYS-21). The user picks one; the tile carries it.
 */
enum class TileAction {
    /** Opens the composer. */
    COMPOSE,

    /** Quietens mail notifications for an hour, and un-quietens them. */
    MUTE_NOTIFICATIONS,
    ;

    val label: String
        get() = when (this) {
            COMPOSE -> "Compose"
            MUTE_NOTIFICATIONS -> "Mute notifications for an hour"
        }

    companion object {
        val DEFAULT = COMPOSE

        fun named(value: String?): TileAction = entries.firstOrNull { it.name == value } ?: DEFAULT
    }
}

/** The chosen tile action, remembered across launches. */
object TileActionPreference {
    private const val FILE = "herold-ui"
    private const val KEY = "tile_action"

    fun current(context: Context): TileAction =
        TileAction.named(context.getSharedPreferences(FILE, Context.MODE_PRIVATE).getString(KEY, null))

    fun remember(context: Context, action: TileAction) {
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit().putString(KEY, action.name).apply()
    }
}
