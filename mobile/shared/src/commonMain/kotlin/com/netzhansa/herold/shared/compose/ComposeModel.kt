package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.MailAddress

/**
 * Where an attachment is in its upload (suite REQ-ATT-03). `PENDING` is
 * the offline state: the file is spooled in app-private storage and goes
 * up when the send drains (REQ-AND-SYNC-21).
 */
enum class AttachmentStatus { UPLOADING, PENDING, READY, FAILED }

/**
 * One file or inline image on the compose. [inline] is the user's choice,
 * not a guess from the MIME type (suite REQ-ATT-01/06, G8): an inline
 * entry is referenced from the body by [cid] and carries
 * `disposition: "inline"` on the wire; an attachment carries
 * `disposition: "attachment"` and appears only in the chip strip.
 */
data class ComposeAttachment(
    val key: String,
    val name: String,
    val type: String,
    val size: Long,
    val blobId: String? = null,
    val status: AttachmentStatus = AttachmentStatus.UPLOADING,
    val inline: Boolean = false,
    val cid: String? = null,
    val error: String? = null,
    /** Bytes held until the upload finishes, so an inline image can render at once. */
    val bytes: ByteArray? = null,
    /** The app-private copy the outbox uploads from when the send drains. */
    val spool: String? = null,
) {
    val isReady: Boolean get() = status == AttachmentStatus.READY && blobId != null

    /** True when the file is on the server or on the device, so a send carries it. */
    val isCarried: Boolean get() = isReady || (status == AttachmentStatus.PENDING && spool != null)

    override fun equals(other: Any?): Boolean =
        other is ComposeAttachment &&
            key == other.key &&
            name == other.name &&
            type == other.type &&
            size == other.size &&
            blobId == other.blobId &&
            status == other.status &&
            spool == other.spool &&
            inline == other.inline &&
            cid == other.cid &&
            error == other.error

    override fun hashCode(): Int {
        var result = key.hashCode()
        result = 31 * result + name.hashCode()
        result = 31 * result + type.hashCode()
        result = 31 * result + size.hashCode()
        result = 31 * result + (blobId?.hashCode() ?: 0)
        result = 31 * result + status.hashCode()
        result = 31 * result + inline.hashCode()
        result = 31 * result + (cid?.hashCode() ?: 0)
        result = 31 * result + (error?.hashCode() ?: 0)
        return result
    }
}

/** The parent a reply or forward answers, and the keyword it earns on send. */
data class ReplyContext(
    val accountId: String,
    val parentId: String,
    val parentKeyword: String,
    val inReplyTo: List<String>,
    val references: List<String>,
)

/**
 * Everything the composer holds. It is a plain value so the screen can
 * keep it in Compose state and the send/save paths take it as an argument
 * (docs/design/android/architecture/05-ui-shell.md, "Testing hook").
 */
data class ComposeState(
    val mode: ComposeMode,
    val accountId: String,
    val identity: Identity?,
    val to: List<MailAddress> = emptyList(),
    val cc: List<MailAddress> = emptyList(),
    val bcc: List<MailAddress> = emptyList(),
    val subject: String = "",
    val bodyHtml: String = "",
    val attachments: List<ComposeAttachment> = emptyList(),
    val replyContext: ReplyContext? = null,
    /** The server-side draft this compose is saved as, once it has been. */
    val draftId: String? = null,
    /** The queued draft save this compose left in the outbox, if any. */
    val draftEntryId: Long? = null,
    val showCc: Boolean = false,
) {
    val recipients: List<MailAddress> get() = to + cc + bcc

    val hasRecipient: Boolean get() = recipients.any { it.email.isNotBlank() }

    val uploading: Boolean get() = attachments.any { it.status == AttachmentStatus.UPLOADING }
}

/** Keywords a send sets on the parent of a reply or forward (suite REQ-MAIL-33). */
object ParentKeywords {
    const val ANSWERED = "\$answered"
    const val FORWARDED = "\$forwarded"
}
