package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.ChangesOutcome
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** What the shell shows about reconciliation (REQ-AND-SYNC-30). */
sealed interface SyncStatus {
    data object Idle : SyncStatus

    data object Syncing : SyncStatus

    /** The last pass failed; reads still serve from the local store. */
    data class Failed(val message: String, val unauthorized: Boolean = false) : SyncStatus
}

/** The JMAP types the engine reconciles, and the store keys their state strings use. */
object SyncTypes {
    const val MAILBOX = "Mailbox"
    const val EMAIL = "Email"
    const val THREAD = "Thread"
    const val IDENTITY = "Identity"

    val ALL = listOf(MAILBOX, EMAIL, THREAD, IDENTITY)
}

/**
 * The sole reconciler between the local store and herold
 * (docs/design/android/architecture/03-sync-and-state.md). It walks every
 * account the session descriptor advertises (primary plus sub-accounts),
 * fills an account that has no persisted state, and otherwise asks
 * `Foo/changes` for the delta since the stored state string
 * (REQ-AND-SYNC-10). A `cannotCalculateChanges` drops that type's rows for
 * that account and refetches.
 *
 * It takes [JmapApi] and [LocalStore] rather than concrete classes so the
 * reconciliation logic runs in `commonTest` against canned responses and an
 * in-memory store, with no network and no SQLite driver.
 */
