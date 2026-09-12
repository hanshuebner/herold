package com.netzhansa.herold.shared.outbox

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import kotlinx.serialization.json.JsonObject

/**
 * The durable queue of pending mutations (REQ-AND-SYNC-20..25). Actions and
 * compose write here; [OutboxDrainer] is the only thing that submits from
 * here. Everything it holds is in the local store, so a queue built with no
 * connectivity is still there after the process dies (REQ-AND-SYNC-22).
 */
class Outbox(
    private val store: LocalStore,
    private val now: () -> Long = { 0L },
) {
    /** Every entry, oldest first; what the outbox screen renders. */
    val entries: Flow<List<OutboxEntry>> = store.outbox()

    /** How many entries are still waiting, for the connectivity chip. */
    val pendingCount: Flow<Int> = entries.map { list -> list.count { it.isPending } }

    suspend fun list(): List<OutboxEntry> = store.outboxList()

    suspend fun entry(id: Long): OutboxEntry? = store.outboxEntry(id)

    /**
     * Queues the `Email/set` of an optimistic action whose rows are already
     * written locally. [snapshot] is what a permanent rejection restores.
     */
    suspend fun enqueueAction(
        accountId: String,
        label: String,
        patches: Map<String, JsonObject>,
        snapshot: List<Email>,
    ): Long = store.enqueueOutbox(
        NewOutboxEntry(
            accountId = accountId,
            kind = OutboxKind.ACTION,
            label = label,
            payload = outboxJson.encodeToString(ActionPayload(patches)),
            revertJson = outboxJson.encodeToString(snapshot.map { it.toSnapshot() }),
            entityIds = patches.keys.toList(),
            createdAt = now(),
        ),
    )

    /**
     * Queues a composed message. [holdUntilMs] is the instant the drain may
     * first submit it, which is how the undo window after Send is realised
     * (issue #354): until then the entry sits in the queue and an undo
     * simply removes it.
     */
    suspend fun enqueueCompose(
        kind: OutboxKind,
        label: String,
        payload: ComposePayload,
        holdUntilMs: Long = 0,
    ): Long {
        val id = store.enqueueOutbox(
            NewOutboxEntry(
                accountId = payload.accountId,
                kind = kind,
                label = label,
                payload = outboxJson.encodeToString(payload),
                createdAt = now(),
            ),
        )
        if (holdUntilMs > 0) {
            store.updateOutboxState(
                id = id,
                state = OutboxState.QUEUED,
                attempts = 0,
                lastError = null,
                permanent = false,
                nextAttemptAt = holdUntilMs,
            )
        }
        return id
    }

    suspend fun updatePayload(id: Long, payload: ComposePayload) {
        store.updateOutboxPayload(id, outboxJson.encodeToString(payload))
    }

    /**
     * Drops an entry that has not been submitted yet, for an undo taken
     * before the drain reached it. Returns the entry when it was still
     * there to drop, null when the server already has it.
     */
    suspend fun cancelIfQueued(id: Long): OutboxEntry? {
        val entry = store.outboxEntry(id) ?: return null
        if (entry.state != OutboxState.QUEUED) return null
        store.deleteOutbox(id)
        return entry
    }

    /**
     * Drops a queued compose and hands back what it held, so the
     * composer can reopen on it - the undo of a send still inside its
     * window (issue #354). Null when the drain already took it.
     */
    suspend fun cancelCompose(id: Long): ComposePayload? {
        val entry = cancelIfQueued(id) ?: return null
        return runCatching { outboxJson.decodeFromString<ComposePayload>(entry.payload) }.getOrNull()
    }

    /** Drops an entry outright, whatever its state; the outbox screen's discard. */
    suspend fun remove(id: Long) = store.deleteOutbox(id)

    /** Puts a failed entry back in the queue for the next drain (REQ-AND-SYNC-25). */
    suspend fun retry(id: Long) {
        val entry = store.outboxEntry(id) ?: return
        store.updateOutboxState(
            id = entry.id,
            state = OutboxState.QUEUED,
            attempts = 0,
            lastError = entry.lastError,
            permanent = false,
            nextAttemptAt = 0,
        )
    }

    /** Puts every failed entry back in the queue. */
    suspend fun retryAll() {
        store.outboxList().filter { it.state == OutboxState.FAILED }.forEach { retry(it.id) }
    }

    /**
     * Discards queued writes for messages a reconciliation has just
     * delivered server truth for: the server's version wins and the
     * optimistic one goes (REQ-AND-SYNC-24). An entry already in flight is
     * left alone - its own result is what is arriving.
     */
    suspend fun discardSupersededBy(accountId: String, ids: Collection<String>): List<OutboxEntry> {
        if (ids.isEmpty()) return emptyList()
        val touched = ids.toSet()
        val superseded = store.outboxList().filter {
            it.accountId == accountId &&
                it.kind == OutboxKind.ACTION &&
                it.state == OutboxState.QUEUED &&
                it.entityIds.any { id -> id in touched }
        }
        superseded.forEach { store.deleteOutbox(it.id) }
        return superseded
    }
}

/** The membership snapshot an action's revert restores. */
fun Email.toSnapshot(): MembershipSnapshot = MembershipSnapshot(
    accountId = accountId,
    id = id,
    keywords = keywords.toList(),
    mailboxIds = mailboxIds.toList(),
    snoozedUntil = snoozedUntil,
)
