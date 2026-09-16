package com.netzhansa.herold.android.ui.diag

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.shared.diag.BugCapture
import com.netzhansa.herold.shared.diag.BugKind
import com.netzhansa.herold.shared.diag.BugSubmission

/**
 * What the report looks like before it is sent (REQ-AND-SYS-53): the
 * screenshot as it was captured, a title, a free-text note, and the
 * checklist of what travels with it. Nothing on this sheet is guessed -
 * the capture happened before it opened, so what it lists is what the
 * mail will carry.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun BugReportSheet(
    capture: BugCapture,
    onDismiss: () -> Unit,
    onSend: (BugSubmission) -> Unit,
) {
    var title by remember { mutableStateOf("") }
    var note by remember { mutableStateOf("") }
    var includeScreenshot by remember { mutableStateOf(capture.screenshots.isNotEmpty()) }
    var includeLogs by remember { mutableStateOf(true) }
    var includeSession by remember { mutableStateOf(false) }

    ModalBottomSheet(onDismissRequest = onDismiss, modifier = Modifier.testTag("bug-sheet")) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .imePadding()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp)
                .padding(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                text = "Report a problem",
                style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.testTag("bug-sheet-title"),
            )
            Text(
                text = "This goes to your own mailbox under \"Bug reports\", with what the app " +
                    "knows about the moment you asked.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            capture.screenshots.firstOrNull()?.let { png ->
                val bitmap = remember(png) { BitmapFactory.decodeByteArray(png, 0, png.size) }
                bitmap?.let {
                    Image(
                        bitmap = it.asImageBitmap(),
                        contentDescription = "The screen as it was captured",
                        contentScale = ContentScale.Fit,
                        alignment = Alignment.TopCenter,
                        modifier = Modifier
                            .height(160.dp)
                            .testTag("bug-thumbnail"),
                    )
                }
            }

            OutlinedTextField(
                value = title,
                onValueChange = { title = it },
                label = { Text("What went wrong") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth().testTag("bug-title"),
            )
            OutlinedTextField(
                value = note,
                onValueChange = { note = it },
                label = { Text("Anything else (optional)") },
                minLines = 3,
                modifier = Modifier.fillMaxWidth().testTag("bug-note"),
            )

            Text(
                text = "Included",
                style = MaterialTheme.typography.titleSmall,
            )
            Text(
                text = summary(capture),
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.testTag("bug-included-summary"),
            )
            CheckRow(
                label = "Screenshot of this screen",
                checked = includeScreenshot,
                enabled = capture.screenshots.isNotEmpty(),
                tag = "bug-include-screenshot",
                onChange = { includeScreenshot = it },
            )
            CheckRow(
                label = "Diagnostic log (${capture.logs.size} lines)",
                checked = includeLogs,
                enabled = capture.logs.isNotEmpty(),
                tag = "bug-include-logs",
                onChange = { includeLogs = it },
            )
            CheckRow(
                label = "Session details, for reproducing",
                checked = includeSession,
                enabled = capture.sessionDetails.isNotEmpty(),
                tag = "bug-include-session",
                onChange = { includeSession = it },
            )

            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                TextButton(onClick = onDismiss, modifier = Modifier.testTag("bug-cancel")) {
                    Text("Cancel")
                }
                Button(
                    enabled = title.isNotBlank(),
                    onClick = {
                        onSend(
                            BugSubmission(
                                title = title,
                                note = note,
                                kind = BugKind.BUG,
                                includeScreenshot = includeScreenshot,
                                includeLogs = includeLogs,
                                includeSessionDetails = includeSession,
                            ),
                        )
                    },
                    modifier = Modifier.testTag("bug-send"),
                ) {
                    Text("Send")
                }
            }
        }
    }
}

/** The facts that always travel, said in one line. */
private fun summary(capture: BugCapture): String = buildString {
    append(capture.route)
    append(" - ").append(capture.device.appVersion)
    if (capture.device.appCommit.isNotBlank()) append(" (").append(capture.device.appCommit).append(")")
    append(", Android ").append(capture.device.androidVersion)
    append(", ").append(capture.device.model)
    append(" - sync ").append(capture.sync.state)
    append(", outbox ").append(capture.outbox.total)
}

@Composable
private fun CheckRow(
    label: String,
    checked: Boolean,
    enabled: Boolean,
    tag: String,
    onChange: (Boolean) -> Unit,
) {
    Row(
        modifier = Modifier.fillMaxWidth().testTag("$tag-row"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Checkbox(
            checked = checked && enabled,
            enabled = enabled,
            onCheckedChange = onChange,
            modifier = Modifier.testTag(tag),
        )
        Text(text = label, style = MaterialTheme.typography.bodyMedium)
    }
}
