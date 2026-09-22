package com.netzhansa.herold.shared.outbox

import com.netzhansa.herold.shared.actions.CategoryActions
import com.netzhansa.herold.shared.actions.MailActions
import com.netzhansa.herold.shared.compose.AttachmentStatus
import com.netzhansa.herold.shared.compose.ComposeAttachment
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.compose.ComposeState
import com.netzhansa.herold.shared.compose.Composer
import com.netzhansa.herold.shared.compose.ReplyContext
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.jmap.BugReportApi
import com.netzhansa.herold.shared.jmap.BugReportPart
import com.netzhansa.herold.shared.jmap.Envelope
import com.netzhansa.herold.shared.jmap.FailureKind
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.classifyFailure
import com.netzhansa.herold.shared.sync.Reachability
import com.netzhansa.herold.shared.store.LocalStore
import com.netzhansa.herold.shared.store.Tombstones
import com.netzhansa.herold.shared.sync.toRuleRow
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** What one entry's submission did. */
private sealed interface StepResult {
    data object Done : StepResult

    /** The server refused; the entry stays for the user to see (REQ-AND-SYNC-23). */
    data class Rejected(val message: String) : StepResult

    /** The attempt could not be completed; it is worth trying again. */
    data class Retry(val message: String) : StepResult

    /**
     * The server does not understand the request as sent. A server
     * behind the build that queued the entry answers this way, so the
     * entry waits for the server to catch up rather than being given
     * up on (issue #420).
     */
    data class Deferred(val message: String) : StepResult

    /**
     * Nothing reached the server. The entry stays queued exactly as it
     * was and the failure goes to the log, not to the user (issue #370).
     */
    data class Offline(val message: String) : StepResult
}

/** An entry the server refused, for the snackbar the shell raises. */
data class OutboxFailure(
    val entryId: Long,
    val kind: OutboxKind,
    val label: String,
    val message: String,
)

/** What a drain pass did, for a background worker's result. */
data class DrainOutcome(
    val submitted: Int = 0,
    val rejected: Int = 0,
    val retryable: Int = 0,
    /** Entries the pass left queued because the server could not be reached. */
    val offline: Int = 0,
    /**
     * Entries left waiting for a server that can take them (issue #420).
     * They are pending, and their next attempt is hours out.
     */
    val deferred: Int = 0,
    val pending: Int = 0,
) {
    /**
     * True when entries remain that a later pass should pick up soon. An
     * entry waiting on a server upgrade is not due for hours, so it does
     * not keep a background job coming back.
     */
    val hasMore: Boolean get() = pending - deferred > 0
}

/**
 * The only thing that submits outbox entries (architecture
 * `03-sync-and-state.md` § Outbox and optimistic reconciliation). It walks
 * each account's entries in the order they were queued, submits them, and
 * on success takes the server's version of the affected messages into the
 * local store.
 *
 * A transient failure (no connection, a 5xx, a timeout) leaves the entry
 * queued with a backoff and stops that account's pass, so the queue keeps
 * its order. A refusal the server actually answered with reverts the
 * optimistic rows, marks the entry failed with the reason, and leaves it
 * listed for a manual retry (REQ-AND-SYNC-23/25).
 *
 * A server that does not understand the request as sent - the answer a
 * server older than this build gives a request the client has grown - is
 * neither of those: the entry is deferred and offered again on a widening
 * schedule until the server can take it, so a report queued against
 * yesterday's server still arrives (REQ-AND-SYNC-27, issue #420).
 */
