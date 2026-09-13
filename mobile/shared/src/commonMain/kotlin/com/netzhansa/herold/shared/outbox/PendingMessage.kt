package com.netzhansa.herold.shared.outbox

/**
 * A composed message the conversation shows before the server has it
 * (issue #369). A reply written with no connection is in the outbox, not
 * in the store, so the thread would otherwise end at the message being
 * answered; this is what the thread renders in its place until the drain
 * has put the real one there.
 */
data class PendingMessage(
    val entryId: Long,
    val accountId: String,
    val threadId: String?,
    val kind: OutboxKind,
    val state: OutboxState,
    val subject: String,
    val bodyHtml: String,
    val fromName: String,
    val fromEmail: String,
    val to: List<OutboxAddress>,
    val createdAt: Long,
    /** What the server refused it with, when it did. */
    val failure: String?,
) {
    /** What the marker on the message says. */
    val marker: String
        get() = when {
            failure != null -> MARKER_FAILED
            kind == OutboxKind.DRAFT -> MARKER_DRAFT
            state == OutboxState.SENDING -> MARKER_SENDING
            else -> MARKER_QUEUED
        }

    /** The recipients as one line, the way a sent message's header reads. */
    val recipientLine: String
        get() = to.joinToString(", ") { it.name?.takeIf { name -> name.isNotBlank() } ?: it.email }

    companion object {
        const val MARKER_QUEUED = "Queued"
        const val MARKER_SENDING = "Sending"
        const val MARKER_DRAFT = "Draft"
        const val MARKER_FAILED = "Not sent"
    }
}

/**
 * The composed messages these entries hold. An entry whose payload cannot
 * be read is left out rather than rendered as an empty message.
 */
fun List<OutboxEntry>.pendingMessages(): List<PendingMessage> = mapNotNull { entry ->
    if (entry.kind != OutboxKind.SEND && entry.kind != OutboxKind.DRAFT) return@mapNotNull null
    val payload = runCatching {
        outboxJson.decodeFromString<ComposePayload>(entry.payload)
    }.getOrNull() ?: return@mapNotNull null
    PendingMessage(
        entryId = entry.id,
        accountId = entry.accountId,
        threadId = payload.threadId,
        kind = entry.kind,
        state = entry.state,
        subject = payload.subject,
        bodyHtml = payload.bodyHtml,
        fromName = payload.identityName,
        fromEmail = payload.identityEmail,
        to = payload.to + payload.cc,
        createdAt = entry.createdAt,
        failure = entry.lastError.takeIf { entry.permanent },
    )
}.sortedBy { it.createdAt }

/** Those of them that belong to one conversation. */
fun List<OutboxEntry>.pendingMessagesIn(accountId: String, threadId: String): List<PendingMessage> =
    pendingMessages().filter { it.accountId == accountId && it.threadId == threadId }

/** The conversations that have something waiting, and what their marker says. */
fun List<OutboxEntry>.pendingMarkersByThread(): Map<String, String> =
    pendingMessages().mapNotNull { message -> message.threadId?.let { it to message.marker } }.toMap()
