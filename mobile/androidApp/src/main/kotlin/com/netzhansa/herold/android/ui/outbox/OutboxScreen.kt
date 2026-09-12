package com.netzhansa.herold.android.ui.outbox

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
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.outbox.OutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxState
import kotlinx.coroutines.launch

/**
 * What is still waiting to reach the server (REQ-AND-SYNC-25): every
 * queued action, draft and send, with its state, the reason a failed one
 * gives, and a retry that puts it back in the queue.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun OutboxScreen(
    container: AppContainer,
    session: SessionScope,
    onBack: () -> Unit,
) {
    val entries by container.outbox.entries.collectAsStateSafely(emptyList())
    val scope = rememberCoroutineScope()

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("outbox-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = { Text("Outbox", modifier = Modifier.testTag("outbox-title")) },
                actions = {
                    if (entries.any { it.state == OutboxState.FAILED }) {
                        TextButton(
                            onClick = {
                                scope.launch {
                                    container.outbox.retryAll()
                                    session.requestDrain(0)
                                }
                            },
                            modifier = Modifier.testTag("outbox-retry-all"),
                        ) {
                            Text("Retry all")
                        }
                    }
                },
            )
        },
    ) { padding ->
        Column(modifier = Modifier.fillMaxSize().padding(padding)) {
            if (entries.isEmpty()) {
                Text(
                    text = "Everything has been sent.",
                    modifier = Modifier.fillMaxWidth().padding(24.dp).testTag("outbox-empty"),
                )
                return@Column
            }
            LazyColumn(modifier = Modifier.fillMaxSize().testTag("outbox-list")) {
                items(entries, key = { it.id }) { entry ->
                    EntryRow(
                        entry = entry,
                        onRetry = {
                            scope.launch {
                                container.outbox.retry(entry.id)
                                session.requestDrain(0)
                            }
                        },
                        onDiscard = { scope.launch { container.outbox.remove(entry.id) } },
                    )
                    HorizontalDivider()
                }
            }
        }
    }
}

@Composable
private fun EntryRow(entry: OutboxEntry, onRetry: () -> Unit, onDiscard: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 12.dp)
            .testTag("outbox-entry-${entry.id}"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = entry.label,
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
            )
            Text(
                text = stateLabel(entry),
                style = MaterialTheme.typography.bodySmall,
                color = if (entry.state == OutboxState.FAILED) {
                    MaterialTheme.colorScheme.error
                } else {
                    MaterialTheme.colorScheme.onSurfaceVariant
                },
                modifier = Modifier.testTag("outbox-state-${entry.id}"),
            )
        }
        if (entry.state == OutboxState.FAILED) {
            TextButton(onClick = onRetry, modifier = Modifier.testTag("outbox-retry-${entry.id}")) {
                Text("Retry")
            }
            TextButton(onClick = onDiscard, modifier = Modifier.testTag("outbox-discard-${entry.id}")) {
                Text("Discard")
            }
        }
    }
}

/** The one line that says where an entry stands and why. */
private fun stateLabel(entry: OutboxEntry): String = when (entry.state) {
    OutboxState.SENDING -> "Sending"
    OutboxState.FAILED -> entry.lastError?.let { "Failed: $it" } ?: "Failed"
    OutboxState.QUEUED -> when {
        entry.attempts > 0 -> "Waiting to retry" + (entry.lastError?.let { " - $it" } ?: "")
        else -> "Queued"
    }
}