class OutboxDrainer(
    private val api: JmapApi,
    private val store: LocalStore,
    private val outbox: Outbox,
    private val spool: BlobSpool,
    private val composer: Composer = Composer(api),
    /**
     * The bug-reports REST surface of the same server (issue #417). The
     * JMAP client carries it, so a drain built on one has it; a drain
     * built on a transport without it refuses a queued report rather
     * than holding it forever.
     */
    private val bugReports: BugReportApi? = api as? BugReportApi,
    /** What a local delete holds away from a fetch in flight (issue #371). */
    private val tombstones: Tombstones = Tombstones(),
    private val reachability: Reachability = Reachability(),
    /** Where transport failures go: the developer log, never the screen. */
    private val log: (String) -> Unit = {},
    private val now: () -> Long = { 0L },
    private val maxAttempts: Int = DEFAULT_MAX_ATTEMPTS,
    private val backoffBaseMs: Long = DEFAULT_BACKOFF_BASE_MS,
    private val backoffCapMs: Long = DEFAULT_BACKOFF_CAP_MS,
    /**
     * How often an entry the server does not understand is offered
     * again before the drain gives up on it (issue #420). The schedule
     * below spends these over about a week, which covers a client that
     * has outrun the server it talks to; a request no server will ever
     * take stops there rather than being offered forever.
     */
    private val maxDeferredAttempts: Int = DEFAULT_MAX_DEFERRED_ATTEMPTS,
    private val deferredBaseMs: Long = DEFAULT_DEFERRED_BASE_MS,
    private val deferredCapMs: Long = DEFAULT_DEFERRED_CAP_MS,
) {
    private val mutex = Mutex()

    private val _failures = MutableSharedFlow<OutboxFailure>(extraBufferCapacity = 8)

    /** Refusals the shell reports to the user as they happen. */
    val failures: SharedFlow<OutboxFailure> = _failures.asSharedFlow()

    /**
     * Submits what is due. One pass runs at a time, so a connectivity
     * change and a foreground sync arriving together do not send an entry
     * twice.
     */
    suspend fun drain(): DrainOutcome = mutex.withLock {
        var submitted = 0
        var rejected = 0
        var retryable = 0
        var offline = 0
        val byAccount = outbox.list().filter { it.isPending }.groupBy { it.accountId }
        for ((_, entries) in byAccount) {
            for (entry in entries.sortedBy { it.id }) {
                val due = entry.nextAttemptAt <= now()
                if (!due) {
                    // A backoff after a transient failure holds the
                    // account's order until that entry drains. A send
                    // held for its undo window and an entry waiting for
                    // a server that can take it both let the entries
                    // behind them through - the first because its wait
                    // is the user's, the second because its wait is the
                    // server's and may be days long.
                    val holdsTheQueue = entry.attempts > 0 && entry.state != OutboxState.DEFERRED
                    if (holdsTheQueue) break else continue
                }
                store.updateOutboxState(
                    id = entry.id,
                    state = OutboxState.SENDING,
                    attempts = entry.attempts,
                    lastError = entry.lastError,
                    permanent = false,
                    nextAttemptAt = entry.nextAttemptAt,
                )
                when (val result = submit(entry)) {
                    is StepResult.Done -> {
                        reachability.reached()
                        discard(entry)
                        submitted++
                    }

                    is StepResult.Rejected -> {
                        reachability.reached()
                        revert(entry)
                        store.updateOutboxState(
                            id = entry.id,
                            state = OutboxState.FAILED,
                            attempts = entry.attempts + 1,
                            lastError = result.message,
                            permanent = true,
                            nextAttemptAt = 0,
                        )
                        _failures.tryEmit(OutboxFailure(entry.id, entry.kind, entry.label, result.message))
                        rejected++
                    }

                    is StepResult.Deferred -> {
                        reachability.reached()
                        val attempts = entry.attempts + 1
                        if (attempts >= maxDeferredAttempts) {
                            // The waiting is spent. The entry is not
                            // thrown away - it is what the user wrote,
                            // and a queued bug report is the evidence
                            // for its own defect - so it stays listed
                            // with what became of it, and the user
                            // hears about it (issue #420).
                            revert(entry)
                            val message = gaveUpMessage(result.message, attempts)
                            store.updateOutboxState(
                                id = entry.id,
                                state = OutboxState.FAILED,
                                attempts = attempts,
                                lastError = message,
                                permanent = true,
                                nextAttemptAt = 0,
                            )
                            log("outbox: \"${entry.label}\" is given up on after $attempts attempts (${result.message})")
                            _failures.tryEmit(OutboxFailure(entry.id, entry.kind, entry.label, message))
                            rejected++
                        } else {
                            val nextAt = now() + deferredBackoff(attempts)
                            store.updateOutboxState(
                                id = entry.id,
                                state = OutboxState.DEFERRED,
                                attempts = attempts,
                                lastError = result.message,
                                permanent = false,
                                nextAttemptAt = nextAt,
                            )
                            log(
                                "outbox: \"${entry.label}\" waits for a server that can take it " +
                                    "(attempt $attempts of $maxDeferredAttempts: ${result.message})",
                            )
                        }
                    }

                    is StepResult.Retry -> {
                        reachability.reached()
                        val attempts = entry.attempts + 1
                        val exhausted = attempts >= maxAttempts
                        store.updateOutboxState(
                            id = entry.id,
                            state = if (exhausted) OutboxState.FAILED else OutboxState.QUEUED,
                            attempts = attempts,
                            lastError = result.message,
                            permanent = false,
                            nextAttemptAt = if (exhausted) 0 else now() + backoff(attempts),
                        )
                        // An entry that has spent its attempts is as
                        // done for as a refused one: the user hears
                        // about it rather than the queue holding it
                        // silently (issue #371).
                        if (exhausted) {
                            _failures.tryEmit(
                                OutboxFailure(entry.id, entry.kind, entry.label, result.message),
                            )
                        }
                        retryable++
                        break
                    }

                    is StepResult.Offline -> {
                        // Being offline is not an attempt the entry
                        // spends, and it is not an error on it: the entry
                        // goes back exactly as it was and leaves with the
                        // next drain a connection triggers (issue #370).
                        reachability.unreachable()
                        log("outbox: \"${entry.label}\" waits for a connection (${result.message})")
                        store.updateOutboxState(
                            id = entry.id,
                            state = OutboxState.QUEUED,
                            attempts = entry.attempts,
                            lastError = entry.lastError,
                            permanent = false,
                            nextAttemptAt = entry.nextAttemptAt,
                        )
                        offline++
                        break
                    }
                }
            }
        }
        val left = outbox.list().filter { it.isPending }
        DrainOutcome(
            submitted = submitted,
            rejected = rejected,
            retryable = retryable,
            offline = offline,
            deferred = left.count { it.state == OutboxState.DEFERRED },
            pending = left.size,
        )
    }

    private suspend fun submit(entry: OutboxEntry): StepResult = when (entry.kind) {
        OutboxKind.ACTION -> submitAction(entry)
        OutboxKind.DESTROY -> submitDestroy(entry)
        OutboxKind.DRAFT -> submitCompose(entry, submitToQueue = false)
        OutboxKind.SEND -> submitCompose(entry, submitToQueue = true)
        OutboxKind.RULE -> submitRule(entry)
        OutboxKind.MAILBOX -> submitMailbox(entry)
        OutboxKind.BUG_REPORT -> submitBugReport(entry)
    }

    /**
     * A bug report (issue #417): the spooled bundle goes up as one
     * multipart `POST /api/v1/bug-reports` on the account's bearer
     * token. The server keeps it for `herold bug-fetch`; nothing of it
     * lands in the user's mailboxes, so there is no local row to take
     * back and the entry is simply done.
     */
    private suspend fun submitBugReport(entry: OutboxEntry): StepResult {
        val payload = runCatching {
            outboxJson.decodeFromString<BugReportPayload>(entry.payload)
        }.getOrNull() ?: return StepResult.Rejected("the queued report could not be read")
        val client = bugReports
            ?: return StepResult.Rejected("this server does not take bug reports")
        val parts = payload.parts.map { part ->
            val bytes = spool.read(part.spool)
                ?: return StepResult.Rejected("the report's ${part.name} is no longer on the device")
            BugReportPart(part.name, part.type, bytes)
        }
        if (parts.isEmpty()) return StepResult.Rejected("the report carries nothing")
        val id = try {
            client.postBugReport(parts)
        } catch (t: Throwable) {
            return failureOf(t)
        }
        log("outbox: the report \"${payload.title}\" is on the server as ${id.ifBlank { "an unnamed report" }}")
        return StepResult.Done
    }

    /**
     * A label write (issue #399). The server owns the ranked set - it
     * renumbers every other ranked label around the one that moved - so
     * the account's labels are read back on both outcomes: after a write
     * that took, to pick up the renumbering, and after a refusal, to put
     * the settings screen back on the server's truth.
     */
    private suspend fun submitMailbox(entry: OutboxEntry): StepResult {
        val payload = runCatching {
            outboxJson.decodeFromString<MailboxPayload>(entry.payload)
        }.getOrNull() ?: return StepResult.Rejected("the queued label change could not be read")
        if (payload.updates.isEmpty() && payload.creates.isEmpty()) return StepResult.Done
        val outcome = try {
            api.mailboxSet(
                payload.accountId,
                create = payload.creates,
                update = payload.updates,
            )
        } catch (t: Throwable) {
            return failureOf(t)
        }
        // The placeholder row stood in for the label until the server
        // made one; the fetch below brings the real row, whatever the
        // server decided, so the placeholder goes either way.
        if (payload.creates.isNotEmpty()) {
            store.deleteMailboxes(payload.accountId, payload.creates.keys.toList())
        }
        refreshMailboxes(payload.accountId)
        if (outcome.isTooManyPinned) return StepResult.Rejected(CategoryActions.TOO_MANY_PINNED)
        outcome.errorMessage?.let { return StepResult.Rejected(it) }
        return StepResult.Done
    }

    /** Takes the server's labels into the store after a label write. */
    private suspend fun refreshMailboxes(accountId: String) {
        val fetched = runCatching { api.mailboxGet(accountId, null) }.getOrNull() ?: return
        store.upsertMailboxes(fetched.list.map { it.toStoreRow(accountId) })
    }

    /**
     * A filter-rule write. The server owns the rule set's shape - a mute
     * and a block are its own compositions - so on success the account's
     * rules are read back rather than patched locally, which is also what
     * gives a create its server-assigned id (suite REQ-FLT-20).
     */
    private suspend fun submitRule(entry: OutboxEntry): StepResult {
        val payload = runCatching {
            outboxJson.decodeFromString<RulePayload>(entry.payload)
        }.getOrNull() ?: return StepResult.Rejected("the queued filter change could not be read")
        val outcome = try {
            when (payload.op) {
                RuleOp.CREATE -> api.managedRuleSet(
                    payload.accountId,
                    create = mapOf(RULE_CREATE_KEY to (payload.rule ?: JsonObject(emptyMap()))),
                )

                RuleOp.UPDATE -> {
                    if (payload.updates.isEmpty()) return StepResult.Done
                    api.managedRuleSet(payload.accountId, update = payload.updates)
                }

                RuleOp.DESTROY -> api.managedRuleSet(
                    payload.accountId,
                    destroy = listOf(payload.ruleId ?: return StepResult.Rejected("the rule is gone")),
                )

                RuleOp.MUTE, RuleOp.UNMUTE -> {
                    api.threadMute(
                        payload.accountId,
                        payload.threadId ?: return StepResult.Rejected("the conversation is gone"),
                        muted = payload.op == RuleOp.MUTE,
                    )
                    null
                }

                RuleOp.BLOCK -> {
                    api.blockedSenderSet(
                        payload.accountId,
                        payload.address ?: return StepResult.Rejected("the sender is gone"),
                    )
                    null
                }
            }
        } catch (t: Throwable) {
            return failureOf(t)
        }
        outcome?.error?.let { return StepResult.Rejected(it) }
        refreshRules(payload.accountId)
        return StepResult.Done
    }

    /** Takes the server's rule set into the store after a rule write. */
    private suspend fun refreshRules(accountId: String) {
        val fetched = runCatching { api.managedRuleGet(accountId, null) }.getOrNull() ?: return
        store.clearManagedRules(accountId)
        store.upsertManagedRules(fetched.list.map { it.toRuleRow(accountId) })
    }

    /**
     * Messages the user took away: the draft a discard threw away
     * (issue #371). The local rows went when the discard was made, so
     * there is nothing to write back on success. A refusal puts the
     * message the server still holds back into the store, so the screen
     * and the server agree again, and the reason reaches the user.
     */
    private suspend fun submitDestroy(entry: OutboxEntry): StepResult {
        val payload = runCatching {
            outboxJson.decodeFromString<DestroyPayload>(entry.payload)
        }.getOrNull() ?: return StepResult.Rejected("the queued discard could not be read")
        if (payload.ids.isEmpty()) return StepResult.Done
        val outcome = try {
            api.emailDestroy(payload.accountId, payload.ids)
        } catch (t: Throwable) {
            return failureOf(t)
        }
        if (outcome.notDestroyed.isNotEmpty()) {
            restore(payload.accountId, outcome.notDestroyed.keys)
            return StepResult.Rejected(outcome.notDestroyed.values.first())
        }
        return StepResult.Done
    }

    /** Brings messages a destroy did not take back into the store. */
    private suspend fun restore(accountId: String, ids: Collection<String>) {
        tombstones.forget(accountId, ids)
        takeServerVersion(accountId, ids)
    }

    private suspend fun submitAction(entry: OutboxEntry): StepResult {
        val payload = runCatching {
            outboxJson.decodeFromString<ActionPayload>(entry.payload)
        }.getOrNull() ?: return StepResult.Rejected("the queued change could not be read")
        if (payload.patches.isEmpty()) return StepResult.Done
        val outcome = try {
            api.emailSet(entry.accountId, payload.patches)
        } catch (t: Throwable) {
            return failureOf(t)
        }
        if (outcome.notUpdated.isNotEmpty()) {
            return StepResult.Rejected(outcome.notUpdated.values.first())
        }
        takeServerVersion(entry.accountId, payload.patches.keys)
        return StepResult.Done
    }

    /**
     * A composed message: the attachments go up, the draft is written,
     * and - for a send - the submission follows. Each step records its
     * result in the entry, so a pass interrupted between two of them
     * resumes at the one that did not finish rather than uploading or
     * creating twice.
     */
    private suspend fun submitCompose(entry: OutboxEntry, submitToQueue: Boolean): StepResult {
        var payload = runCatching {
            outboxJson.decodeFromString<ComposePayload>(entry.payload)
        }.getOrNull() ?: return StepResult.Rejected("the queued message could not be read")

        for ((index, attachment) in payload.attachments.withIndex()) {
            if (attachment.blobId != null) continue
            val handle = attachment.spool
                ?: return StepResult.Rejected("${attachment.name} is no longer on the device")
            val bytes = spool.read(handle)
                ?: return StepResult.Rejected("${attachment.name} is no longer on the device")
            val uploaded = try {
                api.uploadBlob(payload.accountId, bytes, attachment.type, attachment.name)
            } catch (t: Throwable) {
                return failureOf(t)
            }
            val next = payload.attachments.toMutableList()
            next[index] = attachment.copy(blobId = uploaded.blobId)
            payload = payload.copy(attachments = next)
            outbox.updatePayload(entry.id, payload)
        }

        val email = composer.buildEmail(payload.toComposeState(), payload.draftsMailboxId)

        if (payload.draftId == null) {
            val written = try {
                api.emailCreate(payload.accountId, email)
            } catch (t: Throwable) {
                return failureOf(t)
            }
            val draftId = written.id
            if (written.error != null || draftId == null) {
                return StepResult.Rejected(written.error ?: "the draft was not saved")
            }
            payload = payload.copy(draftId = draftId)
            outbox.updatePayload(entry.id, payload)
            outbox.noteWritten(entry.id, draftId)
        } else if (!submitToQueue) {
            val written = try {
                api.emailReplace(payload.accountId, payload.draftId, email)
            } catch (t: Throwable) {
                return failureOf(t)
            }
            if (written.error != null) return StepResult.Rejected(written.error)
        }

        if (!submitToQueue) {
            // The user threw this draft away while the entry was being
            // submitted, so it could not be taken out of the queue: the
            // message the write has just left on the server goes the
            // way its local row already did (issue #371).
            val written = payload.draftId
            if (written != null && discardedMeanwhile(entry.id)) {
                store.deleteEmails(payload.accountId, listOf(written))
                outbox.enqueueDestroy(
                    payload.accountId,
                    MailActions.Labels.DISCARD_DRAFT,
                    listOf(written),
                )
            }
            return StepResult.Done
        }

        val outcome = try {
            api.sendEmail(
                accountId = payload.accountId,
                email = email,
                draftId = payload.draftId,
                identityId = payload.identityId,
                envelope = Envelope(
                    mailFrom = payload.identityEmail,
                    rcptTo = payload.recipients.map { it.email }.filter { it.isNotBlank() }.distinct(),
                ),
                onSuccessUpdate = onSuccessUpdate(payload),
                parentId = payload.parentId,
                parentKeyword = payload.parentKeyword,
            )
        } catch (t: Throwable) {
            return failureOf(t)
        }
        if (outcome.error != null) return StepResult.Rejected(outcome.error)
        return StepResult.Done
    }

    /**
     * True when a discard landed while this entry was in flight, so the
     * entry could not be taken out of the queue and the draft it has
     * just written has to come off the server (issue #371).
     */
    private suspend fun discardedMeanwhile(entryId: Long): Boolean =
        outbox.composePayload(entryId)?.discardAfterWrite == true

    /** The patch that moves a sent draft into Sent, as compose writes it. */
    private fun onSuccessUpdate(payload: ComposePayload): JsonObject = buildJsonObject {
        put("mailboxIds/${payload.draftsMailboxId}", JsonPrimitive(null as String?))
        payload.sentMailboxId?.let { put("mailboxIds/$it", true) }
        put("keywords/${Keywords.DRAFT}", JsonPrimitive(null as String?))
        put("keywords/${Keywords.SEEN}", true)
    }

    /**
     * Replaces the optimistic rows with what the server now holds for the
     * messages the entry touched (REQ-AND-SYNC-23). The per-type state
     * strings are left alone, so the next `Email/changes` still asks for
     * exactly what it would have asked for.
     */
    private suspend fun takeServerVersion(accountId: String, ids: Collection<String>) {
        if (ids.isEmpty()) return
        val fetched = runCatching { api.emailGet(accountId, ids.toList()) }.getOrNull() ?: return
        if (fetched.list.isNotEmpty()) {
            store.upsertEmails(fetched.list.map { it.toStoreRow(accountId) })
        }
    }

    /** Puts the messages of a refused action back as they were. */
    private suspend fun revert(entry: OutboxEntry) {
        val json = entry.revertJson ?: return
        val snapshots = runCatching {
            outboxJson.decodeFromString<List<MembershipSnapshot>>(json)
        }.getOrNull() ?: return
        snapshots.forEach { snapshot ->
            store.updateMembership(
                accountId = snapshot.accountId,
                id = snapshot.id,
                keywords = snapshot.keywords.toSet(),
                mailboxIds = snapshot.mailboxIds.toSet(),
                snoozedUntil = snapshot.snoozedUntil,
            )
        }
    }

    /** Removes a drained entry and the spooled files it owned. */
    private suspend fun discard(entry: OutboxEntry) {
        spooledHandles(entry).forEach { spool.remove(it) }
        outbox.remove(entry.id)
    }

    private fun spooledHandles(entry: OutboxEntry): List<String> = when (entry.kind) {
        OutboxKind.SEND, OutboxKind.DRAFT -> runCatching {
            outboxJson.decodeFromString<ComposePayload>(entry.payload)
        }.getOrNull()?.attachments?.mapNotNull { it.spool }.orEmpty()

        OutboxKind.BUG_REPORT -> runCatching {
            outboxJson.decodeFromString<BugReportPayload>(entry.payload)
        }.getOrNull()?.parts?.map { it.spool }.orEmpty()

        else -> emptyList()
    }

    /**
     * What the drain does with a thrown failure: a refusal the server
     * answered with stands, a busy server is worth another attempt, and a
     * failure on the wire leaves the entry untouched (issue #370).
     */
    private fun failureOf(t: Throwable): StepResult = when (classifyFailure(t)) {
        FailureKind.REFUSED -> StepResult.Rejected(t.message ?: "the server refused the change")
        FailureKind.UNSUPPORTED -> StepResult.Deferred(
            t.message ?: "this server does not take this request",
        )

        FailureKind.BUSY -> StepResult.Retry(t.message ?: "the server could not do this now")
        FailureKind.OFFLINE -> StepResult.Offline(t.message ?: "no connection")
    }

    /** What the outbox screen says about an entry the drain gave up on. */
    private fun gaveUpMessage(reason: String, attempts: Int): String =
        "$reason - unsent after $attempts attempts; it is kept here and goes out on a retry"

    private fun backoff(attempts: Int): Long = widening(attempts, backoffBaseMs, backoffCapMs)

    /**
     * How long an entry waits for the server to catch up. It widens like
     * the transient backoff but from minutes rather than seconds: the
     * condition it waits out is a server deployment, not a busy moment.
     */
    private fun deferredBackoff(attempts: Int): Long = widening(attempts, deferredBaseMs, deferredCapMs)

    private fun widening(attempts: Int, baseMs: Long, capMs: Long): Long {
        var delay = baseMs
        repeat(attempts - 1) { delay = (delay * 2).coerceAtMost(capMs) }
        return delay.coerceAtMost(capMs)
    }

    private companion object {
        /** The creation key a `ManagedRule/set` create is routed back by. */
        private const val RULE_CREATE_KEY = "rule1"

        const val DEFAULT_MAX_ATTEMPTS = 6
        const val DEFAULT_BACKOFF_BASE_MS = 5_000L
        const val DEFAULT_BACKOFF_CAP_MS = 5 * 60_000L

        /**
         * 24 offers on the widening schedule below spend about eight
         * days, so a client that has outrun its server survives a
         * deployment that takes a working week.
         */
        const val DEFAULT_MAX_DEFERRED_ATTEMPTS = 24
        const val DEFAULT_DEFERRED_BASE_MS = 15 * 60_000L
        const val DEFAULT_DEFERRED_CAP_MS = 12 * 60 * 60_000L
    }
}

