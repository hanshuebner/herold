package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.ChangesOutcome
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.outbox.DrainOutcome
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.coroutines.CancellationException
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
    const val MANAGED_RULE = "ManagedRule"

    val ALL = listOf(MAILBOX, EMAIL, THREAD, IDENTITY, MANAGED_RULE)
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
    private val outbox: Outbox = Outbox(store),
    /** The outbox's submitter; null in tests that only exercise reconciliation. */
    private val drainer: OutboxDrainer? = null,
    /** What the engine's own traffic says about reaching the server (issue #370). */
    private val reachability: Reachability = Reachability(),
    private val inboxFetchLimit: Int = DEFAULT_INBOX_FETCH,
    private val now: () -> Long = { 0L },
    /** What a pass did and how long it took; the app's diagnostic ring. */
    private val log: (String) -> Unit = {},
) {
    private val mutex = Mutex()

    /** The mailboxes an on-demand fill has already covered this run. */
    private val filledMailboxes = mutableSetOf<Pair<String, String>>()
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
        val startedAt = now()
        // The queue goes out before the fold: our own writes reach the
        // server first, so the changes we then read already contain them
        // rather than superseding them (REQ-AND-SYNC-24).
        drainOutbox()
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
            reachability.reached()
            _status.value = SyncStatus.Idle
        } catch (e: JmapException) {
            // The server answered, so the phone is not offline; the pass
            // failed for its own reason.
            reachability.reached()
            _status.value = SyncStatus.Failed(e.message ?: "sync failed", unauthorized = e.isUnauthorized)
        } catch (c: CancellationException) {
            // The pass was called off - the scope that asked for it is
            // gone. Nothing failed and nothing is running, and what the
            // last request met says nothing about reaching the server;
            // the cancellation goes on up.
            _status.value = SyncStatus.Idle
            throw c
        } catch (t: Throwable) {
            reachability.unreachable()
            _status.value = SyncStatus.Failed(t.message ?: "sync failed")
        }
        // What a pass that got to the end costs is the ring's answer to
        // "the refresh takes forever"; a pass that failed is reported by
        // the scheduler with its reason (issue #450).
        if (_status.value is SyncStatus.Idle) log("pass finished in ${now() - startedAt} ms")
        _status.value
    }

    /**
     * Submits what the outbox holds (REQ-AND-SYNC-22). The engine owns
     * the drain because it is the only component that talks to the
     * server on the store's behalf; the UI asks for one, it never
     * submits itself.
     */
    suspend fun drainOutbox(): DrainOutcome = drainer?.drain() ?: DrainOutcome()

    /** Reconciles one account, for an EventSource `StateChange` naming it. */
    suspend fun syncAccount(accountId: String, types: List<String> = SyncTypes.ALL): SyncStatus =
        mutex.withLock {
            _status.value = SyncStatus.Syncing
            try {
                syncAccountLocked(accountId, types)
                reachability.reached()
                _status.value = SyncStatus.Idle
            } catch (e: JmapException) {
                reachability.reached()
                _status.value = SyncStatus.Failed(e.message ?: "sync failed", unauthorized = e.isUnauthorized)
            } catch (c: CancellationException) {
                _status.value = SyncStatus.Idle
                throw c
            } catch (t: Throwable) {
                reachability.unreachable()
                _status.value = SyncStatus.Failed(t.message ?: "sync failed")
            }
            _status.value
        }

    private suspend fun syncAccountLocked(accountId: String, types: List<String> = SyncTypes.ALL) {
        // A fold that took long enough for the reader to notice names
        // itself, so a slow pass is read off the ring rather than
        // guessed at (issue #450).
        suspend fun fold(type: String, run: suspend () -> Unit) {
            if (!types.contains(type)) return
            val at = now()
            run()
            val took = now() - at
            if (took >= SLOW_FOLD_MS) log("folding $type took $took ms")
        }
        fold(SyncTypes.MAILBOX) { syncMailboxes(accountId) }
        fold(SyncTypes.EMAIL) { syncEmails(accountId) }
        fold(SyncTypes.THREAD) { syncThreads(accountId) }
        fold(SyncTypes.IDENTITY) { syncIdentities(accountId) }
        fold(SyncTypes.MANAGED_RULE) { syncManagedRules(accountId) }
    }

    /**
     * Whether a `Foo/changes` answer leaves the fold where it started.
     *
     * An answer that reports more changes to come and hands back the
     * state it was asked from carries the fold nowhere: asking again
     * puts the identical question and the pass never ends (issue #450).
     * A fold that has taken [MAX_CHANGE_ROUNDS] rounds is held to the
     * same rule, so no sequence of answers can keep a pass running.
     * Either way the type is refetched, which is what the client does
     * for any state it cannot fold from.
     */
    private fun ChangesOutcome.Changed.leadsNowhere(since: String, round: Int): Boolean =
        hasMoreChanges && (newState == since || round >= MAX_CHANGE_ROUNDS)

    private suspend fun syncMailboxes(accountId: String, round: Int = 0) {
        val since = store.syncState(accountId, SyncTypes.MAILBOX)
        if (since == null) {
            fillMailboxes(accountId)
            return
        }
        when (val outcome = api.mailboxChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate ->
                refetchMailboxes(accountId, since, "the server cannot calculate them")

            is ChangesOutcome.Changed -> {
                if (outcome.leadsNowhere(since, round)) {
                    refetchMailboxes(accountId, since, "the changes answer does not advance")
                    return
                }
                if (outcome.destroyed.isNotEmpty()) store.deleteMailboxes(accountId, outcome.destroyed)
                val touched = outcome.created + outcome.updated
                if (touched.isNotEmpty()) {
                    val fetched = api.mailboxGet(accountId, touched)
                    store.upsertMailboxes(fetched.list.map { it.toDomain(accountId) })
                }
                store.setSyncState(accountId, SyncTypes.MAILBOX, outcome.newState)
                if (outcome.hasMoreChanges) syncMailboxes(accountId, round + 1)
            }
        }
    }

    /** Drops the account's mailboxes and reads them again, saying why. */
    private suspend fun refetchMailboxes(accountId: String, since: String, why: String) {
        log("refetching mailboxes from state $since: $why")
        store.clearMailboxes(accountId)
        store.setSyncState(accountId, SyncTypes.MAILBOX, null)
        fillMailboxes(accountId)
    }

    private suspend fun fillMailboxes(accountId: String) {
        val result = api.mailboxGet(accountId, null)
        store.upsertMailboxes(result.list.map { it.toDomain(accountId) })
        if (result.state.isNotBlank()) store.setSyncState(accountId, SyncTypes.MAILBOX, result.state)
    }

    private suspend fun syncEmails(accountId: String, round: Int = 0) {
        val since = store.syncState(accountId, SyncTypes.EMAIL)
        if (since == null) {
            fillEmails(accountId)
            return
        }
        when (val outcome = api.emailChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate ->
                refetchEmails(accountId, since, "the server cannot calculate them")

            is ChangesOutcome.Changed -> {
                if (outcome.leadsNowhere(since, round)) {
                    refetchEmails(accountId, since, "the changes answer does not advance")
                    return
                }
                if (outcome.destroyed.isNotEmpty()) store.deleteEmails(accountId, outcome.destroyed)
                val touched = (outcome.created + outcome.updated).distinct()
                if (touched.isNotEmpty()) {
                    // Server truth for a message with a queued optimistic
                    // write on it wins; the queued write is discarded
                    // (REQ-AND-SYNC-24).
                    outbox.discardSupersededBy(accountId, touched)
                    val fetched = api.emailGet(accountId, touched)
                    store.upsertEmails(fetched.list.map { it.toDomain(accountId) })
                    if (fetched.notFound.isNotEmpty()) store.deleteEmails(accountId, fetched.notFound)
                }
                store.setSyncState(accountId, SyncTypes.EMAIL, outcome.newState)
                if (outcome.hasMoreChanges) syncEmails(accountId, round + 1)
            }
        }
    }

    /** Drops the account's mail and reads it again, saying why. */
    private suspend fun refetchEmails(accountId: String, since: String, why: String) {
        log("refetching mail from state $since: $why")
        store.clearEmails(accountId)
        store.setSyncState(accountId, SyncTypes.EMAIL, null)
        fillEmails(accountId)
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

    private suspend fun syncThreads(accountId: String, round: Int = 0) {
        val since = store.syncState(accountId, SyncTypes.THREAD) ?: return
        when (val outcome = api.threadChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate ->
                refetchThreads(accountId, since, "the server cannot calculate them")

            is ChangesOutcome.Changed -> {
                if (outcome.leadsNowhere(since, round)) {
                    refetchThreads(accountId, since, "the changes answer does not advance")
                    return
                }
                if (outcome.destroyed.isNotEmpty()) store.deleteThreads(accountId, outcome.destroyed)
                val touched = (outcome.created + outcome.updated).distinct()
                if (touched.isNotEmpty()) {
                    val fetched = api.threadGet(accountId, touched)
                    store.upsertThreads(fetched.list.map { it.toDomain(accountId) })
                }
                store.setSyncState(accountId, SyncTypes.THREAD, outcome.newState)
                if (outcome.hasMoreChanges) syncThreads(accountId, round + 1)
            }
        }
    }

    /** Drops the account's threads and reads them again, saying why. */
    private suspend fun refetchThreads(accountId: String, since: String, why: String) {
        log("refetching threads from state $since: $why")
        store.clearThreads(accountId)
        store.setSyncState(accountId, SyncTypes.THREAD, null)
        refetchThreadsOfKnownEmails(accountId)
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
     * The account's filter rules (suite REQ-FLT-20). They fold like every
     * other type, so the filters screen renders store rows and a rule
     * written in the suite reaches the phone through `ManagedRule/changes`
     * rather than a screen-level fetch.
     */
    private suspend fun syncManagedRules(accountId: String, round: Int = 0) {
        val since = store.syncState(accountId, SyncTypes.MANAGED_RULE)
        if (since == null) {
            fillManagedRules(accountId)
            return
        }
        when (val outcome = api.managedRuleChanges(accountId, since)) {
            is ChangesOutcome.CannotCalculate ->
                refetchManagedRules(accountId, since, "the server cannot calculate them")

            is ChangesOutcome.Changed -> {
                if (outcome.leadsNowhere(since, round)) {
                    refetchManagedRules(accountId, since, "the changes answer does not advance")
                    return
                }
                if (outcome.destroyed.isNotEmpty()) store.deleteManagedRules(accountId, outcome.destroyed)
                val touched = (outcome.created + outcome.updated).distinct()
                if (touched.isNotEmpty()) {
                    val fetched = api.managedRuleGet(accountId, touched)
                    store.upsertManagedRules(fetched.list.map { it.toDomain(accountId) })
                    if (fetched.notFound.isNotEmpty()) {
                        store.deleteManagedRules(accountId, fetched.notFound)
                    }
                }
                store.setSyncState(accountId, SyncTypes.MANAGED_RULE, outcome.newState)
                if (outcome.hasMoreChanges) syncManagedRules(accountId, round + 1)
            }
        }
    }

    /** Drops the account's rules and reads them again, saying why. */
    private suspend fun refetchManagedRules(accountId: String, since: String, why: String) {
        log("refetching rules from state $since: $why")
        store.clearManagedRules(accountId)
        store.setSyncState(accountId, SyncTypes.MANAGED_RULE, null)
        fillManagedRules(accountId)
    }

    /**
     * The whole rule set. A rule destroyed while the client was away is
     * gone from the fetch, so the account's rows are replaced rather than
     * merged.
     */
    private suspend fun fillManagedRules(accountId: String) {
        val result = runCatching { api.managedRuleGet(accountId, null) }.getOrNull() ?: return
        store.clearManagedRules(accountId)
        store.upsertManagedRules(result.list.map { it.toDomain(accountId) })
        if (result.state.isNotBlank()) store.setSyncState(accountId, SyncTypes.MANAGED_RULE, result.state)
    }

    /**
     * Completes a thread against the server: a search result or a
     * notification for a conversation the fill never covered (issue #339),
     * or a conversation the fill partly covered because one of its
     * messages sits in a mailbox the device does not sync (issue #461). A
     * thread the store already holds every member of costs no
     * `Email/get`, since the id set from `Thread/get` matches what is
     * already cached; only the members missing locally are fetched. The
     * store's own reconciliation is untouched: the rows land through the
     * same mapping a sync pass uses and the per-type state strings are
     * left alone, so the next `Email/changes` still asks for exactly what
     * it would have asked for.
     *
     * Returns true when the thread's messages are in the store afterwards.
     * A server that cannot be reached leaves the local cache as it stands,
     * so an already-complete thread still opens offline.
     */
    suspend fun ensureThread(accountId: String, threadId: String): Boolean {
        val thread = runCatching { api.threadGet(accountId, listOf(threadId)) }
            .getOrNull()?.list?.firstOrNull()
            ?: return store.threadEmailList(accountId, threadId).isNotEmpty()
        store.upsertThreads(listOf(thread.toDomain(accountId)))
        val ids = thread.emailIds.filter { it.isNotBlank() }
        if (ids.isEmpty()) return false
        val held = store.threadEmailList(accountId, threadId).map { it.id }.toSet()
        val missing = ids.filterNot { it in held }
        if (missing.isNotEmpty()) {
            val fetched = runCatching { api.emailGet(accountId, missing) }.getOrNull()
            if (fetched != null) store.upsertEmails(fetched.list.map { it.toDomain(accountId) })
        }
        return store.threadEmailList(accountId, threadId).isNotEmpty()
    }

    /**
     * Fills a mailbox the user opened - a label, Archive, Sent - the way
     * the first pass fills the inbox (REQ-AND-SYNC-02): its newest
     * messages and the rest of their threads. The initial fill covers the
     * inbox, so a drawer destination is filled the first time it is
     * opened (issue #374).
     *
     * The per-type state strings are left alone, so the next
     * `Email/changes` still asks for exactly what it would have asked for.
     * Returns true when the mailbox's messages are in the store.
     */
    suspend fun ensureMailbox(accountId: String, mailboxId: String): Boolean = mutex.withLock {
        if (!filledMailboxes.add(accountId to mailboxId)) return true
        val ids = runCatching { api.emailQueryInbox(accountId, mailboxId, inboxFetchLimit) }
            .getOrElse {
                filledMailboxes.remove(accountId to mailboxId)
                return false
            }
        if (ids.isEmpty()) return true
        val fetched = runCatching { api.emailGet(accountId, ids) }
            .getOrElse {
                filledMailboxes.remove(accountId to mailboxId)
                return false
            }
        store.upsertEmails(fetched.list.map { it.toDomain(accountId) })
        fillThreadsOf(accountId, fetched.list)
        return true
    }

    /**
     * The rest of each fetched message's thread, so an opened conversation
     * is complete offline.
     */
    private suspend fun fillThreadsOf(
        accountId: String,
        fetched: List<WireEmail>,
    ) {
        val threadIds = fetched.map { it.threadId }.filter { it.isNotBlank() }.distinct()
        if (threadIds.isEmpty()) return
        val threads = runCatching { api.threadGet(accountId, threadIds) }.getOrNull() ?: return
        store.upsertThreads(threads.list.map { it.toDomain(accountId) })
        val known = fetched.map { it.id }.toSet()
        val missing = threads.list.flatMap { it.emailIds }.filter { it !in known }.distinct()
        if (missing.isEmpty()) return
        val rest = runCatching { api.emailGet(accountId, missing) }.getOrNull() ?: return
        store.upsertEmails(rest.list.map { it.toDomain(accountId) })
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
        // A cache that cannot answer costs a download, never the screen
        // that asked (issue #420).
        runCatching { store.cachedBlob(accountId, blobId) }.getOrNull()?.let { return it.bytes }
        val downloaded = runCatching { api.downloadBlob(accountId, blobId, type, name) }.getOrNull()
            ?: return null
        runCatching { store.cacheBlob(accountId, blobId, downloaded.contentType, downloaded.bytes) }
        return downloaded.bytes
    }

    /**
     * The sender's picture, where herold hosts a principal for the
     * address and that principal has one (issue #348). It goes through
     * the same blob cache as an attachment, so a repeat notification from
     * the same sender costs no download.
     */
    suspend fun senderAvatar(accountId: String, address: String): ByteArray? {
        if (address.isBlank()) return null
        val blobId = runCatching { api.principalAvatarBlobId(accountId, address) }.getOrNull() ?: return null
        return blob(accountId, blobId, "image/*", "avatar")
    }

    private suspend fun inboxIdFor(accountId: String): String? =
        store.mailboxList().firstOrNull { it.accountId == accountId && it.role == MailboxRoles.INBOX }?.id

    companion object {
        const val DEFAULT_INBOX_FETCH = 100

        /**
         * How many `Foo/changes` rounds one type's fold takes in a pass
         * before the type is refetched instead (issue #450). A client
         * that is behind by a working day folds in a handful of rounds,
         * so the cap is met only by an answer sequence going nowhere.
         */
        const val MAX_CHANGE_ROUNDS = 64

        /** A fold slower than this names itself in the ring. */
        const val SLOW_FOLD_MS = 2_000L
    }
}
