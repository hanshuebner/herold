package com.netzhansa.herold.shared.actions

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

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
    /**
     * When the offer stops being takeable, in epoch milliseconds; null
     * when it stands for as long as its snackbar does. It is an instant
     * rather than a duration because the screen that shows the offer is
     * not always the screen that parked it: a send takes its undo window
     * with it, so a snackbar raised on the screen the composer returned
     * to comes down when the hold is up, not a full window later.
     */
    val expiresAtMs: Long?,
    /**
     * What the snackbar's action reads: "Undo", or "Discard" for a saved
     * draft. Null when the message is a confirmation with nothing to
     * take back, which is what a sent bug report leaves (issue #438).
     */
    val actionLabel: String?,
    /**
     * The surface that parked the offer on its way off screen, when the
     * offer belongs to the screen the user lands on; null when whichever
     * surface is up may show it. Archiving from an open conversation pops
     * back to the list, so the thread view hands its offer on rather than
     * showing it for the instant it has left.
     */
    val handedOnBy: Any?,
    private val action: suspend () -> Unit,
) {
    /** How much of the offer is left at [now]; null when it has no deadline. */
    fun remainingMs(now: Long): Long? = expiresAtMs?.let { (it - now).coerceAtLeast(0) }

    /** True when taking it back is no longer possible. */
    fun isExpired(now: Long): Boolean = remainingMs(now) == 0L

    suspend fun undo() = action()
}

/**
 * What an offer's snackbar says. Held here so the screen that starts an
 * action and the list that shows its offer name it the same way.
 */
object UndoMessages {
    const val ARCHIVED = "Archived"
    const val DELETED = "Deleted"
    const val SNOOZED = "Snoozed"
    const val SENDING = "Sending"
    const val DRAFT_SAVED = "Draft saved"

    /** A discard the server refused; the draft is back where it was. */
    const val DISCARD_FAILED = "The draft was not discarded"
}

/** What an offer's action reads. */
object UndoActions {
    const val UNDO = "Undo"
    const val DISCARD = "Discard"

    /** What a message with nothing to take back offers. */
    const val DISMISS = "Dismiss"
}

/**
 * Where an action parks its undo offer for the screen that will show it
 * (issue #345). Archiving from an open conversation pops back to the list,
 * so the offer outlives the screen the user invoked it on: the thread view
 * writes the change and leaves the offer here, and the list picks it up
 * when it is returned to.
 */
class UndoCenter(private val now: () -> Long = { 0L }) {

    private val _pending = MutableStateFlow<UndoOffer?>(null)

    /** The offer waiting to be shown, null when none is outstanding. */
    val pending: StateFlow<UndoOffer?> = _pending.asStateFlow()

    /**
     * Queues [action] and parks its undo offer. Returns null, and queues
     * nothing, when the action changed nothing - a settling swipe can ask
     * twice, and the second pass has nothing to offer an undo for.
     */
    suspend fun offer(
        message: String,
        action: PendingAction,
        actions: MailActions,
        handOnFrom: Any? = null,
    ): UndoOffer? {
        if (action.isEmpty) return null
        actions.commit(action)
        val offer = UndoOffer(message, null, UndoActions.UNDO, handOnFrom) { actions.undo(action) }
        _pending.value = offer
        return offer
    }

    /**
     * Parks an offer whose undo is something other than a mail action.
     * [windowMs] is how long it may still be taken back from now, and a
     * null [actionLabel] leaves the snackbar a confirmation with no
     * action on it.
     */
    fun offer(
        message: String,
        windowMs: Long?,
        actionLabel: String? = UndoActions.UNDO,
        handOnFrom: Any? = null,
        undo: suspend () -> Unit,
    ): UndoOffer {
        val offer = UndoOffer(message, windowMs?.let { now() + it }, actionLabel, handOnFrom, undo)
        _pending.value = offer
        return offer
    }

    /**
     * Takes the parked offer for [surface], leaving none behind, so
     * exactly one screen shows it however many are watching. An offer
     * whose window ran out before any screen picked it up is dropped
     * rather than shown: the send it belonged to has already left. An
     * offer a surface handed on stays parked for that surface, so it
     * waits for the screen the user lands on (issue #378).
     */
    fun take(surface: Any? = null): UndoOffer? {
        val parked = _pending.value ?: return null
        if (parked.isExpired(now())) {
            _pending.compareAndSet(parked, null)
            return null
        }
        if (surface != null && surface === parked.handedOnBy) return null
        return if (_pending.compareAndSet(parked, null)) parked else null
    }
}
