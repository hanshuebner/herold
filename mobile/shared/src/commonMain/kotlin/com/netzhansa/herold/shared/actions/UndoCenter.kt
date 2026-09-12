package com.netzhansa.herold.shared.actions

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.getAndUpdate

/**
 * An action whose optimistic write is in the local store, whose
 * `Email/set` is in flight, and whose undo has not been offered yet.
 *
 * [commit] is the server half, started when the offer was made; a screen
 * that shows the offer awaits it to learn whether the change held.
 */
class UndoOffer internal constructor(
    /** What the snackbar says, "Archived" or "Snoozed". */
    val message: String,
    private val pending: PendingAction,
    val commit: Deferred<ActionResult>,
) {
    /** What an undo restores: the messages as they were before the action. */
    val snapshot: ActionSnapshot get() = pending.snapshot
}

/**
 * What an offer's snackbar says. Held here so the screen that starts an
 * action and the list that shows its offer name it the same way.
 */
object UndoMessages {
    const val ARCHIVED = "Archived"
    const val SNOOZED = "Snoozed"
}

/**
 * Where an action parks its undo offer for the screen that will show it
 * (issue #345). Archiving from an open conversation pops back to the list,
 * so the offer outlives the screen the user invoked it on: the thread view
 * writes the change and leaves the offer here, and the list picks it up
 * when it is returned to.
 *
 * The commit runs in this object's scope rather than a composable's, so
 * leaving the screen does not cancel the `Email/set` half of an action the
 * user already saw take effect.
 */
class UndoCenter(private val scope: CoroutineScope) {

    private val _pending = MutableStateFlow<UndoOffer?>(null)

    /** The offer waiting to be shown, null when none is outstanding. */
    val pending: StateFlow<UndoOffer?> = _pending.asStateFlow()

    /**
     * Commits [action] and parks its undo offer. Returns null, and sends
     * nothing, when the action changed nothing - a settling swipe can ask
     * twice, and the second pass has nothing to offer an undo for.
     */
    fun offer(message: String, action: PendingAction, actions: MailActions): UndoOffer? {
        if (action.isEmpty) return null
        val offer = UndoOffer(message, action, scope.async { actions.commit(action) })
        _pending.value = offer
        return offer
    }

    /**
     * Takes the parked offer, leaving none behind, so exactly one screen
     * shows it however many are watching.
     */
    fun take(): UndoOffer? = _pending.getAndUpdate { null }
}
