package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.actions.MailActions
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.store.LocalStore

/**
 * What one compose's draft is, as far as the client knows right now
 * (issue #371).
 *
 * A save reaches the server after the composer has closed, and a queued
 * save reaches it later still, so the ids a draft can be addressed by
 * appear over time. The handle is what identifies the draft before any
 * of them exists: the discard offer holds this, and [Drafts] reads the
 * ids out of it when the discard is taken - or applies the discard to
 * the save when the save is the one that arrives second.
 */
class DraftHandle(accountId: String = "") {

    /** The account the compose is on; the identity picker can move it. */
    var accountId: String = accountId
        internal set

    /** The message the save wrote, once the server has answered. */
    var draftId: String? = null
        internal set

    /** The queue entry the save is waiting in, when it was queued. */
    var entryId: Long? = null
        internal set

    /** True once the user has thrown this draft away. */
    var isDiscarded: Boolean = false
        internal set
}

/**
 * The draft side of compose that outlives the composer: what a save
 * produced, and what a discard does about it (issue #371, suite
 * REQ-DFT-42).
 *
 * A discard takes the draft out of the local store at once - the
 * conversation renders from the store, so the card goes with the row -
 * and queues the destroy in the durable outbox, where it is retried
 * like every other write and listed with its reason when the server
 * refuses it. A discard taken before the save has an id is remembered
 * against the handle, so the message the save goes on to create is
 * taken away as soon as it exists.
 */
class Drafts(
    private val store: LocalStore,
    private val outbox: Outbox,
    /** Asks the sync engine for a drain; a no-op when nothing can run one. */
    private val requestDrain: () -> Unit = {},
) {

    /** The save wrote [draftId]; the discard that came first still applies. */
    suspend fun saved(handle: DraftHandle, accountId: String, draftId: String) {
        handle.accountId = accountId
        handle.draftId = draftId
        if (handle.isDiscarded) destroy(accountId, draftId)
    }

    /** The save is queued as [entryId]; a discard taken already still applies. */
    suspend fun queued(handle: DraftHandle, accountId: String, entryId: Long) {
        handle.accountId = accountId
        handle.entryId = entryId
        if (handle.isDiscarded) discard(handle)
    }

    /**
     * Throws the draft away, whatever the save has reached: a queue
     * entry the drain has not taken is dropped, one it is submitting is
     * marked so the drain destroys what it writes, one it has already
     * submitted is followed to the message it left, and a message the
     * server holds is destroyed through the outbox.
     */
    suspend fun discard(handle: DraftHandle) {
        handle.isDiscarded = true
        handle.entryId?.let { entryId ->
            when {
                // Not submitted yet: the save simply does not happen.
                outbox.cancelIfQueued(entryId) != null -> handle.entryId = null
                // Being submitted: the drain takes away what it writes.
                outbox.markDiscardAfterWrite(entryId) -> Unit
                // Already written: the message it left is the one to go.
                else -> outbox.writtenDraftId(entryId)?.let { handle.draftId = it }
            }
        }
        handle.draftId?.let { destroy(handle.accountId, it) }
    }

    /**
     * Throws away a draft that is already a message, named by its id:
     * what the conversation's own Discard acts on, for a draft whose
     * save has long since landed and whose snackbar offer is gone
     * (issue #371).
     */
    suspend fun discardSaved(accountId: String, draftId: String) = destroy(accountId, draftId)

    private suspend fun destroy(accountId: String, draftId: String) {
        store.deleteEmails(accountId, listOf(draftId))
        outbox.enqueueDestroy(accountId, MailActions.Labels.DISCARD_DRAFT, listOf(draftId))
        requestDrain()
    }
}
