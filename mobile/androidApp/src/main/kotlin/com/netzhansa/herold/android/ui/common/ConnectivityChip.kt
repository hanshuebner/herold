package com.netzhansa.herold.android.ui.common

import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CloudOff
import androidx.compose.material.icons.filled.Schedule
import androidx.compose.material3.AssistChip
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp

/**
 * The non-intrusive connectivity and outbox indication (REQ-AND-SYNC-30).
 * It says what is true of the user's mail rather than of the radio: with
 * a queue, how much is waiting; without one, only that the phone is
 * offline. It is absent when there is nothing to say, and a drop shorter
 * than the grace period never makes it appear (`offlineIndication`).
 *
 * Tapping it opens the outbox, which is where "what is waiting" is
 * answered in full (REQ-AND-SYNC-25).
 */
@Composable
fun ConnectivityChip(offline: Boolean, pending: Int, onOpenOutbox: () -> Unit) {
    if (!offline && pending == 0) return
    val label = when {
        offline && pending > 0 -> "Offline - $pending waiting"
        offline -> "Offline"
        else -> "$pending waiting to send"
    }
    AssistChip(
        onClick = onOpenOutbox,
        label = { Text(label) },
        leadingIcon = {
            Icon(
                imageVector = if (offline) Icons.Filled.CloudOff else Icons.Filled.Schedule,
                contentDescription = null,
            )
        },
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 12.dp, vertical = 4.dp)
            .testTag("connectivity-chip"),
    )
}
