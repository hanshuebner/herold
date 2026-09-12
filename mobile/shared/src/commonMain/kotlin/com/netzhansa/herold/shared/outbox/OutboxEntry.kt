package com.netzhansa.herold.shared.outbox

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject

/** What an entry is waiting to do (REQ-AND-SYNC-20/21). */
enum class OutboxKind {
    /** An optimistic action: one `Email/set` patch per affected message. */
    ACTION,

    /** A composed message saved as a draft in the account's Drafts mailbox. */
    DRAFT,

    /** A composed message to upload, write and submit, in that order. */
    SEND,
    ;

    companion object {
        fun from(value: String): OutboxKind =
            entries.firstOrNull { it.name.equals(value, ignoreCase = true) } ?: ACTION
    }
}

/** Where an entry is in its drain (REQ-AND-SYNC-25). */
enum class OutboxState {
    QUEUED,
    SENDING,
    FAILED,
    ;

    /** The lower-case form the database column holds. */
    val wire: String get() = name.lowercase()

    companion object {
        fun from(value: String): OutboxState =
            entries.firstOrNull { it.name.equals(value, ignoreCase = true) } ?: QUEUED
    }
}

/**
 * One pending mutation. It carries everything the drain needs with no
 * in-memory state, so a queue built before the process died drains after
 * the next launch (REQ-AND-SYNC-22).
 *
 * @param label what the outbox screen calls the entry
 * @param payload the JSON the drainer submits, rewritten as a multi-step
 *   entry advances so a resumed drain skips the steps that already took
 * @param revertJson the pre-change snapshot a permanent rejection restores
 * @param entityIds the messages the entry touches, for the server-wins rule
 * @param permanent true when the server refused the entry outright, so the
 *   drain leaves it alone until the user retries it
 */
data class OutboxEntry(
    val id: Long,
    val accountId: String,
    val kind: OutboxKind,
    val label: String,
    val payload: String,
    val revertJson: String?,
    val entityIds: List<String>,
    val createdAt: Long,
    val state: OutboxState,
    val attempts: Int,
    val lastError: String?,
    val permanent: Boolean,
    val nextAttemptAt: Long,
) {
    /** True while the entry is still going to be submitted on its own. */
    val isPending: Boolean get() = state != OutboxState.FAILED
}

/** A new entry as a caller hands it in; the store assigns the id. */
data class NewOutboxEntry(
    val accountId: String,
    val kind: OutboxKind,
    val label: String,
    val payload: String,
    val revertJson: String? = null,
    val entityIds: List<String> = emptyList(),
    val createdAt: Long = 0,
)

/** The membership one message had before an action, for the revert. */
@Serializable
data class MembershipSnapshot(
    val accountId: String,
    val id: String,
    val keywords: List<String> = emptyList(),
    val mailboxIds: List<String> = emptyList(),
    val snoozedUntil: String? = null,
)

/** The `Email/set` patches an action entry submits, by message id. */
@Serializable
data class ActionPayload(val patches: Map<String, JsonObject>)

/**
 * A file on a composed message. An attachment picked offline is spooled
 * into app-private storage and carried by [spool] until the drain uploads
 * it and records its [blobId]; the picker's URI is not depended on past
 * the moment the user chose the file (REQ-AND-SYNC-21).
 */
@Serializable
data class OutboxAttachment(
    val name: String,
    val type: String,
    val size: Long,
    val inline: Boolean = false,
    val cid: String? = null,
    val blobId: String? = null,
    val spool: String? = null,
)

/** An address as the payload carries it. */
@Serializable
data class OutboxAddress(val name: String? = null, val email: String)

/**
 * A composed message waiting to be written and, for a [OutboxKind.SEND],
 * submitted. It holds the compose's content rather than a finished `Email`
 * object because the blob ids of attachments spooled offline are only known
 * once the drain has uploaded them.
 */
@Serializable
data class ComposePayload(
    val accountId: String,
    val identityId: String,
    val identityName: String = "",
    val identityEmail: String,
    val to: List<OutboxAddress> = emptyList(),
    val cc: List<OutboxAddress> = emptyList(),
    val bcc: List<OutboxAddress> = emptyList(),
    val subject: String = "",
    val bodyHtml: String = "",
    val attachments: List<OutboxAttachment> = emptyList(),
    val draftsMailboxId: String,
    val sentMailboxId: String? = null,
    /** The server-side draft, once an earlier attempt created it. */
    val draftId: String? = null,
    val parentId: String? = null,
    val parentKeyword: String? = null,
    val inReplyTo: List<String> = emptyList(),
    val references: List<String> = emptyList(),
) {
    val recipients: List<OutboxAddress> get() = to + cc + bcc
}

/** The serializer the payload columns are written and read with. */
val outboxJson: Json = Json { ignoreUnknownKeys = true }
