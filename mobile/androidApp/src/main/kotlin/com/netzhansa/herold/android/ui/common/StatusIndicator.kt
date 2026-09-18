package com.netzhansa.herold.android.ui.common

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.shared.sync.AppStatus

/** The touch target the dot sits in; it is the same in every state. */
private val SLOT = 40.dp

/** The dot itself. */
private val DOT = 10.dp

/** How long one breath of the in-flight animation takes. */
private const val PULSE_MS = 700

/**
 * What the client's dealings with the server look like, in one dot
 * (REQ-AND-SYNC-30). It lives in the top app bar's action row and keeps
 * its slot in every state, so nothing in the content column moves when
 * the connection drops, a sync starts or one fails: the screen the user
 * is reading stays exactly where it was.
 *
 * Idle is a faint dot, an in-flight interaction pulses, offline is a
 * struck-through muted dot and a failure is an accented one. The detail
 * behind any of them - the last error, the queue, the log ring - is on
 * the diagnostics screen, which a tap opens (REQ-AND-SYS-54).
 */
@Composable
fun StatusIndicator(
    status: AppStatus,
    onOpenDiagnostics: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val colours = MaterialTheme.colorScheme
    val colour = when (status) {
        AppStatus.IDLE -> colours.onSurfaceVariant
        AppStatus.BUSY -> colours.primary
        AppStatus.OFFLINE -> colours.onSurfaceVariant
        AppStatus.FAILED -> colours.error
    }
    val description = statusDescription(status)

    Box(
        modifier = modifier
            .size(SLOT)
            .clickable(onClick = onOpenDiagnostics)
            .semantics { contentDescription = description }
            .testTag("status-indicator"),
        contentAlignment = Alignment.Center,
    ) {
        // The pulse is an infinite animation, so it is composed only
        // while something is actually in flight: a permanently running
        // animation would keep the frame clock busy for as long as the
        // app is open.
        val opacity = if (status == AppStatus.BUSY) pulseAlpha() else staticAlpha(status)
        Canvas(
            modifier = Modifier
                .size(DOT)
                .alpha(opacity)
                .testTag("status-dot-${status.name.lowercase()}"),
        ) {
            if (status == AppStatus.OFFLINE) {
                // A struck ring, so the offline state is told apart from
                // the idle dot by its shape and not by colour alone.
                val stroke = size.minDimension / 5f
                drawCircle(color = colour, radius = (size.minDimension - stroke) / 2f, style = Stroke(stroke))
                drawLine(
                    color = colour,
                    start = Offset(0f, size.height),
                    end = Offset(size.width, 0f),
                    strokeWidth = stroke,
                    cap = StrokeCap.Round,
                )
            } else {
                drawCircle(color = colour)
            }
        }
    }
}

/** How low the idle dot sits: present, and not asking for attention. */
private const val IDLE_ALPHA = 0.25f

/** The breathing the in-flight dot does. */
@Composable
private fun pulseAlpha(): Float {
    val pulse by rememberInfiniteTransition(label = "status-pulse").animateFloat(
        initialValue = 0.35f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(tween(PULSE_MS), RepeatMode.Reverse),
        label = "status-pulse-alpha",
    )
    return pulse
}

private fun staticAlpha(status: AppStatus): Float = if (status == AppStatus.IDLE) IDLE_ALPHA else 1f

/** What TalkBack reads, and what the diagnostics screen repeats in words. */
fun statusDescription(status: AppStatus): String = when (status) {
    AppStatus.IDLE -> "Up to date"
    AppStatus.BUSY -> "Talking to the server"
    AppStatus.OFFLINE -> "Offline"
    AppStatus.FAILED -> "Last attempt failed"
}