class SyncEngine(
    private val api: JmapApi,
    private val store: LocalStore,
    private val inboxFetchLimit: Int = DEFAULT_INBOX_FETCH,
    private val now: () -> Long = { 0L },
) {
    private val mutex = Mutex()
    private val _status = MutableStateFlow<SyncStatus>(SyncStatus.Idle)
    val status: StateFlow<SyncStatus> = _status.asStateFlow()

    /** Category names the account's classifier derived, refreshed with each full pass. */
    private val _categories = MutableStateFlow<List<String>>(emptyList())
    val categories: StateFlow<List<String>> = _categories.asStateFlow()

    /**
     * Reads the session descriptor, records its accounts, and reconciles all
     * of them. Safe to call repeatedly; one pass runs at a time.
     */
    suspend fun syncAll(): SyncStatus = mutex.withLock {
        _status.value = SyncStatus.Syncing
        try {
            val session = api.session()
            val accountIds = session.mailAccountIds()
            store.replaceAccounts(
                accountIds.mapIndexed { index, id ->
                    Account(
                        id = id,
                        name = session.accounts[id]?.name ?: session.username,
                        isPrimary = id == session.mailAccountId,
                        isPersonal = session.accounts[id]?.isPersonal ?: true,
                        sortOrder = index,
                    )
                },
            )
            accountIds.forEach { syncAccountLocked(it) }
            val primary = session.mailAccountId
            if (primary != null) {
                _categories.value = runCatching { api.derivedCategories(primary) }.getOrDefault(emptyList())
            }
            _status.value = SyncStatus.Idle
        } catch (e: JmapException) {
            _status.value = SyncStatus.Failed(e.message ?: "sync failed", unauthorized = e.isUnauthorized)
        } catch (t: Throwable) {
            _status.value = SyncStatus.Failed(t.message ?: "sync failed")
        }
        _status.value
    }

    /** Reconciles one account, for an EventSource `StateChange` naming it. */
    suspend fun syncAccount(accountId: String, types: List<String> = SyncTypes.ALL): SyncStatus =
        mutex.withLock {
            _status.value = SyncStatus.Syncing
            try {
                syncAccountLocked(accountId, types)
                _status.value = SyncStatus.Idle
            } catch (e: JmapException) {
                _status.value = SyncStatus.Failed(e.message ?: "sync failed", unauthorized = e.isUnauthorized)
            } catch (t: Throwable) {
                _status.value = SyncStatus.Failed(t.message ?: "sync failed")
            }
            _status.value
        }

    private suspend fun syncAccountLocked(accountId: String, types: List<String> = SyncTypes.ALL) {
        if (types.contains(SyncTypes.MAILBOX)) syncMailboxes(accountId)
        if (types.contains(SyncTypes.EMAIL)) syncEmails(accountId)
        if (types.contains(SyncTypes.THREAD)) syncThreads(accountId)
        if (types.contains(SyncTypes.IDENTITY)) syncIdentities(accountId)
    }

    private suspend fun syncMailboxes(accountId: String) {
        val since = store.syncState(accountId, SyncTypes.MAILBOX)
        if (since == null) {
            fillMailboxes(accountId)
            return
        }
        when (val outcome = api.mailboxChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate -> {
                store.clearMailboxes(accountId)
                store.setSyncState(accountId, SyncTypes.MAILBOX, null)
                fillMailboxes(accountId)
            }

            is ChangesOutcome.Changed -> {
                if (outcome.destroyed.isNotEmpty()) store.deleteMailboxes(accountId, outcome.destroyed)
                val touched = outcome.created + outcome.updated
                if (touched.isNotEmpty()) {
                    val fetched = api.mailboxGet(accountId, touched)
                    store.upsertMailboxes(fetched.list.map { it.toDomain(accountId) })
                }
                store.setSyncState(accountId, SyncTypes.MAILBOX, outcome.newState)
                if (outcome.hasMoreChanges) syncMailboxes(accountId)
            }
        }
    }

    private suspend fun fillMailboxes(accountId: String) {
        val result = api.mailboxGet(accountId, null)
        store.upsertMailboxes(result.list.map { it.toDomain(accountId) })
        if (result.state.isNotBlank()) store.setSyncState(accountId, SyncTypes.MAILBOX, result.state)
    }

    private suspend fun syncEmails(accountId: String) {
        val since = store.syncState(accountId, SyncTypes.EMAIL)
        if (since == null) {
            fillEmails(accountId)
            return
        }
        when (val outcome = api.emailChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate -> {
                store.clearEmails(accountId)
                store.setSyncState(accountId, SyncTypes.EMAIL, null)
                fillEmails(accountId)
            }

            is ChangesOutcome.Changed -> {
                if (outcome.destroyed.isNotEmpty()) store.deleteEmails(accountId, outcome.destroyed)
                val touched = (outcome.created + outcome.updated).distinct()
                if (touched.isNotEmpty()) {
                    val fetched = api.emailGet(accountId, touched)
                    store.upsertEmails(fetched.list.map { it.toDomain(accountId) })
                    if (fetched.notFound.isNotEmpty()) store.deleteEmails(accountId, fetched.notFound)
                }
                store.setSyncState(accountId, SyncTypes.EMAIL, outcome.newState)
                if (outcome.hasMoreChanges) syncEmails(accountId)
            }
        }
    }

    /**
     * First fill for an account: the inbox's most recent messages, then the
     * remaining messages of each thread they belong to, so an opened thread
     * is complete offline (REQ-AND-SYNC-02).
     */
    private suspend fun fillEmails(accountId: String) {
        val inboxId = inboxIdFor(accountId) ?: return
        val ids = api.emailQueryInbox(accountId, inboxId, inboxFetchLimit)
        val fetched = api.emailGet(accountId, ids)
        store.upsertEmails(fetched.list.map { it.toDomain(accountId) })

        val threadIds = fetched.list.map { it.threadId }.filter { it.isNotBlank() }.distinct()
        if (threadIds.isNotEmpty()) {
            val threads = api.threadGet(accountId, threadIds)
            store.upsertThreads(threads.list.map { it.toDomain(accountId) })
            if (threads.state.isNotBlank()) store.setSyncState(accountId, SyncTypes.THREAD, threads.state)

            val known = fetched.list.map { it.id }.toSet()
            val missing = threads.list.flatMap { it.emailIds }.filter { it !in known }.distinct()
            if (missing.isNotEmpty()) {
                val rest = api.emailGet(accountId, missing)
                store.upsertEmails(rest.list.map { it.toDomain(accountId) })
            }
        }
        if (fetched.state.isNotBlank()) store.setSyncState(accountId, SyncTypes.EMAIL, fetched.state)
    }

    private suspend fun syncThreads(accountId: String) {
        val since = store.syncState(accountId, SyncTypes.THREAD) ?: return
        when (val outcome = api.threadChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate -> {
                store.clearThreads(accountId)
                store.setSyncState(accountId, SyncTypes.THREAD, null)
                refetchThreadsOfKnownEmails(accountId)
            }

            is ChangesOutcome.Changed -> {
                if (outcome.destroyed.isNotEmpty()) store.deleteThreads(accountId, outcome.destroyed)
                val touched = (outcome.created + outcome.updated).distinct()
                if (touched.isNotEmpty()) {
                    val fetched = api.threadGet(accountId, touched)
                    store.upsertThreads(fetched.list.map { it.toDomain(accountId) })
                }
                store.setSyncState(accountId, SyncTypes.THREAD, outcome.newState)
                if (outcome.hasMoreChanges) syncThreads(accountId)
            }
        }
    }

    private suspend fun refetchThreadsOfKnownEmails(accountId: String) {
        val threadIds = store.emailList().filter { it.accountId == accountId }
            .map { it.threadId }.distinct()
        if (threadIds.isEmpty()) return
        val fetched = api.threadGet(accountId, threadIds)
        store.upsertThreads(fetched.list.map { it.toDomain(accountId) })
        if (fetched.state.isNotBlank()) store.setSyncState(accountId, SyncTypes.THREAD, fetched.state)
    }

    private suspend fun syncIdentities(accountId: String) {
        val since = store.syncState(accountId, SyncTypes.IDENTITY)
        if (since == null) {
            fillIdentities(accountId)
            return
        }
        when (val outcome = api.identityChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate -> {
                store.clearIdentities(accountId)
                store.setSyncState(accountId, SyncTypes.IDENTITY, null)
                fillIdentities(accountId)
            }

            is ChangesOutcome.Changed -> {
                if (outcome.destroyed.isNotEmpty()) store.deleteIdentities(accountId, outcome.destroyed)
                if (outcome.created.isNotEmpty() || outcome.updated.isNotEmpty()) fillIdentities(accountId)
                store.setSyncState(accountId, SyncTypes.IDENTITY, outcome.newState)
            }
        }
    }

    private suspend fun fillIdentities(accountId: String) {
        val result = runCatching { api.identityGet(accountId) }.getOrNull() ?: return
        store.upsertIdentities(result.list.map { it.toDomain(accountId) })
        if (result.state.isNotBlank()) store.setSyncState(accountId, SyncTypes.IDENTITY, result.state)
    }

    /**
     * Brings a thread the store does not hold into it - a search result
     * outside the synced set, or a notification for a conversation the
     * fill never covered (issue #339). The store's own reconciliation is
     * untouched: the rows land through the same mapping a sync pass uses
     * and the per-type state strings are left alone, so the next
     * `Email/changes` still asks for exactly what it would have asked for.
     *
     * Returns true when the thread's messages are in the store afterwards.
     */
    suspend fun ensureThread(accountId: String, threadId: String): Boolean {
        if (store.threadEmailList(accountId, threadId).isNotEmpty()) return true
        val thread = runCatching { api.threadGet(accountId, listOf(threadId)) }
            .getOrNull()?.list?.firstOrNull() ?: return false
        store.upsertThreads(listOf(thread.toDomain(accountId)))
        val ids = thread.emailIds.filter { it.isNotBlank() }
        if (ids.isEmpty()) return false
        val fetched = runCatching { api.emailGet(accountId, ids) }.getOrNull() ?: return false
        store.upsertEmails(fetched.list.map { it.toDomain(accountId) })
        return store.threadEmailList(accountId, threadId).isNotEmpty()
    }

    /**
     * Fetches and caches a message body on open. Returns the stored message
     * with its body; with no connectivity it returns whatever the cache
     * holds, so a previously read thread still opens and an unread one
     * renders the not-downloaded placeholder (REQ-AND-SYNC-03/12).
     */
    suspend fun loadBody(accountId: String, emailId: String): com.netzhansa.herold.shared.domain.Email? {
        val cached = store.email(accountId, emailId)
        if (cached?.bodyHtml != null || cached?.bodyText != null) return cached
        val fetched = runCatching { api.emailGet(accountId, listOf(emailId), withBody = true) }
            .getOrNull()?.list?.firstOrNull()
            ?: return cached
        val mapped = fetched.toDomain(accountId)
        store.upsertEmails(listOf(mapped))
        store.storeBody(
            accountId = accountId,
            id = emailId,
            html = mapped.bodyHtml,
            text = mapped.bodyText,
            attachments = mapped.attachments,
            fetchedAt = now(),
        )
        return store.email(accountId, emailId)
    }

    /**
     * Downloads a blob through the cache, so a re-opened thread renders
     * offline. Returns null when the blob is neither cached nor reachable.
     */
    suspend fun blob(accountId: String, blobId: String, type: String, name: String): ByteArray? {
        store.cachedBlob(accountId, blobId)?.let { return it.bytes }
        val downloaded = runCatching { api.downloadBlob(accountId, blobId, type, name) }.getOrNull()
            ?: return null
        store.cacheBlob(accountId, blobId, downloaded.contentType, downloaded.bytes)
        return downloaded.bytes
    }

    private suspend fun inboxIdFor(accountId: String): String? =
        store.mailboxList().firstOrNull { it.accountId == accountId && it.role == MailboxRoles.INBOX }?.id

    companion object {
        const val DEFAULT_INBOX_FETCH = 100
    }
}
