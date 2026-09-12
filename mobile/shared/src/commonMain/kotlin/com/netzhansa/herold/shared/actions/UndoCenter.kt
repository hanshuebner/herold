package com.netzhansa.herold.shared.actions

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.getAndUpdate

/**
 * An offer to take back what just happened: an action whose optimistic
 * write is in the store and whose outbox entry is queued, or a send
 * waiting out its undo window (issues #345, #354).
 *
 * [undo] is what taking it back does. A queued entry the drain has not
 * reached is simply dropped; one the server already has is followed by
 * its inverse.
 */
class UndoOffer internal constructor(
    /** What the snackbar says, "Archived", "Snoozed" or "Sending". */
    val message: String,
    /** How long the offer stands, in milliseconds; null for the default. */
    val windowMs: Long?,
    private val action: suspend () -> Unit,
) {
    suspend fun undo() = action()
}

/**
 * What an offer's snackbar says. Held here so the screen that starts an
 * action and the list that shows its offer name it the same way.
 */
object UndoMessages {
    const val ARCHIVED = "Archived"
    const val SNOOZED = "Snoozed"
    const val SENDING = "Sending"
}

/**
 * Where an action parks its undo offer for the screen that will show it
 * (issue #345). Archiving from an open conversation pops back to the list,
 * so the offer outlives the screen the user invoked it on: the thread view
 * writes the change and leaves the offer here, and the list picks it up
 * when it is returned to.
 */
class UndoCenter {

    private val _pending = MutableStateFlow<UndoOffer?>(null)

    /** The offer waiting to be shown, null when none is outstanding. */
    val pending: StateFlow<UndoOffer?> = _pending.asStateFlow()

    /**
     * Queues [action] and parks its undo offer. Returns null, and queues
     * nothing, when the action changed nothing - a settling swipe can ask
     * twice, and the second pass has nothing to offer an undo for.
     */
    suspend fun offer(message: String, action: PendingAction, actions: MailActions): UndoOffer? {
        if (action.isEmpty) return null
        actions.commit(action)
        val offer = UndoOffer(message, null) { actions.undo(action) }
        _pending.value = offer
        return offer
    }

    /** Parks an offer whose undo is something other than a mail action. */
    fun offer(message: String, windowMs: Long?, undo: suspend () -> Unit): UndoOffer {
        val offer = UndoOffer(message, windowMs, undo)
        _pending.value = offer
        return offer
    }

    /**
     * Takes the parked offer, leaving none behind, so exactly one screen
     * shows it however many are watching.
     */
    fun take(): UndoOffer? = _pending.getAndUpdate { null }
}
