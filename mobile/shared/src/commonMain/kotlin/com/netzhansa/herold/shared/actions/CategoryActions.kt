package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.CategoryDisposition
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.outbox.MailboxPayload
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * Category settings: a label's `disposition` and its rank in the
 * principal's one priority list (issue #399, server issue #333, suite
 * REQ-CAT-04/05/11).
 *
 * The server owns both, so every change is a `Mailbox/set` and goes
 * through the durable outbox like any other mutation: the local row
 * changes at once, the call follows from the drain, and the drain
 * replaces the row with the server's version - which also carries the
 * dense renumbering a reorder triggers on the labels that did not move
 * (REQ-AND-SYNC-20..25).
 */
class CategoryActions(
    private val store: LocalStore,
    private val outbox: Outbox,
    private val requestDrain: () -> Unit = {},
) {
    /**
     * Sets [label]'s disposition. Pinning a label also gives it a rank,
     * at the end of the ranked list, so the tab row has an order to draw
     * (REQ-CAT-05/11).
     */
    suspend fun setDisposition(
        label: Mailbox,
        disposition: CategoryDisposition,
        all: List<Mailbox>,
    ): Long {
        val rank = if (disposition == CategoryDisposition.PINNED && label.priority == null) {
            nextRank(all, label.accountId)
        } else {
            label.priority
        }
        store.upsertMailboxes(listOf(label.copy(disposition = disposition, priority = rank)))
        return enqueue(
            accountId = label.accountId,
            label = MailActions.Labels.CATEGORY_DISPOSITION,
            updates = mapOf(
                label.id to buildJsonObject {
                    put("disposition", disposition.wire)
                    if (rank != label.priority) put("priority", rank)
                },
            ),
        )
    }

    /**
     * Moves the pinned category at [from] to [to] in the tab order, as a
     * drag leaves it. Every pinned label of the account is renumbered
     * from zero so the order is total, and the labels whose rank actually
     * moved travel as one `Mailbox/set`.
     */
    suspend fun reorderPinned(pinned: List<Mailbox>, from: Int, to: Int): Long? {
        val moved = reorder(pinned, from, to) ?: return null
        val patches = moved.filter { row ->
            pinned.firstOrNull { it.id == row.id }?.priority != row.priority
        }
        if (patches.isEmpty()) return null
        store.upsertMailboxes(moved)
        return enqueue(
            accountId = patches.first().accountId,
            label = MailActions.Labels.CATEGORY_ORDER,
            updates = patches.associate { row ->
                row.id to buildJsonObject { put("priority", row.priority) }
            },
        )
    }

    private suspend fun enqueue(
        accountId: String,
        label: String,
        updates: Map<String, JsonObject>,
    ): Long {
        val id = outbox.enqueueMailbox(
            accountId = accountId,
            label = label,
            payload = MailboxPayload(accountId = accountId, updates = updates),
        )
        requestDrain()
        return id
    }

    companion object {
        /**
         * What the user is told when the server refuses a sixth pinned
         * category (`tooManyPinned`, REQ-CAT-11).
         */
        const val TOO_MANY_PINNED =
            "at most ${CategoryDisposition.PINNED_LIMIT} categories can be pinned; unpin one first"

        /** The labels a settings screen offers a disposition for (REQ-CAT-13). */
        fun categoryLabels(mailboxes: List<Mailbox>, accountId: String?): List<Mailbox> =
            mailboxes
                .filter { it.role == null && (accountId == null || it.accountId == accountId) }
                .sortedWith(compareBy({ it.priority ?: Int.MAX_VALUE }, { it.name.lowercase() }))

        /** The pinned labels in tab order, the list a drag reorders. */
        fun pinnedLabels(mailboxes: List<Mailbox>, accountId: String?): List<Mailbox> =
            categoryLabels(mailboxes, accountId)
                .filter { it.disposition == CategoryDisposition.PINNED }

        /**
         * [labels] with the entry at [from] moved to [to], renumbered
         * densely from zero. Null when the move changes nothing.
         */
        fun reorder(labels: List<Mailbox>, from: Int, to: Int): List<Mailbox>? {
            if (from == to || from !in labels.indices || to !in labels.indices) return null
            val ordered = labels.toMutableList()
            ordered.add(to, ordered.removeAt(from))
            return ordered.mapIndexed { rank, row -> row.copy(priority = rank) }
        }

        /** The rank a newly ranked label takes: behind every ranked one. */
        private fun nextRank(all: List<Mailbox>, accountId: String): Int =
            (all.filter { it.accountId == accountId }.mapNotNull { it.priority }.maxOrNull() ?: -1) + 1
    }
}