/**
 * The compose the payload describes: what the wire builder renders at
 * drain time, and what the composer reopens on when a send is taken back
 * within its undo window (issue #354).
 */
fun ComposePayload.toComposeState(): ComposeState = ComposeState(
    mode = if (parentId == null) ComposeMode.NEW else ComposeMode.REPLY,
    accountId = accountId,
    identity = Identity(
        accountId = accountId,
        id = identityId,
        name = identityName,
        email = identityEmail,
    ),
    to = to.map { MailAddress(it.name, it.email) },
    cc = cc.map { MailAddress(it.name, it.email) },
    bcc = bcc.map { MailAddress(it.name, it.email) },
    subject = subject,
    bodyHtml = bodyHtml,
    attachments = attachments.map {
        ComposeAttachment(
            key = (it.blobId ?: it.spool).orEmpty() + ":" + it.name,
            name = it.name,
            type = it.type,
            size = it.size,
            blobId = it.blobId,
            status = if (it.blobId == null) AttachmentStatus.PENDING else AttachmentStatus.READY,
            inline = it.inline,
            cid = it.cid,
            spool = it.spool,
        )
    },
    replyContext = parentId?.let {
        ReplyContext(
            accountId = accountId,
            parentId = it,
            threadId = threadId.orEmpty(),
            parentKeyword = parentKeyword.orEmpty(),
            inReplyTo = inReplyTo,
            references = references,
        )
    },
    draftId = draftId,
)
