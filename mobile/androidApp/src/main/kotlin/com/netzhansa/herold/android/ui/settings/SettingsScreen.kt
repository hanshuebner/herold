package com.netzhansa.herold.android.ui.settings

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.auth.IdlePeriod
import com.netzhansa.herold.android.auth.UnlockController

/**
 * The app's settings: how long a message waits before it goes (issue
 * #354), whether the app locks behind the device's unlock
 * (REQ-AND-AUTH-11), and the way through to the account's active
 * sessions (REQ-AND-AUTH-22).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(
    unlock: UnlockController,
    onSessions: () -> Unit,
    onBack: () -> Unit,
) {
    val context = LocalContext.current
    var window by remember { mutableStateOf(UndoSendPreference.current(context)) }
    var unlockEnabled by remember { mutableStateOf(unlock.enabled) }
    var idlePeriod by remember { mutableStateOf(unlock.idlePeriod) }
    val unlockAvailable = remember { unlock.available() }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("settings-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = { Text("Settings", modifier = Modifier.testTag("settings-title")) },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .testTag("settings-screen"),
        ) {
            Text(
                text = "Undo send",
                style = MaterialTheme.typography.titleSmall,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
            )
            Text(
                text = "How long a sent message waits on the phone before it goes.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            UndoSendWindow.entries.forEach { option ->
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clickable {
                            window = option
                            UndoSendPreference.remember(context, option)
                        }
                        .padding(horizontal = 16.dp, vertical = 10.dp)
                        .testTag("undo-send-${option.seconds}"),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    RadioButton(selected = window == option, onClick = null)
                    Text(text = option.label, modifier = Modifier.padding(start = 12.dp))
                }
            }

            Text(
                text = "Unlock",
                style = MaterialTheme.typography.titleSmall,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
            )
            Text(
                text = if (unlockAvailable) {
                    "Ask for your fingerprint, face or screen lock before the app reaches your mail."
                } else {
                    "Set a screen lock on this device to use it."
                },
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 10.dp)
                    .testTag("unlock-toggle-row"),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(text = "Require unlock", modifier = Modifier.weight(1f))
                Switch(
                    checked = unlockEnabled,
                    enabled = unlockAvailable,
                    onCheckedChange = {
                        unlockEnabled = it
                        unlock.enabled = it
                    },
                    modifier = Modifier.testTag("unlock-toggle"),
                )
            }
            if (unlockEnabled) {
                IdlePeriod.entries.forEach { option ->
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable {
                                idlePeriod = option
                                unlock.idlePeriod = option
                            }
                            .padding(horizontal = 16.dp, vertical = 10.dp)
                            .testTag("unlock-idle-${option.minutes}"),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        RadioButton(selected = idlePeriod == option, onClick = null)
                        Text(text = option.label, modifier = Modifier.padding(start = 12.dp))
                    }
                }
            }

            Text(
                text = "Account",
                style = MaterialTheme.typography.titleSmall,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 12.dp),
            )
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable(onClick = onSessions)
                    .padding(horizontal = 16.dp, vertical = 14.dp)
                    .testTag("settings-sessions"),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(text = "Sessions", modifier = Modifier.weight(1f))
                Text(
                    text = "Where this account is signed in",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}
