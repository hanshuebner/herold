package com.netzhansa.herold.android.ui.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.AssistChip
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.shared.auth.Credential
import kotlinx.coroutines.launch

/**
 * The account's active sessions and grants, read from the same
 * endpoint the Suite's session management reads
 * (`GET /api/v1/auth/credentials`, REQ-AS-30..33; REQ-AND-AUTH-22).
 * This device's own grant is marked. Revoking it signs the app out:
 * the server drops the access token with the family, so the next call
 * comes back 401 and the refresh is refused.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SessionsScreen(container: AppContainer, session: SessionScope, onBack: () -> Unit) {
    var entries by remember { mutableStateOf<List<Credential>>(emptyList()) }
    var ownGrantId by remember { mutableStateOf<String?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var reloads by remember { mutableStateOf(0) }
    val scope = rememberCoroutineScope()

    LaunchedEffect(reloads) {
        loading = true
        ownGrantId = container.tokenStore.grantId()
        runCatching { session.credentials.list() }
            .onSuccess {
                entries = it
                error = null
            }
            .onFailure { error = it.message ?: "the sessions list could not be read" }
        loading = false
    }

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("sessions-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = { Text("Sessions", modifier = Modifier.testTag("sessions-title")) },
                actions = {
                    TextButton(
                        onClick = { reloads++ },
                        modifier = Modifier.testTag("sessions-refresh"),
                    ) { Text("Refresh") }
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding).testTag("sessions-screen")) {
            if (loading) {
                CircularProgressIndicator(modifier = Modifier.padding(16.dp).testTag("sessions-loading"))
            }
            error?.let {
                Text(
                    text = it,
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.padding(16.dp).testTag("sessions-error"),
                )
            }
            LazyColumn(modifier = Modifier.fillMaxSize().testTag("sessions-list")) {
                items(entries, key = { it.kind + ":" + it.id }) { entry ->
                    val isThisDevice = entry.kind == Credential.KIND_OAUTH2_GRANT && entry.id == ownGrantId
                    CredentialRow(
                        entry = entry,
                        isThisDevice = isThisDevice,
                        onRevoke = {
                            scope.launch {
                                runCatching { session.credentials.revoke(entry.kind, entry.id) }
                                if (isThisDevice) {
                                    // Its own grant: the credential is
                                    // gone server-side, so the app goes
                                    // back to sign-in (REQ-AND-AUTH-22).
                                    container.signOut()
                                } else {
                                    reloads++
                                }
                            }
                        },
                    )
                    HorizontalDivider()
                }
            }
        }
    }
}

@Composable
private fun CredentialRow(entry: Credential, isThisDevice: Boolean, onRevoke: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 12.dp)
            .testTag("session-${entry.kind}-${entry.id}"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(text = describe(entry), style = MaterialTheme.typography.bodyLarge)
            val detail = listOfNotNull(
                entry.lastSeenIp.takeIf { it.isNotBlank() },
                entry.lastUsedAt.takeIf { it.isNotBlank() }?.let { "last used $it" }
                    ?: entry.createdAt.takeIf { it.isNotBlank() }?.let { "created $it" },
            ).joinToString(" - ")
            if (detail.isNotBlank()) {
                Text(
                    text = detail,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            if (isThisDevice) {
                AssistChip(
                    onClick = {},
                    label = { Text("This device") },
                    modifier = Modifier.testTag("session-this-device"),
                )
            }
        }
        TextButton(
            onClick = onRevoke,
            modifier = Modifier.testTag("session-revoke-${entry.kind}-${entry.id}"),
        ) {
            Text(if (isThisDevice) "Sign out" else "Revoke")
        }
    }
}

private fun describe(entry: Credential): String = when (entry.kind) {
    Credential.KIND_SESSION -> entry.userAgent.takeIf { it.isNotBlank() } ?: "Browser session"
    Credential.KIND_DEVICE_TOKEN -> entry.label.takeIf { it.isNotBlank() } ?: "Device token"
    Credential.KIND_OAUTH2_GRANT -> entry.label.takeIf { it.isNotBlank() }
        ?: entry.clientId.takeIf { it.isNotBlank() }
        ?: "App"
    else -> entry.kind
}
