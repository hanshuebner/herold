package com.netzhansa.herold.android.ui.common

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.actions.SnoozePreset
import com.netzhansa.herold.shared.domain.Mailbox
import kotlinx.datetime.Clock
import kotlinx.datetime.TimeZone

/**
 * The snooze picker as a bottom sheet (REQ-AND-NAV-04) carrying the suite's
 * presets (REQ-SNZ-01..04). The custom date-and-time preset is milestone 1c
 * work alongside compose's own pickers.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SnoozeSheet(
    onDismiss: () -> Unit,
    onPick: (wakeAtIso: String) -> Unit,
) {
    val zone = TimeZone.currentSystemDefault()
    val now = Clock.System.now()
    val options = listOf(
        "Later today" to SnoozePreset.LATER_TODAY,
        "Tomorrow morning" to SnoozePreset.TOMORROW_MORNING,
        "This weekend" to SnoozePreset.THIS_WEEKEND,
        "Next week" to SnoozePreset.NEXT_WEEK,
    )
    ModalBottomSheet(onDismissRequest = onDismiss, modifier = Modifier.testTag("snooze-sheet")) {
        Column(modifier = Modifier.fillMaxWidth().padding(bottom = 24.dp)) {
            Text(
                text = "Snooze until",
                style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
            )
            options.forEach { (label, preset) ->
                ListItem(
                    headlineContent = { Text(label) },
                    modifier = Modifier
                        .testTag("snooze-${preset.name}")
                        .clickableItem { onPick(SnoozeClock.wireValue(SnoozeClock.wakeTime(preset, now, zone))) },
                )
            }
            TextButton(onClick = onDismiss, modifier = Modifier.padding(horizontal = 16.dp)) {
                Text("Cancel")
            }
        }
    }
}

/**
 * The label picker as a bottom sheet: a label is a mailbox with no role, the
 * same object the suite's picker offers (web/apps/suite/src/lib/mail/LabelPicker.svelte).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun LabelSheet(
    labels: List<Mailbox>,
    applied: Set<String>,
    onDismiss: () -> Unit,
    onToggle: (Mailbox, Boolean) -> Unit,
) {
    var appliedNames by remember { mutableStateOf(applied) }
    ModalBottomSheet(onDismissRequest = onDismiss, modifier = Modifier.testTag("label-sheet")) {
        Column(modifier = Modifier.fillMaxWidth().padding(bottom = 24.dp)) {
            Text(
                text = "Labels",
                style = MaterialTheme.typography.titleMedium,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
            )
            if (labels.isEmpty()) {
                Text(
                    text = "This account has no labels yet",
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                )
            }
            LazyColumn {
                items(labels, key = { it.id }) { label ->
                    val checked = appliedNames.contains(label.name)
                    ListItem(
                        headlineContent = { Text(label.name) },
                        trailingContent = { Checkbox(checked = checked, onCheckedChange = null) },
                        modifier = Modifier
                            .testTag("label-${label.name}")
                            .clickableItem {
                                appliedNames = if (checked) appliedNames - label.name else appliedNames + label.name
                                onToggle(label, !checked)
                            },
                    )
                }
            }
            TextButton(onClick = onDismiss, modifier = Modifier.padding(horizontal = 16.dp)) {
                Text("Done")
            }
        }
    }
}

private fun Modifier.clickableItem(onClick: () -> Unit): Modifier = clickable(onClick = onClick)
