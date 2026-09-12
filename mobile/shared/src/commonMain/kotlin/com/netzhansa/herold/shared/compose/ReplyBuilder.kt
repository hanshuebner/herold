package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.MailAddress

/** What the user opened the composer for. */
enum class ComposeMode { NEW, REPLY, REPLY_ALL, FORWARD, EDIT_DRAFT }

/**
 * Recipients, subject, threading headers and the quoted original of a
 * reply or forward.
 *
 * The rules mirror the suite's
 * (`web/apps/suite/src/lib/compose/compose.svelte.ts`): the same marker
 * vocabulary, the same own-sent handling, the same Cc derivation and the
 * same quote layout, so a conversation read on one client and answered on
 * the other looks the same to the recipient.
 */
object ReplyBuilder {

    /** Reply markers the subject collapses, English plus the ones that arrive from other clients. */
    private val replyMarkers = listOf("re", "aw", "antwort", "antw", "sv", "vs", "odp", "res", "rif")

    /** Forward markers, same vocabulary as the suite. */
    private val forwardMarkers = listOf("fwd", "fw", "wg", "tr", "rv", "vb")

    fun replySubject(original: String?): String = "Re: " + stripMarkers(original.orEmpty(), replyMarkers)

    fun forwardSubject(original: String?): String = "Fwd: " + stripMarkers(original.orEmpty(), forwardMarkers)

    /**
     * The To list. A reply to a message the user sent goes to that
     * message's recipients (suite REQ-MAIL-30); everything else goes to the
     * sender. An own-sent message with no visible To falls back to the
     * sender so the field is never left empty.
     */
    fun replyTo(parent: Email, selfEmails: Set<String>): List<MailAddress> {
        if (isOwnMessage(parent, selfEmails)) {
            val recipients = parent.toAddresses.filter { it.email.isNotBlank() }
            if (recipients.isNotEmpty()) return recipients
        }
        return listOfNotNull(parent.fromAddress.takeIf { it.email.isNotBlank() })
    }

    /**
     * The Cc list of a reply-all: the parent's To then Cc, minus the
     * user's own addresses and minus the primary reply target. For a
     * message the user sent, only the parent's Cc carries over - its To
     * becomes the reply's To.
     */
    fun replyAllCc(parent: Email, selfEmails: Set<String>): List<MailAddress> {
        val own = isOwnMessage(parent, selfEmails)
        val seen = mutableSetOf<String>()
        val out = mutableListOf<MailAddress>()
        if (own) {
            parent.toAddresses.forEach { seen.add(it.email.lowercase()) }
        } else {
            seen.add(parent.fromEmail.lowercase())
        }
        val sources = if (own) listOf(parent.ccAddresses) else listOf(parent.toAddresses, parent.ccAddresses)
        sources.forEach { list ->
            list.forEach { address ->
                val key = address.email.lowercase()
                if (key.isBlank() || key in selfEmails || key in seen) return@forEach
                seen.add(key)
                out.add(address)
            }
        }
        return out
    }

    /** The `references` of the reply: the parent's references then its Message-ID. */
    fun references(parent: Email): List<String> =
        (parent.references + parent.messageId).distinct().filter { it.isNotBlank() }

    /** The `inReplyTo` of the reply: the parent's Message-ID. */
    fun inReplyTo(parent: Email): List<String> = parent.messageId.filter { it.isNotBlank() }

    /**
     * The reply body: two empty paragraphs for the cursor, the attribution
     * line, then the original in a `<blockquote>`.
     */
    fun replyQuote(parent: Email, sentAtLabel: String?): String {
        val sender = parent.fromAddress.takeIf { it.email.isNotBlank() }?.format() ?: "(unknown sender)"
        val header = if (sentAtLabel.isNullOrBlank()) {
            "$sender wrote:"
        } else {
            "On $sentAtLabel, $sender wrote:"
        }
        return "<p></p><p></p><p>${HtmlText.escape(header)}</p>" +
            "<blockquote>${quotedBody(parent)}</blockquote><p></p>"
    }

    /**
     * The forward body: the five header lines in one paragraph, then the
     * original in a `<blockquote>`.
     */
    fun forwardQuote(parent: Email, sentAtLabel: String?): String {
        val header = listOf(
            "---------- Forwarded message ----------",
            "From: " + parent.fromAddress.takeIf { it.email.isNotBlank() }?.format().orEmpty(),
            "Date: " + sentAtLabel.orEmpty(),
            "Subject: " + parent.subject,
            "To: " + parent.toAddresses.joinToString(", ") { it.format() },
        ).joinToString("<br>") { HtmlText.escape(it) }
        return "<p></p><p></p><p>$header</p><blockquote>${quotedBody(parent)}</blockquote><p></p>"
    }

    /**
     * The attachments a forward carries over. They reference the parent's
     * blobs directly - the blobs live in the same account's store, so
     * nothing is re-uploaded (suite re #273). Inline parts belong to the
     * quoted body and are left out of the chip strip.
     */
    fun forwardAttachments(parent: Email): List<ComposeAttachment> =
        parent.attachments.filterNot { it.isInline }.mapIndexed { index, attachment ->
            ComposeAttachment(
                key = "fwd-$index-${attachment.blobId}",
                name = attachment.name,
                type = attachment.type.ifBlank { "application/octet-stream" },
                size = attachment.size,
                blobId = attachment.blobId,
                status = AttachmentStatus.READY,
            )
        }

    /** True when the parent is a message the user sent (suite REQ-MAIL-30a). */
    fun isOwnMessage(parent: Email, selfEmails: Set<String>): Boolean =
        parent.deliveredTo.isNullOrBlank() && parent.fromEmail.lowercase() in selfEmails

    private fun quotedBody(parent: Email): String {
        val text = parent.bodyText?.takeIf { it.isNotBlank() }
            ?: parent.bodyHtml?.let { HtmlText.toPlainText(it) }?.takeIf { it.isNotBlank() }
            ?: parent.preview.takeIf { it.isNotBlank() }
        return text?.let { HtmlText.toHtml(it) } ?: "<p>(no quoted body)</p>"
    }

    private fun stripMarkers(subject: String, markers: List<String>): String {
        val pattern = Regex("^(?:${markers.joinToString("|")})\\s*:\\s*", RegexOption.IGNORE_CASE)
        var value = subject.trim()
        while (true) {
            val next = pattern.replace(value, "").trim()
            if (next == value) return value
            value = next
        }
    }
}
