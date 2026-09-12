package com.netzhansa.herold.android.ui.settings

import android.content.Context

/**
 * How long a sent message is held before the outbox drains it, so the
 * user can take it back (issue #354). The choices match what a phone
 * user expects from a mail app: off, or five to thirty seconds.
 */
enum class UndoSendWindow(val seconds: Int) {
    OFF(0),
    FIVE(5),
    TEN(10),
    TWENTY(20),
    THIRTY(30),
    ;

    val millis: Long get() = seconds * 1_000L

    val label: String get() = if (this == OFF) "Off" else "$seconds seconds"

    companion object {
        val DEFAULT = FIVE

        fun named(value: String?): UndoSendWindow =
            entries.firstOrNull { it.name == value } ?: DEFAULT
    }
}

/** The chosen window, remembered across launches. */
object UndoSendPreference {
    private const val FILE = "herold-ui"
    private const val KEY = "undo_send_window"

    fun current(context: Context): UndoSendWindow =
        UndoSendWindow.named(
            context.getSharedPreferences(FILE, Context.MODE_PRIVATE).getString(KEY, null),
        )

    fun remember(context: Context, window: UndoSendWindow) {
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit().putString(KEY, window.name).apply()
    }
}
