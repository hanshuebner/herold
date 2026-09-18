package com.netzhansa.herold.android.ui.diag

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
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.BuildConfig
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.android.ui.common.statusDescription
import com.netzhansa.herold.shared.diag.LogLine
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.sync.SyncStatus
import com.netzhansa.herold.shared.sync.appStatus
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.flowOf
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * What the app knows about itself (REQ-AND-SYS-54): the connection, what
 * the reconciler last did and why it stopped, what is in the queue, how
 * push is wired, and the diagnostic ring the bug reporter carries. It is
 * where the status indicator's one dot is spelled out, and where an
 * error goes instead of a banner the user has to catch as it passes.
 *
 * The ring reads newest first, in a monospace column, and one button
 * puts the whole of it on the clipboard.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DiagnosticsScreen(
    container: AppContainer,
    session: SessionScope?,
    onReportProblem: () -> Unit,
    onBack: () -> Unit,
) {
    val clipboard = LocalClipboardManager.current
    val offline by container.offline.collectAsStateSafely(false)
    val syncFlow = remember(session) { session?.syncEngine?.status ?: flowOf(SyncStatus.Idle) }
    val syncStatus by syncFlow.collectAsStateSafely(SyncStatus.Idle)
    val queue by container.outbox.entries.collectAsStateSafely(emptyList())
    val push = container.push
    val registration by produceState<String?>(initialValue = null) {
        value = runCatching { container.store.pushRegistration()?.transport }.getOrNull()
    }

    // The ring grows while the screen is open, which is the point of
    // watching it: a reproduction attempted from here is readable as it
    // happens.
    val lines by produceState(initialValue = DiagLog.ring.lines()) {
        while (true) {
            value = DiagLog.ring.lines()
            delay(RING_POLL_MS)
        }
    }
    val newestFirst = remember(lines) { lines.asReversed() }

    // Opening the screen is itself worth a line: a report filed from
    // here says when the user went looking.
    LaunchedEffect(Unit) { DiagLog.i(TAG, "diagnostics opened") }

    val pending = queue.count { it.isPending }
    val failed = queue.count { it.state == OutboxState.FAILED }
    val status = appStatus(offline, syncStatus, pending, failed)

    Scaffold(
        topBar = {
            TopAppBar(
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("diagnostics-back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                title = { Text("Diagnostics", modifier = Modifier.testTag("diagnostics-title")) },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .testTag("diagnostics-screen"),
        ) {
            Fact(
                tag = "diagnostics-status",
                label = "Status",
                value = statusDescription(status),
            )
            Fact(
                tag = "diagnostics-connectivity",
                label = "Connection",
                value = if (offline) "Offline" else "Connected to ${session?.baseUrl ?: "no server"}",
            )
            Fact(
                tag = "diagnostics-sync",
                label = "Sync",
                value = when (val current = syncStatus) {
                    SyncStatus.Idle -> if (session == null) "No session" else "Idle"
                    SyncStatus.Syncing -> "Running"
                    is SyncStatus.Failed -> "Failed: ${current.message}"
                },
            )
            Fact(
                tag = "diagnostics-outbox",
                label = "Outbox",
                value = outboxSummary(queue.size, pending, failed) +
                    (queue.firstNotNullOfOrNull { it.lastError }?.let { "\nLast error: $it" } ?: ""),
            )
            Fact(
                tag = "diagnostics-push",
                label = "Push",
                value = buildString {
                    append(push.transport()?.wire ?: "no transport")
                    append(registration?.let { " - registered over $it" } ?: " - not registered")
                    runCatching { push.distributor() }.getOrNull()?.let { append("\nDistributor: $it") }
                },
            )
            Fact(
                tag = "diagnostics-build",
                label = "Build",
                value = "${BuildConfig.VERSION_NAME} (${BuildConfig.GIT_COMMIT})",
            )

            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 8.dp),
                horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                OutlinedButton(
                    onClick = { clipboard.setText(AnnotatedString(ringText(newestFirst))) },
                    modifier = Modifier.testTag("diagnostics-copy"),
                ) {
                    Text("Copy log")
                }
                Button(
                    onClick = onReportProblem,
                    modifier = Modifier.testTag("diagnostics-report"),
                ) {
                    Text("Report a problem")
                }
            }

            Text(
                text = if (newestFirst.isEmpty()) {
                    "The diagnostic log is off or empty."
                } else {
                    "Log (${newestFirst.size} lines, newest first)"
                },
                style = MaterialTheme.typography.titleSmall,
                modifier = Modifier
                    .padding(horizontal = 16.dp, vertical = 8.dp)
                    .testTag("diagnostics-log-heading"),
            )
            HorizontalDivider()
            LazyColumn(modifier = Modifier.fillMaxSize().testTag("diagnostics-log")) {
                items(newestFirst) { line ->
                    Text(
                        text = formatLine(line),
                        style = MaterialTheme.typography.bodySmall,
                        fontFamily = FontFamily.Monospace,
                        color = when (line.level) {
                            "error" -> MaterialTheme.colorScheme.error
                            else -> MaterialTheme.colorScheme.onSurface
                        },
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(horizontal = 16.dp, vertical = 2.dp)
                            .testTag("diagnostics-log-line"),
                    )
                }
            }
        }
    }
}

/** One labelled fact, laid out the same whatever it says. */
@Composable
private fun Fact(tag: String, label: String, value: String) {
    Column(modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp).testTag(tag)) {
        Text(
            text = label,
            style = MaterialTheme.typography.labelMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        Text(text = value, style = MaterialTheme.typography.bodyMedium)
    }
}

private fun outboxSummary(total: Int, pending: Int, failed: Int): String = when {
    total == 0 -> "Empty"
    else -> "$total waiting ($pending pending, $failed failed)"
}

private val CLOCK = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)

private fun formatLine(line: LogLine): String =
    "${CLOCK.format(Date(line.atMs))} ${line.level.first().uppercase()} ${line.ctx}: ${line.message}"

private fun ringText(lines: List<LogLine>): String = lines.joinToString("\n", transform = ::formatLine)

/** How often the open screen re-reads the ring. */
private const val RING_POLL_MS = 1_000L

private const val TAG = "herold.diagnostics"
