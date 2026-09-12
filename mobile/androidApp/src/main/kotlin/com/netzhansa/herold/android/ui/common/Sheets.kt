package com.netzhansa.herold.android.ui.common

import android.text.format.DateFormat
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DatePicker
import androidx.compose.material3.DatePickerDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.SelectableDates
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TimeInput
import androidx.compose.material3.TimePicker
import androidx.compose.material3.rememberDatePickerState
import androidx.compose.material3.rememberTimePickerState
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
import androidx.compose.ui.window.Dialog
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.actions.SnoozePreset
import com.netzhansa.herold.shared.domain.Mailbox
import kotlinx.datetime.Clock
import kotlinx.datetime.Instant
import kotlinx.datetime.LocalDate
import kotlinx.datetime.TimeZone
import kotlinx.datetime.atStartOfDayIn
import kotlinx.datetime.toLocalDateTime

/**
 * The snooze picker as a bottom sheet (REQ-AND-NAV-04) carrying the suite's
 * presets (REQ-SNZ-01..04) and its custom pick (REQ-SNZ-05): the Material
 * date picker followed by the time picker, opening on the next full hour.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SnoozeSheet(
    onDismiss: () -> Unit,
    onPick: (wakeAtIso: String) -> Unit,
) {
    val zone = TimeZone.currentSystemDefault()
    // One reading of the clock for the whole picker, so the presets and the
    // custom default stay on the values the sheet opened with.
    val now = remember { Clock.System.now() }
    val default = remember(now) { SnoozeClock.nextFullHour(now, zone).toLocalDateTime(zone) }
    var stage by remember { mutableStateOf(CustomStage.NONE) }
    var pickedDate by remember { mutableStateOf(default.date) }
    val options = listOf(
        "Later today" to SnoozePreset.LATER_TODAY,
        "Tomorrow morning" to SnoozePreset.TOMORROW_MORNING,
        "This weekend" to SnoozePreset.THIS_WEEKEND,
        "Next week" to SnoozePreset.NEXT_WEEK,
    )
    when (stage) {
        CustomStage.NONE ->
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
                            supportingContent = {
                                Text(SnoozeClock.describe(SnoozeClock.wakeTime(preset, now, zone), now, zone))
                            },
                            modifier = Modifier
                                .testTag("snooze-${preset.name}")
                                .clickableItem { onPick(SnoozeClock.wireValue(SnoozeClock.wakeTime(preset, now, zone))) },
                        )
                    }
                    ListItem(
                        headlineContent = { Text("Pick date and time") },
                        supportingContent = { Text(SnoozeClock.describe(SnoozeClock.nextFullHour(now, zone), now, zone)) },
                        modifier = Modifier
                            .testTag("snooze-custom")
                            .clickableItem { stage = CustomStage.DATE },
                    )
                    TextButton(onClick = onDismiss, modifier = Modifier.padding(horizontal = 16.dp)) {
                        Text("Cancel")
                    }
                }
            }

        CustomStage.DATE ->
            SnoozeDatePicker(
                initial = pickedDate,
                today = now.toLocalDateTime(zone).date,
                onCancel = onDismiss,
                onPicked = { date ->
                    pickedDate = date
                    stage = CustomStage.TIME
                },
            )

        CustomStage.TIME ->
            SnoozeTimePicker(
                initialHour = default.hour,
                initialMinute = default.minute,
                onCancel = onDismiss,
                onPicked = { hour, minute ->
                    onPick(SnoozeClock.wireValue(SnoozeClock.customWakeTime(pickedDate, hour, minute, zone)))
                },
            )
    }
}

/** Which half of the custom pick is on screen. */
private enum class CustomStage { NONE, DATE, TIME }

/**
 * The wake date. The dialog's calendar works in UTC-midnight milliseconds,
 * which is the calendar day the user tapped; the clock time picked next
 * turns it into an instant in the device's zone.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun SnoozeDatePicker(
    initial: LocalDate,
    today: LocalDate,
    onCancel: () -> Unit,
    onPicked: (LocalDate) -> Unit,
) {
    val earliest = today.atStartOfDayIn(TimeZone.UTC).toEpochMilliseconds()
    val state = rememberDatePickerState(
        initialSelectedDateMillis = initial.atStartOfDayIn(TimeZone.UTC).toEpochMilliseconds(),
        selectableDates = object : SelectableDates {
            override fun isSelectableDate(utcTimeMillis: Long): Boolean = utcTimeMillis >= earliest
            override fun isSelectableYear(year: Int): Boolean = year >= today.year
        },
    )
    DatePickerDialog(
        onDismissRequest = onCancel,
        modifier = Modifier.testTag("snooze-date-dialog"),
        confirmButton = {
            TextButton(
                onClick = {
                    val millis = state.selectedDateMillis ?: return@TextButton
                    onPicked(Instant.fromEpochMilliseconds(millis).toLocalDateTime(TimeZone.UTC).date)
                },
                modifier = Modifier.testTag("snooze-date-confirm"),
            ) {
                Text("Next")
            }
        },
        dismissButton = {
            TextButton(onClick = onCancel, modifier = Modifier.testTag("snooze-date-cancel")) {
                Text("Cancel")
            }
        },
    ) {
        DatePicker(state = state)
    }
}

/**
 * The wake time, opening on the next full hour. The dial is the default
 * surface and the keyboard toggle switches to the text entry the platform
 * offers for an exact minute.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun SnoozeTimePicker(
    initialHour: Int,
    initialMinute: Int,
    onCancel: () -> Unit,
    onPicked: (hour: Int, minute: Int) -> Unit,
) {
    val state = rememberTimePickerState(
        initialHour = initialHour,
        initialMinute = initialMinute,
        is24Hour = DateFormat.is24HourFormat(LocalContext.current),
    )
    var typed by remember { mutableStateOf(false) }
    Dialog(onDismissRequest = onCancel) {
        Surface(
            shape = MaterialTheme.shapes.extraLarge,
            tonalElevation = 6.dp,
            modifier = Modifier.testTag("snooze-time-dialog"),
        ) {
            Column(
                modifier = Modifier.padding(20.dp).verticalScroll(rememberScrollState()),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Text(
                    text = "Wake at",
                    style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.fillMaxWidth().padding(bottom = 16.dp),
                )
                if (typed) TimeInput(state = state) else TimePicker(state = state)
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    TextButton(
                        onClick = { typed = !typed },
                        modifier = Modifier.testTag("snooze-time-mode"),
                    ) {
                        Text(if (typed) "Dial" else "Keyboard")
                    }
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.End,
                    ) {
                        TextButton(onClick = onCancel, modifier = Modifier.testTag("snooze-time-cancel")) {
                            Text("Cancel")
                        }
                        TextButton(
                            onClick = { onPicked(state.hour, state.minute) },
                            modifier = Modifier.testTag("snooze-time-confirm"),
                        ) {
                            Text("Snooze")
                        }
                    }
                }
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
