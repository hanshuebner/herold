package com.netzhansa.herold.android.ui.diag

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.BugReport
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.AssistChip
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import androidx.navigation.NavController
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.diag.BugReportController
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.android.diag.DiagPreferences
import com.netzhansa.herold.android.diag.ShakeToReport
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.android.ui.settings.UndoSendPreference
import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.diag.BugCapture
import com.netzhansa.herold.shared.diag.BugSubmission
import com.netzhansa.herold.shared.diag.PendingBugReport
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * The reporter, mounted over every screen of the shell
 * (REQ-AND-SYS-50..53). It answers both entry points - the menu items
 * and the shake - by capturing first and opening the sheet second, so
 * what the report carries is the screen the user was complaining about
 * rather than the sheet covering it.
 *
 * A report stays open across screens (issue #424): "Add another
 * capture" puts the shell back with the report held, a chip says how
 * many screens it carries, and the next gesture asks whether to add to
 * it or start again. What is held is written to app storage, so the
 * captures survive the app being killed between two of them.
 *
 * Sending goes through the outbox with the undo window the user chose
 * for mail, so a report asked for by mistake is taken back the same way
 * a message is.
 */
@Composable
fun BugReportHost(
    container: AppContainer,
    session: SessionScope?,
    navController: NavController,
) {
    val context = LocalContext.current
    val activity = remember(context) { context.findActivity() }
    val scope = rememberCoroutineScope()
    val requested by container.bugReportRequested.collectAsStateSafely(false)
    val reporter = remember(container) { BugReportController(container) }
    val store = container.pendingBugReport
    var pending by remember { mutableStateOf<PendingBugReport?>(null) }
    var sheetOpen by remember { mutableStateOf(false) }
    var prompt by remember { mutableStateOf<BugCapture?>(null) }
    var confirmNew by remember { mutableStateOf<BugCapture?>(null) }
    var confirmDiscard by remember { mutableStateOf(false) }
    var capturing by remember { mutableStateOf(false) }

    fun holdReport(report: PendingBugReport?) {
        pending = report
        scope.launch(Dispatchers.IO) {
            if (report == null) store.clear() else store.save(report)
        }
    }

    // What was captured before the app was last killed, back on screen
    // as the chip that says a report is open.
    LaunchedEffect(Unit) {
        if (pending != null) return@LaunchedEffect
        val held = withContext(Dispatchers.IO) { store.load() }
        if (held != null) {
            DiagLog.i(TAG, "a bug report with ${held.captureCount} captures is still open")
            pending = held
        }
    }

    val busy = sheetOpen || prompt != null || confirmNew != null || confirmDiscard
    val shakeEnabled = remember(context) { DiagPreferences.shakeToReport(context) }
    ShakeToReport(enabled = shakeEnabled && !busy) {
        container.requestBugReport()
    }

    LaunchedEffect(requested) {
        if (!requested || busy || capturing) return@LaunchedEffect
        val host = activity ?: run {
            container.bugReportRequested.value = false
            return@LaunchedEffect
        }
        capturing = true
        val entry = navController.currentBackStackEntry
        val route = entry?.destination?.route ?: BugCapture.UNKNOWN_ROUTE
        val bundle = entry?.arguments
        val arguments = entry?.destination?.arguments.orEmpty().keys.mapNotNull { key ->
            bundle?.getString(key)?.let { key to it }
        }.toMap()
        DiagLog.i(TAG, "capturing a bug report on route $route")
        val fresh = runCatching {
            reporter.capture(
                activity = host,
                session = session,
                route = route,
                arguments = arguments,
                withScreenshot = true,
            )
        }.getOrNull()
        capturing = false
        container.bugReportRequested.value = false
        if (fresh == null) return@LaunchedEffect
        val open = pending?.takeIf { !it.isExpired(System.currentTimeMillis()) }
        if (open == null) {
            holdReport(
                PendingBugReport(
                    startedAtMs = System.currentTimeMillis(),
                    submission = BugSubmission(),
                    capture = fresh,
                ),
            )
            sheetOpen = true
        } else {
            // A report is already open: the maintainer says whether
            // this screen belongs to it (issue #424).
            prompt = fresh
        }
    }

    // The marker. A report the maintainer walked away from is easy to
    // forget, so the shell says it is open and how much is on it, from
    // whatever screen they are on.
    val open = pending
    if (open != null && !busy) {
        Box(modifier = Modifier.fillMaxSize()) {
            AssistChip(
                onClick = { sheetOpen = true },
                label = { Text(markerLabel(open.captureCount)) },
                leadingIcon = {
                    Icon(imageVector = Icons.Default.BugReport, contentDescription = null)
                },
                modifier = Modifier
                    .align(Alignment.BottomStart)
                    .navigationBarsPadding()
                    .padding(start = 16.dp, bottom = 16.dp)
                    .testTag("bug-pending-chip"),
            )
        }
    }

    prompt?.let { fresh ->
        val held = open ?: return@let
        AlertDialog(
            onDismissRequest = { prompt = null },
            title = { Text("A report is open") },
            text = {
                Text(
                    "This screen can join the report you started, or start a report " +
                        "of its own.",
                )
            },
            confirmButton = {
                TextButton(
                    onClick = {
                        prompt = null
                        holdReport(held.copy(capture = held.capture.withShot(fresh.shots.first())))
                        sheetOpen = true
                    },
                    modifier = Modifier.testTag("bug-prompt-add"),
                ) {
                    Text("Add to the open report (${captures(held.captureCount)})")
                }
            },
            dismissButton = {
                TextButton(
                    onClick = {
                        prompt = null
                        confirmNew = fresh
                    },
                    modifier = Modifier.testTag("bug-prompt-new"),
                ) {
                    Text("Start a new report")
                }
            },
            modifier = Modifier.testTag("bug-prompt"),
        )
    }

    confirmNew?.let { fresh ->
        val held = open
        AlertDialog(
            onDismissRequest = { confirmNew = null },
            title = { Text("Drop the open report?") },
            text = {
                Text(
                    "The report you started carries ${captures(held?.captureCount ?: 0)} " +
                        "and has not been sent. Starting a new one drops it.",
                )
            },
            confirmButton = {
                TextButton(
                    onClick = {
                        confirmNew = null
                        DiagLog.i(TAG, "the open bug report was dropped for a new one")
                        holdReport(
                            PendingBugReport(
                                startedAtMs = System.currentTimeMillis(),
                                submission = BugSubmission(),
                                capture = fresh,
                            ),
                        )
                        sheetOpen = true
                    },
                    modifier = Modifier.testTag("bug-confirm-new"),
                ) {
                    Text("Drop it and start a new report")
                }
            },
            dismissButton = {
                TextButton(
                    onClick = { confirmNew = null },
                    modifier = Modifier.testTag("bug-keep-open"),
                ) {
                    Text("Keep it")
                }
            },
            modifier = Modifier.testTag("bug-confirm-new-dialog"),
        )
    }

    if (confirmDiscard) {
        val held = open
        AlertDialog(
            onDismissRequest = { confirmDiscard = false },
            title = { Text("Discard the report?") },
            text = {
                Text("The ${captures(held?.captureCount ?: 0)} on it are dropped unsent.")
            },
            confirmButton = {
                TextButton(
                    onClick = {
                        confirmDiscard = false
                        holdReport(null)
                    },
                    modifier = Modifier.testTag("bug-confirm-discard"),
                ) {
                    Text("Discard")
                }
            },
            dismissButton = {
                TextButton(
                    onClick = {
                        confirmDiscard = false
                        sheetOpen = true
                    },
                    modifier = Modifier.testTag("bug-keep-report"),
                ) {
                    Text("Keep it")
                }
            },
            modifier = Modifier.testTag("bug-confirm-discard-dialog"),
        )
    }

    if (!sheetOpen) return
    val report = open ?: return
    BugReportSheet(
        capture = report.capture,
        initial = report.submission,
        // Swiping the sheet away leaves the report open rather than
        // throwing away captures the maintainer did not ask to lose;
        // Discard is the way out.
        onDismiss = { sheetOpen = false },
        onAddAnother = { submission ->
            sheetOpen = false
            holdReport(report.copy(submission = submission))
            DiagLog.i(TAG, "the bug report stays open with ${report.captureCount} captures")
        },
        onRemoveCapture = { index ->
            val left = report.capture.withoutShot(index)
            if (left.shots.isEmpty()) {
                sheetOpen = false
                holdReport(null)
            } else {
                holdReport(report.copy(capture = left))
            }
        },
        onDiscard = {
            sheetOpen = false
            if (report.captureCount > 1) confirmDiscard = true else holdReport(null)
        },
        onSend = { submission ->
            sheetOpen = false
            holdReport(null)
            val window = UndoSendPreference.current(context).millis
            scope.launch {
                val outcome = runCatching {
                    reporter.send(submission, report.capture, session, window)
                }
                outcome.exceptionOrNull()?.let { failure ->
                    DiagLog.w(TAG, "the report could not be queued: ${failure.message}")
                }
                when (val result = outcome.getOrNull() ?: ComposeResult.Failed("the report could not be queued")) {
                    is ComposeResult.Queued -> {
                        container.undo.offer(SENDING, windowMs = window.takeIf { it > 0 }) {
                            container.outbox.remove(result.entryId)
                        }
                        session?.requestDrain?.invoke(window)
                    }

                    is ComposeResult.Failed -> {
                        DiagLog.w(TAG, "the report was not queued: ${result.message}")
                        container.undo.offer(result.message, windowMs = null, actionLabel = "Dismiss") {}
                    }

                    is ComposeResult.Saved -> Unit
                }
            }
        },
    )
}

/** What the marker says, which is how many screens the open report holds. */
private fun markerLabel(count: Int): String = "Report open: ${captures(count)}"

/** How many captures, said so one of them does not read as several. */
private fun captures(count: Int): String = if (count == 1) "1 capture" else "$count captures"

/** What the snackbar says while the report waits out its undo window. */
private const val SENDING = "Sending the report to the server"

private const val TAG = "herold.bugreport"

/** The Activity the composition is hosted by, for the window capture. */
private tailrec fun Context.findActivity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.findActivity()
    else -> null
}
