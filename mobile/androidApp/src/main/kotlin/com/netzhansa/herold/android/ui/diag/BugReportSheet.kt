package com.netzhansa.herold.android.ui.diag

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.material3.Surface
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
import com.netzhansa.herold.shared.diag.BugBundleWriter
import com.netzhansa.herold.shared.diag.BugCapture
import com.netzhansa.herold.shared.diag.BugSubmission

/**
 * What the report looks like before it is sent (REQ-AND-SYS-53): the
 * screens as they were captured, the checklist of what travels with
 * them, and two optional fields. Nothing on this sheet is guessed - the
 * capture happened before it opened, so what it lists is what the
 * report will carry.
 *
 * Send needs nothing typed. Describing a problem on a phone keyboard is
 * the slowest part of reporting one, so the phone's job is to capture
 * and the description is added on the desktop, where `/bug-inbox` asks
 * for it (issue #408). "Add another capture" keeps the report open and
 * puts the shell back, for a problem that takes more than one picture
 * to show (issue #424). The action row is pinned below the scrolling
 * content, so the one tap is always in reach with the keyboard up.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun BugReportSheet(
    capture: BugCapture,
    initial: BugSubmission,
    onDismiss: () -> Unit,
    onAddAnother: (BugSubmission) -> Unit,
    onRemoveCapture: (Int) -> Unit,
    onDiscard: () -> Unit,
    onSend: (BugSubmission) -> Unit,
) {
    var title by remember(initial) { mutableStateOf(initial.title) }
    var note by remember(initial) { mutableStateOf(initial.note) }
    var includeScreenshot by remember(initial) {
        mutableStateOf(initial.includeScreenshot && capture.screenshots.isNotEmpty())
    }
    var includeLogs by remember(initial) { mutableStateOf(initial.includeLogs) }
    var includeSession by remember(initial) { mutableStateOf(initial.includeSessionDetails) }

    fun submission(): BugSubmission = BugSubmission(
        title = title,
        note = note,
        kind = initial.kind,
        includeScreenshot = includeScreenshot,
        includeLogs = includeLogs,
        includeSessionDetails = includeSession,
    )

    // Opened at its full height rather than half: the point of the
    // sheet is the one tap, and a half-open sheet puts it below the fold.
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        modifier = Modifier.testTag("bug-sheet"),
    ) {
      Column(modifier = Modifier.fillMaxWidth().imePadding()) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .weight(1f, fill = false)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                text = "Report a problem",
                style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.testTag("bug-sheet-title"),
            )
            Text(
                text = "This goes to your server for the maintainer to pick up, with what " +
                    "the app knew the moment you asked. Say nothing here and describe it " +
                    "later on the desktop.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            CaptureStrip(capture = capture, onRemove = onRemoveCapture)

            OutlinedTextField(
                value = title,
                onValueChange = { title = it },
                label = { Text("What went wrong (optional)") },
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
                label = screenshotLabel(capture),
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

        }

        HorizontalDivider()
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp, vertical = 12.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.End),
            verticalAlignment = Alignment.CenterVertically,
        ) {
                TextButton(onClick = onDiscard, modifier = Modifier.testTag("bug-cancel")) {
                    Text("Discard")
                }
                TextButton(
                    onClick = { onAddAnother(submission()) },
                    modifier = Modifier.testTag("bug-add-capture"),
                ) {
                    Text("Add another capture")
                }
                Button(
                    onClick = { onSend(submission()) },
                    modifier = Modifier.testTag("bug-send"),
                ) {
                    Text("Send")
                }
        }
      }
    }
}

/**
 * Every screen the report has been taken on, in capture order, each one
 * removable (issue #424). The route under a picture says which screen
 * it is, which is what tells two inbox captures apart.
 */
@Composable
private fun CaptureStrip(capture: BugCapture, onRemove: (Int) -> Unit) {
    if (capture.shots.isEmpty()) return
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .horizontalScroll(rememberScrollState())
            .testTag("bug-capture-strip"),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        capture.shots.forEachIndexed { at, shot ->
            val index = at + 1
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Box(modifier = Modifier.testTag("bug-capture-$index")) {
                    val png = shot.screenshot
                    val bitmap = remember(png) {
                        png?.let { BitmapFactory.decodeByteArray(it, 0, it.size) }
                    }
                    if (bitmap != null) {
                        Image(
                            bitmap = bitmap.asImageBitmap(),
                            contentDescription = "Capture $index, on ${shot.route}",
                            contentScale = ContentScale.Fit,
                            alignment = Alignment.TopCenter,
                            modifier = Modifier
                                .height(160.dp)
                                .testTag("bug-thumbnail-$index"),
                        )
                    } else {
                        Surface(
                            color = MaterialTheme.colorScheme.surfaceVariant,
                            modifier = Modifier.height(160.dp).width(90.dp),
                        ) {
                            Box(contentAlignment = Alignment.Center) {
                                Text(
                                    text = "no picture",
                                    style = MaterialTheme.typography.labelSmall,
                                )
                            }
                        }
                    }
                    IconButton(
                        onClick = { onRemove(at) },
                        colors = IconButtonDefaults.filledIconButtonColors(),
                        modifier = Modifier
                            .align(Alignment.TopEnd)
                            .padding(2.dp)
                            .size(28.dp)
                            .testTag("bug-remove-capture-$index"),
                    ) {
                        Icon(
                            imageVector = Icons.Default.Close,
                            contentDescription = "Remove capture $index",
                            modifier = Modifier.size(16.dp),
                        )
                    }
                }
                Text(
                    text = "$index. " + BugBundleWriter.routeLabel(shot.route),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

/** What the screenshot row is called, which counts them once there are several. */
private fun screenshotLabel(capture: BugCapture): String = when (val count = capture.screenshots.size) {
    0, 1 -> "Screenshot of this screen"
    else -> "Screenshots of the $count captured screens"
}

/** The facts that always travel, said in one line. */
private fun summary(capture: BugCapture): String = buildString {
    append(capture.route)
    if (capture.shots.size > 1) append(" +").append(capture.shots.size - 1)
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
