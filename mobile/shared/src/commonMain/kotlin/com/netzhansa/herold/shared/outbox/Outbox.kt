package com.netzhansa.herold.shared.outbox

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
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
    private val writtenMutex = Mutex()

    /**
     * The message each compose entry wrote, for a discard that arrives
     * after the drain has taken the entry away (issue #371). The entry
     * id is what a compose still waiting for its save is known by, so
     * this is how that name reaches the message the save created. The
     * last few are kept, which covers the offers that can still be
     * taken.
     */
    private val written = mutableMapOf<Long, String>()
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
     * Queues the destroy of messages whose local rows are already gone
     * (issue #371): the draft a discard threw away. It travels like
     * every other write, so a destroy that could not be submitted is
     * retried, and one the server refuses is listed with its reason
     * rather than swallowed.
     */
    suspend fun enqueueDestroy(
        accountId: String,
        label: String,
        ids: List<String>,
    ): Long = store.enqueueOutbox(
        NewOutboxEntry(
            accountId = accountId,
            kind = OutboxKind.DESTROY,
            label = label,
            payload = outboxJson.encodeToString(DestroyPayload(accountId, ids)),
            entityIds = ids,
            createdAt = now(),
        ),
    )

    /**
     * Queues a filter-rule write (suite REQ-FLT-20). It carries no
     * membership snapshot: the rows a rule write touches are the account's
     * rules, which the drain reads back from the server on success.
     */
    suspend fun enqueueRule(
        accountId: String,
        label: String,
        payload: RulePayload,
    ): Long = store.enqueueOutbox(
        NewOutboxEntry(
            accountId = accountId,
            kind = OutboxKind.RULE,
            label = label,
            payload = outboxJson.encodeToString(payload),
            createdAt = now(),
        ),
    )

    /**
     * Queues a label write (issue #399): the `Mailbox/set` that changes a
     * category's disposition or its rank. The local rows are written by
     * the caller, so the settings screen reflects the change at once and
     * the server's answer replaces it when the drain reads the labels
     * back.
     */
    suspend fun enqueueMailbox(
        accountId: String,
        label: String,
        payload: MailboxPayload,
    ): Long = store.enqueueOutbox(
        NewOutboxEntry(
            accountId = accountId,
            kind = OutboxKind.MAILBOX,
            label = label,
            payload = outboxJson.encodeToString(payload),
            entityIds = payload.updates.keys.toList(),
            createdAt = now(),
        ),
    )

    /**
     * Queues a bug report (issues #417, #438). It is due the moment it
     * is written: a report has nothing to take back, so the next drain
     * posts it.
     */
    suspend fun enqueueBugReport(
        label: String,
        payload: BugReportPayload,
    ): Long = store.enqueueOutbox(
        NewOutboxEntry(
            accountId = payload.accountId,
            kind = OutboxKind.BUG_REPORT,
            label = label,
            payload = outboxJson.encodeToString(payload),
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
    ): Long = enqueueHeld(
        NewOutboxEntry(
            accountId = payload.accountId,
            kind = kind,
            label = label,
            payload = outboxJson.encodeToString(payload),
            createdAt = now(),
        ),
        holdUntilMs,
    )

    /** Writes [entry] and holds it back until [holdUntilMs] when there is one. */
    private suspend fun enqueueHeld(entry: NewOutboxEntry, holdUntilMs: Long): Long {
        val id = store.enqueueOutbox(entry)
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

    /** The compose an entry carries, or null when it is not one. */
    suspend fun composePayload(id: Long): ComposePayload? {
        val entry = store.outboxEntry(id) ?: return null
        if (entry.kind != OutboxKind.DRAFT && entry.kind != OutboxKind.SEND) return null
        return runCatching { outboxJson.decodeFromString<ComposePayload>(entry.payload) }.getOrNull()
    }

    /**
     * Marks a compose already being submitted as discarded, so the
     * drain takes the draft it writes off the server again rather than
     * leaving one the user threw away (issue #371). Returns false when
     * the entry is gone.
     */
    suspend fun markDiscardAfterWrite(id: Long): Boolean {
        val payload = composePayload(id) ?: return false
        updatePayload(id, payload.copy(discardAfterWrite = true))
        return true
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

    /** Records the message a compose entry wrote. */
    suspend fun noteWritten(entryId: Long, draftId: String) {
        writtenMutex.withLock {
            written[entryId] = draftId
            while (written.size > WRITTEN_KEPT) {
                written.remove(written.keys.first())
            }
        }
    }

    /** The message entry [entryId] wrote, as far as this process knows. */
    suspend fun writtenDraftId(entryId: Long): String? = writtenMutex.withLock { written[entryId] }

    /** Drops an entry outright, whatever its state; the outbox screen's discard. */
    suspend fun remove(id: Long) = store.deleteOutbox(id)

    /**
     * Puts a failed entry back in the queue for the next drain
     * (REQ-AND-SYNC-25). It goes out at once - a retry the user asked
     * for waits for nothing - and keeps its attempt count, which is what
     * says how often this entry has been tried.
     */
    suspend fun retry(id: Long) {
        val entry = store.outboxEntry(id) ?: return
        store.updateOutboxState(
            id = entry.id,
            state = OutboxState.QUEUED,
            attempts = entry.attempts,
            lastError = entry.lastError,
            permanent = false,
            nextAttemptAt = 0,
        )
    }

    /**
     * Puts every entry a drain will not pick up on its own back in the
     * queue: the ones the server refused, and the ones waiting out a
     * server that could not take them (issue #420).
     */
    suspend fun retryAll() {
        store.outboxList().filter { it.isStalled }.forEach { retry(it.id) }
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

/** How many written compose entries are remembered for a late discard. */
private const val WRITTEN_KEPT = 32

/** The membership snapshot an action's revert restores. */
fun Email.toSnapshot(): MembershipSnapshot = MembershipSnapshot(
    accountId = accountId,
    id = id,
    keywords = keywords.toList(),
    mailboxIds = mailboxIds.toList(),
    snoozedUntil = snoozedUntil,
)
