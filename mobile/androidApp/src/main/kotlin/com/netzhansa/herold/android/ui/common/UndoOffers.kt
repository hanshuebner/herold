package com.netzhansa.herold.android.ui.common

import androidx.compose.material3.SnackbarDuration
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.SnackbarResult
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.shared.actions.ActionResult
import com.netzhansa.herold.shared.actions.UndoOffer
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.coroutineScope

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
fun UndoOffers(container: AppContainer, session: SessionScope, snackbar: SnackbarHostState) {
    LaunchedEffect(container, session) {
        container.undo.pending.collect { parked ->
            if (parked == null) return@collect
            val offer = container.undo.take() ?: return@collect
            show(offer, session, snackbar)
        }
    }
}

/**
 * The snackbar for one offer: up with the optimistic write, down when the
 * server rejects the change, and an undo that puts the messages back.
 */
private suspend fun show(
    offer: UndoOffer,
    session: SessionScope,
    snackbar: SnackbarHostState,
) = coroutineScope {
    // One offer at a time: a snackbar still up from an earlier action
    // would otherwise hold this one in the host's queue, invisible.
    snackbar.currentSnackbarData?.dismiss()
    val shown = async {
        snackbar.showSnackbar(
            message = offer.message,
            actionLabel = UNDO_LABEL,
            withDismissAction = true,
            duration = SnackbarDuration.Long,
        )
    }

    val result = offer.commit.await()
    if (result is ActionResult.Reverted) {
        shown.cancelAndJoin()
        snackbar.showSnackbar(result.message)
        return@coroutineScope
    }
    if (shown.await() == SnackbarResult.ActionPerformed) {
        val restored = session.actions.restore(offer.snapshot)
        if (restored is ActionResult.Reverted) snackbar.showSnackbar(restored.message)
    }
}

const val UNDO_LABEL = "Undo"
