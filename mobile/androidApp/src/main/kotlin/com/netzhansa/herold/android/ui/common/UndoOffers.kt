package com.netzhansa.herold.android.ui.common

import androidx.compose.material3.SnackbarDuration
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.SnackbarResult
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.shared.actions.UndoOffer
import kotlinx.coroutines.withTimeoutOrNull

/**
 * Shows the undo offers actions park in the container (issue #345).
 *
 * A message list mounts this next to its snackbar host. An action invoked
 * on the list itself parks its offer and this picks it up at once; an
 * action invoked in the thread view parks its offer and pops back, and
 * this picks it up when the list is composed again - so the affordance
 * appears on the screen the user lands on, whichever screen started the
 * action.
 *
 * The offer is taken rather than observed, so two lists watching at once
 * still show it once.
 */
@Composable
fun UndoOffers(container: AppContainer, snackbar: SnackbarHostState) {
    LaunchedEffect(container) {
        container.undo.pending.collect { parked ->
            if (parked == null) return@collect
            val offer = container.undo.take() ?: return@collect
            show(offer, snackbar)
        }
    }
}

/**
 * The snackbar for one offer. An offer with its own window - a send
 * holding for its undo period (issue #354) - comes down when that window
 * is up, so the affordance is gone exactly when taking it back stops
 * being possible.
 */
private suspend fun show(offer: UndoOffer, snackbar: SnackbarHostState) {
    // One offer at a time: a snackbar still up from an earlier action
    // would otherwise hold this one in the host's queue, invisible.
    snackbar.currentSnackbarData?.dismiss()
    suspend fun raise() = snackbar.showSnackbar(
        message = offer.message,
        actionLabel = UNDO_LABEL,
        withDismissAction = true,
        duration = SnackbarDuration.Long,
    )
    val window = offer.windowMs
    val result = if (window == null) raise() else withTimeoutOrNull(window) { raise() }
    if (result == SnackbarResult.ActionPerformed) offer.undo()
}

const val UNDO_LABEL = "Undo"
