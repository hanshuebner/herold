package com.netzhansa.herold.shared.outbox

import com.netzhansa.herold.shared.compose.AttachmentStatus
import com.netzhansa.herold.shared.compose.ComposeAttachment
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.compose.ComposeState
import com.netzhansa.herold.shared.compose.Composer
import com.netzhansa.herold.shared.compose.ReplyContext
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.jmap.Envelope
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.store.LocalStore
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
}

/** An entry the server refused, for the snackbar the shell raises. */
data class OutboxFailure(val entryId: Long, val label: String, val message: String)

/** What a drain pass did, for a background worker's result. */
data class DrainOutcome(
    val submitted: Int = 0,
    val rejected: Int = 0,
    val retryable: Int = 0,
    val pending: Int = 0,
) {
    /** True when entries remain that a later pass should pick up. */
    val hasMore: Boolean get() = pending > 0
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
 */
class OutboxDrainer(
    private val api: JmapApi,
    private val store: LocalStore,
    private val outbox: Outbox,
    private val spool: BlobSpool,
    private val composer: Composer = Composer(api),
    private val now: () -> Long = { 0L },
    private val maxAttempts: Int = DEFAULT_MAX_ATTEMPTS,
    private val backoffBaseMs: Long = DEFAULT_BACKOFF_BASE_MS,
    private val backoffCapMs: Long = DEFAULT_BACKOFF_CAP_MS,
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
        val byAccount = outbox.list().filter { it.isPending }.groupBy { it.accountId }
        for ((_, entries) in byAccount) {
            for (entry in entries.sortedBy { it.id }) {
                val due = entry.nextAttemptAt <= now()
                if (!due) {
                    // A send held for its undo window lets the entries
                    // behind it through; a backoff after a failure holds
                    // the account's order until the failed entry drains.
                    if (entry.attempts > 0) break else continue
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
                        discard(entry)
                        submitted++
                    }

                    is StepResult.Rejected -> {
                        revert(entry)
                        store.updateOutboxState(
                            id = entry.id,
                            state = OutboxState.FAILED,
                            attempts = entry.attempts + 1,
                            lastError = result.message,
                            permanent = true,
                            nextAttemptAt = 0,
                        )
                        _failures.tryEmit(OutboxFailure(entry.id, entry.label, result.message))
                        rejected++
                    }

                    is StepResult.Retry -> {
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
                        retryable++
                        break
                    }
                }
            }
        }
        DrainOutcome(
            submitted = submitted,
            rejected = rejected,
            retryable = retryable,
            pending = outbox.list().count { it.isPending },
        )
    }

    private suspend fun submit(entry: OutboxEntry): StepResult = when (entry.kind) {
        OutboxKind.ACTION -> submitAction(entry)
        OutboxKind.DRAFT -> submitCompose(entry, submitToQueue = false)
        OutboxKind.SEND -> submitCompose(entry, submitToQueue = true)
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
        } else if (!submitToQueue) {
            val written = try {
                api.emailReplace(payload.accountId, payload.draftId, email)
            } catch (t: Throwable) {
                return failureOf(t)
            }
            if (written.error != null) return StepResult.Rejected(written.error)
        }

        if (!submitToQueue) return StepResult.Done

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

    private fun spooledHandles(entry: OutboxEntry): List<String> {
        if (entry.kind == OutboxKind.ACTION) return emptyList()
        val payload = runCatching {
            outboxJson.decodeFromString<ComposePayload>(entry.payload)
        }.getOrNull() ?: return emptyList()
        return payload.attachments.mapNotNull { it.spool }
    }

    /**
     * Whether a thrown failure is worth another attempt. A status the
     * server answered with in the 4xx range, other than the two that mean
     * "come back later", is the server's decision and stands.
     */
    private fun failureOf(t: Throwable): StepResult {
        if (t is JmapException) {
            val status = t.status
            val clientError = status != null && status in 400..499 &&
                status != HTTP_REQUEST_TIMEOUT && status != HTTP_TOO_MANY_REQUESTS &&
                status != HTTP_UNAUTHORIZED
            val refused = t.methodError != null && t.methodError !in RETRYABLE_METHOD_ERRORS
            if (clientError || refused) return StepResult.Rejected(t.message ?: "the server refused the change")
        }
        return StepResult.Retry(t.message ?: "no connection")
    }

    private fun backoff(attempts: Int): Long {
        var delay = backoffBaseMs
        repeat(attempts - 1) { delay = (delay * 2).coerceAtMost(backoffCapMs) }
        return delay.coerceAtMost(backoffCapMs)
    }

    private companion object {
        const val DEFAULT_MAX_ATTEMPTS = 6
        const val DEFAULT_BACKOFF_BASE_MS = 5_000L
        const val DEFAULT_BACKOFF_CAP_MS = 5 * 60_000L
        const val HTTP_REQUEST_TIMEOUT = 408
        const val HTTP_TOO_MANY_REQUESTS = 429
        const val HTTP_UNAUTHORIZED = 401

        /** Method errors that describe a busy server rather than a refusal. */
        val RETRYABLE_METHOD_ERRORS = setOf("serverFail", "serverUnavailable", "serverPartialFail")
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
            parentKeyword = parentKeyword.orEmpty(),
            inReplyTo = inReplyTo,
            references = references,
        )
    },
    draftId = draftId,
)
