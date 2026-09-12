package com.netzhansa.herold.shared.store

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.Thread
import com.netzhansa.herold.shared.outbox.NewOutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxState
import kotlinx.coroutines.flow.Flow

/** A blob held in the local cache (REQ-AND-SYNC-12). */
data class CachedBlob(
    val contentType: String,
    val bytes: ByteArray,
) {
    override fun equals(other: Any?): Boolean =
        other is CachedBlob && contentType == other.contentType && bytes.contentEquals(other.bytes)

    override fun hashCode(): Int = 31 * contentType.hashCode() + bytes.contentHashCode()
}

/**
 * The push subscription this install holds with herold
 * (REQ-AND-PUSH-01/04). [tokenFingerprint] is a stable hash of what
 * identifies the registration - the FCM token or the distributor endpoint
 * - so a rotation is detected without either being written anywhere.
 * [transport] is the wire `kind` it was registered under, so switching
 * transports registers anew even when the identity happens to match.
 */
data class PushRegistration(
    val subscriptionId: String,
    val deviceClientId: String,
    val tokenFingerprint: String,
    val registeredAt: Long,
    val transport: String = "fcm",
)

/**
 * The local source of truth (REQ-AND-SYNC-01). The UI reads it and nothing
 * else; the sync engine is the only writer of server-derived rows, and
 * optimistic actions write through it before their `Email/set` is sent.
 *
 * It is an interface so screen logic and the reconciler are testable against
 * an in-memory implementation without a SQLite driver or a network
 * (docs/design/android/architecture/05-ui-shell.md, "Testing hook").
 */
interface LocalStore {
    fun accounts(): Flow<List<Account>>

    suspend fun accountList(): List<Account>

    suspend fun replaceAccounts(accounts: List<Account>)

    fun mailboxes(): Flow<List<Mailbox>>

    suspend fun mailboxList(): List<Mailbox>

    suspend fun upsertMailboxes(rows: List<Mailbox>)

    suspend fun deleteMailboxes(accountId: String, ids: List<String>)

    suspend fun clearMailboxes(accountId: String)

    /** Every inbox message across accounts, newest first (suite REQ-MAIL-SUB-03). */
    fun inboxEmails(limit: Long = DEFAULT_INBOX_LIMIT): Flow<List<Email>>

    /** The snoozed messages, next to wake first (suite REQ-SNZ-14). */
    fun snoozedEmails(limit: Long = DEFAULT_INBOX_LIMIT): Flow<List<Email>>

    fun threadEmails(accountId: String, threadId: String): Flow<List<Email>>

    /**
     * The thread's messages as they stand, for a caller that must know
     * whether the store holds the thread at all before rendering it
     * (issue #339).
     */
    suspend fun threadEmailList(accountId: String, threadId: String): List<Email>

    suspend fun email(accountId: String, id: String): Email?

    suspend fun emailList(): List<Email>

    /**
     * Offline search over the synced set: sender, subject and the cached
     * preview matched case-insensitively, newest first (REQ-AND-SYNC-13).
     */
    suspend fun searchCached(query: String, limit: Long = DEFAULT_SEARCH_LIMIT): List<Email>

    suspend fun upsertEmails(rows: List<Email>)

    suspend fun deleteEmails(accountId: String, ids: List<String>)

    suspend fun clearEmails(accountId: String)

    /**
     * Writes the membership an optimistic action produced (star, read,
     * archive, label, snooze) without touching the cached body.
     */
    suspend fun updateMembership(
        accountId: String,
        id: String,
        keywords: Set<String>,
        mailboxIds: Set<String>,
        snoozedUntil: String?,
    )

    suspend fun storeBody(
        accountId: String,
        id: String,
        html: String?,
        text: String?,
        attachments: List<Attachment>,
        fetchedAt: Long,
    )

    suspend fun thread(accountId: String, id: String): Thread?

    suspend fun upsertThreads(rows: List<Thread>)

    suspend fun deleteThreads(accountId: String, ids: List<String>)

    suspend fun clearThreads(accountId: String)

    fun identities(): Flow<List<Identity>>

    suspend fun upsertIdentities(rows: List<Identity>)

    suspend fun deleteIdentities(accountId: String, ids: List<String>)

    suspend fun clearIdentities(accountId: String)

    suspend fun syncState(accountId: String, type: String): String?

    suspend fun setSyncState(accountId: String, type: String, state: String?)

    suspend fun cachedBlob(accountId: String, blobId: String): CachedBlob?

    suspend fun cacheBlob(accountId: String, blobId: String, contentType: String, bytes: ByteArray)

    /**
     * Every outbox entry, oldest first: the order the drain submits them
     * in (REQ-AND-SYNC-22). Outbox rows are never evicted by cache
     * pressure; only a completed or discarded drain deletes one.
     */
    fun outbox(): Flow<List<OutboxEntry>>

    suspend fun outboxList(): List<OutboxEntry>

    suspend fun outboxEntry(id: Long): OutboxEntry?

    /** Appends an entry and returns its id. */
    suspend fun enqueueOutbox(entry: NewOutboxEntry): Long

    suspend fun updateOutboxState(
        id: Long,
        state: OutboxState,
        attempts: Int,
        lastError: String?,
        permanent: Boolean,
        nextAttemptAt: Long,
    )

    /** Records the progress a multi-step entry made (uploaded blob, created draft). */
    suspend fun updateOutboxPayload(id: Long, payload: String)

    suspend fun deleteOutbox(id: Long)

    suspend fun pushRegistration(): PushRegistration?

    /** Records the subscription herold created, or clears it with null. */
    suspend fun setPushRegistration(registration: PushRegistration?)

    suspend fun blobCacheSize(): Long

    /** Drops every row of every account; used on sign-out (REQ-AND-AUTH-21). */
    suspend fun clearAll()

    companion object {
        const val DEFAULT_INBOX_LIMIT: Long = 500
        const val DEFAULT_SEARCH_LIMIT: Long = 100
    }
}
