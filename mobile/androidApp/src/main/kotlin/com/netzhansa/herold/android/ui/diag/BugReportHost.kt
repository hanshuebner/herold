package com.netzhansa.herold.android.ui.diag

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
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
import kotlinx.coroutines.launch

/**
 * The reporter, mounted over every screen of the shell
 * (REQ-AND-SYS-50..53). It answers both entry points - the menu items
 * and the shake - by capturing first and opening the sheet second, so
 * what the report carries is the screen the user was complaining about
 * rather than the sheet covering it.
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
    var capture by remember { mutableStateOf<BugCapture?>(null) }
    var capturing by remember { mutableStateOf(false) }

    val shakeEnabled = remember(context) { DiagPreferences.shakeToReport(context) }
    ShakeToReport(enabled = shakeEnabled && capture == null) {
        container.requestBugReport()
    }

    LaunchedEffect(requested) {
        if (!requested || capture != null || capturing) return@LaunchedEffect
        val host = activity ?: run {
            container.bugReportRequested.value = false
            return@LaunchedEffect
        }
        capturing = true
        val entry = navController.currentBackStackEntry
        val route = entry?.destination?.route ?: "unknown"
        val bundle = entry?.arguments
        val arguments = entry?.destination?.arguments.orEmpty().keys.mapNotNull { key ->
            bundle?.getString(key)?.let { key to it }
        }.toMap()
        DiagLog.i(TAG, "capturing a bug report on route $route")
        capture = runCatching {
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
    }

    val pending = capture ?: return
    BugReportSheet(
        capture = pending,
        onDismiss = { capture = null },
        onSend = { submission ->
            capture = null
            val hold = UndoSendPreference.current(context).millis
            scope.launch {
                when (val result = reporter.send(submission, pending, hold)) {
                    is ComposeResult.Queued -> {
                        container.undo.offer(SENDING, windowMs = hold.takeIf { it > 0 }) {
                            container.outbox.remove(result.entryId)
                        }
                        session?.requestDrain?.invoke(hold)
                    }

                    is ComposeResult.Failed ->
                        container.undo.offer(result.message, windowMs = null, actionLabel = "Dismiss") {}

                    is ComposeResult.Saved -> Unit
                }
            }
        },
    )
}

/** What the snackbar says while the report waits out its undo window. */
private const val SENDING = "Sending the report"

private const val TAG = "herold.bugreport"

/** The Activity the composition is hosted by, for the window capture. */
private tailrec fun Context.findActivity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.findActivity()
    else -> null
}
