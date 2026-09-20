package com.netzhansa.herold.android.ui.diag

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.offset
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
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import androidx.navigation.NavController
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.diag.BugReportController
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.android.diag.DiagPreferences
import com.netzhansa.herold.android.diag.ShakeToReport
import com.netzhansa.herold.android.ui.common.collectAsStateSafely
import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.diag.BugCapture
import com.netzhansa.herold.shared.diag.BugSubmission
import com.netzhansa.herold.shared.diag.PendingBugReport
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlin.math.roundToInt

/**
 * The reporter, mounted over every screen of the shell
 * (REQ-AND-SYS-50..53). It answers both entry points - the menu items
 * and the shake - by capturing first and opening the sheet second, so
 * what the report carries is the screen the user was complaining about
 * rather than the sheet covering it.
 *
 * A report stays open across screens (issue #424): "Add another
 * capture" puts the shell back with the report held and a chip says how
 * many screens it carries. Tapping the chip captures the screen the
 * maintainer walked to and puts it on the report; a shake or "Report a
 * problem" asks first, since those may well mean a new problem. The
 * chip is dragged aside when it sits on the thing being reported. What
 * is held is written to app storage, so the captures survive the app
 * being killed between two of them.
 *
 * Sending goes through the outbox and leaves at once: a report is not
 * correspondence and has nothing to take back, so it is queued with no
 * hold and the drain is asked for on the tap (issue #438). The
 * confirmation says the report is on its way, or that it waits for a
 * connection when there is none.
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
    var markerTaps by remember { mutableStateOf(0) }
    // Where the maintainer dragged the marker, held for as long as the
    // report is: its default corner sometimes sits on the very thing
    // the report is about.
    var markerOffset by remember { mutableStateOf(Offset.Zero) }
    var markerSize by remember { mutableStateOf(IntSize.Zero) }
    var shellSize by remember { mutableStateOf(IntSize.Zero) }
    val marginPx = with(LocalDensity.current) { MARKER_MARGIN.toPx() }

    fun holdReport(report: PendingBugReport?) {
        if (report == null) markerOffset = Offset.Zero
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

    // The window and the app's state, taken where the user is
    // standing. Both entry points take it the same way; what differs is
    // what becomes of it.
    suspend fun takeCapture(): BugCapture? {
        val host = activity ?: return null
        val entry = navController.currentBackStackEntry
        val route = entry?.destination?.route ?: BugCapture.UNKNOWN_ROUTE
        val bundle = entry?.arguments
        val arguments = entry?.destination?.arguments.orEmpty().keys.mapNotNull { key ->
            bundle?.getString(key)?.let { key to it }
        }.toMap()
        DiagLog.i(TAG, "capturing a bug report on route $route")
        return runCatching {
            reporter.capture(
                activity = host,
                session = session,
                route = route,
                arguments = arguments,
                withScreenshot = true,
            )
        }.getOrNull()
    }

    /** Puts [fresh] on the open report and shows it. */
    fun addToTheOpenReport(held: PendingBugReport, fresh: BugCapture) {
        holdReport(held.copy(capture = held.capture.withShot(fresh.shots.first())))
        sheetOpen = true
    }

    /** Starts a report on [fresh] and shows it. */
    fun startAReport(fresh: BugCapture) {
        holdReport(
            PendingBugReport(
                startedAtMs = System.currentTimeMillis(),
                submission = BugSubmission(),
                capture = fresh,
            ),
        )
        sheetOpen = true
    }

    LaunchedEffect(requested) {
        if (!requested || busy || capturing) return@LaunchedEffect
        if (activity == null) {
            container.bugReportRequested.value = false
            return@LaunchedEffect
        }
        capturing = true
        val fresh = takeCapture()
        capturing = false
        container.bugReportRequested.value = false
        if (fresh == null) return@LaunchedEffect
        val open = pending?.takeIf { !it.isExpired(System.currentTimeMillis()) }
        if (open == null) {
            startAReport(fresh)
        } else {
            // A report is already open: the shake and the menu entry ask
            // whether this screen belongs to it (issue #424).
            prompt = fresh
        }
    }

    // The marker's own tap: the maintainer pointing at the open report
    // means this screen goes on it, so it is captured and added with
    // nothing to answer.
    LaunchedEffect(markerTaps) {
        if (markerTaps == 0 || busy || capturing) return@LaunchedEffect
        val held = pending ?: return@LaunchedEffect
        capturing = true
        val fresh = takeCapture()
        capturing = false
        if (fresh == null) return@LaunchedEffect
        if (held.isExpired(System.currentTimeMillis())) startAReport(fresh) else addToTheOpenReport(held, fresh)
    }

    // The marker. A report the maintainer walked away from is easy to
    // forget, so the shell says it is open and how much is on it, from
    // whatever screen they are on, and tapping it puts that screen on
    // the report.
    val open = pending
    if (open != null && !busy) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .onSizeChanged { shellSize = it },
        ) {
            AssistChip(
                onClick = { markerTaps++ },
                label = { Text(markerLabel(open.captureCount)) },
                leadingIcon = {
                    Icon(imageVector = Icons.Default.BugReport, contentDescription = null)
                },
                modifier = Modifier
                    .align(Alignment.BottomStart)
                    .navigationBarsPadding()
                    .padding(start = MARKER_MARGIN, bottom = MARKER_MARGIN)
                    .offset { IntOffset(markerOffset.x.roundToInt(), markerOffset.y.roundToInt()) }
                    .onSizeChanged { markerSize = it }
                    // Dragged out of the way and kept there. The
                    // detector waits out the touch slop, so a tap still
                    // reaches the chip and takes a capture.
                    .pointerInput(Unit) {
                        detectDragGestures { change, delta ->
                            change.consume()
                            val room = Offset(
                                (shellSize.width - markerSize.width - 2 * marginPx).coerceAtLeast(0f),
                                (shellSize.height - markerSize.height - 2 * marginPx).coerceAtLeast(0f),
                            )
                            markerOffset = Offset(
                                (markerOffset.x + delta.x).coerceIn(0f, room.x),
                                (markerOffset.y + delta.y).coerceIn(-room.y, 0f),
                            )
                        }
                    }
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
                        addToTheOpenReport(held, fresh)
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
                        startAReport(fresh)
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
            scope.launch {
                val outcome = runCatching {
                    reporter.send(submission, report.capture, session)
                }
                outcome.exceptionOrNull()?.let { failure ->
                    DiagLog.w(TAG, "the report could not be queued: ${failure.message}")
                }
                when (val result = outcome.getOrNull() ?: ComposeResult.Failed("the report could not be queued")) {
                    is ComposeResult.Queued -> {
                        // A report has nothing to take back, so the
                        // confirmation carries no action and the drain
                        // is asked for at once (issue #438). With no
                        // connection the entry stays in the queue and
                        // the confirmation says so.
                        container.undo.offer(
                            if (container.offline.value) WAITING_FOR_A_CONNECTION else ON_ITS_WAY,
                            windowMs = null,
                            actionLabel = null,
                        ) {}
                        session?.requestDrain?.invoke(0)
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

/** How far the marker sits from the corner it rests in. */
private val MARKER_MARGIN = 16.dp

/** What the marker says, which is how many screens the open report holds. */
private fun markerLabel(count: Int): String = "Add to report (${captures(count)})"

/** How many captures, said so one of them does not read as several. */
private fun captures(count: Int): String = if (count == 1) "1 capture" else "$count captures"

/** What the snackbar says once the report is queued and its upload has started. */
private const val ON_ITS_WAY = "The report is on its way to the server"

/** What it says instead when there is no connection to send it over. */
private const val WAITING_FOR_A_CONNECTION =
    "Offline - the report goes out when the connection is back"

private const val TAG = "herold.bugreport"

/** The Activity the composition is hosted by, for the window capture. */
private tailrec fun Context.findActivity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.findActivity()
    else -> null
}
