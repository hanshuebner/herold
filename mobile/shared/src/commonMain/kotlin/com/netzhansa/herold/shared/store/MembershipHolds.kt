package com.netzhansa.herold.shared.store

import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/**
 * The rows an optimistic action changed, held against server state until
 * the outbox entry carrying that action has drained (issue #473).
 *
 * `MailActions` writes a message's intended membership and keywords into
 * the local store the moment the user acts, then queues the `Email/set`
 * behind it. A response already on its way when the action happened - an
 * `Email/get` a screen asked for, or a sync pass that started earlier -
 * was built from the pre-action state, and `upsertEmails` writing it
 * straight over the row undoes the action until the next pass corrects
 * it again: a swiped-away row flickers back before it settles.
 *
 * Holding the acted-on ids here keeps `upsertEmails` from writing their
 * `mailboxIds`, `keywords` and `snoozedUntil` back over what the action
 * set. The whole of those three is held, not only the fields the action
 * itself patched: telling an action's own field apart from an unrelated
 * one a concurrent client changed on the same row would need per-field
 * provenance the store does not keep. Everything else a response carries
 * - a subject, a preview, an updated body - still lands.
 *
 * A row can carry more than one action at once, so [mark] counts rather
 * than replaces: two overlapping actions on the same id are only fully
 * released once both have drained. `OutboxDrainer` calls [forget] once
 * the entry that placed a hold has drained, whichever way - sent,
 * refused, or given up on. A hold nothing releases - the app killed
 * mid-drain, say - expires after [ttlMs] on its own, the same bound a
 * [Tombstones] entry carries, so a change that never drains cannot pin a
 * row's membership away forever.
 */
class MembershipHolds(
    private val now: () -> Long = { 0L },
    private val ttlMs: Long = Tombstones.DEFAULT_TTL_MS,
) {
    private data class Hold(val count: Int, val until: Long)

    private val mutex = Mutex()
    private val held = mutableMapOf<String, Hold>()

    /** Holds [ids] of [accountId]'s membership and keywords against incoming writes. */
    suspend fun mark(accountId: String, ids: Collection<String>) {
        if (ids.isEmpty()) return
        val until = now() + ttlMs
        mutex.withLock {
            prune()
            ids.forEach { id ->
                val key = key(accountId, id)
                val existing = held[key]
                held[key] = Hold(count = (existing?.count ?: 0) + 1, until = until)
            }
        }
    }

    /** Releases one hold [mark] placed on [ids]; the entry that placed it has drained. */
    suspend fun forget(accountId: String, ids: Collection<String>) {
        if (ids.isEmpty()) return
        mutex.withLock {
            ids.forEach { id ->
                val key = key(accountId, id)
                val existing = held[key] ?: return@forEach
                if (existing.count <= 1) held.remove(key) else held[key] = existing.copy(count = existing.count - 1)
            }
        }
    }

    /** Whatever of [ids] is still held, so a caller can freeze those rows' membership. */
    suspend fun heldOf(accountId: String, ids: Collection<String>): Set<String> {
        if (ids.isEmpty()) return emptySet()
        return mutex.withLock {
            prune()
            ids.filterTo(mutableSetOf()) { held.containsKey(key(accountId, it)) }
        }
    }

    /** Forgets everything; the sign-out that drops the account's rows. */
    suspend fun clear() {
        mutex.withLock { held.clear() }
    }

    private fun prune() {
        val at = now()
        held.entries.removeAll { it.value.until <= at }
    }

    private fun key(accountId: String, id: String): String = "$accountId $id"
}
