package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.mail.HtmlSanitizer

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

    /**
     * The original as the quote carries it: its own markup when it has
     * some, the paragraph form of its text otherwise (issue #431).
     *
     * An HTML original goes in as the sanitized fragment of what the
     * reader saw, so a forward keeps the layout, the styling and the
     * images of the message it forwards. The `text/plain` alternative of
     * the outgoing message is derived from this body by the composer, so
     * the recipient still gets the text rendering the quote used to be.
     */
    private fun quotedBody(parent: Email): String {
        parent.bodyHtml?.let { html ->
            val fragment = HtmlSanitizer.quoteFragment(html)
            if (fragment.contains("<img", ignoreCase = true) || HtmlText.toPlainText(fragment).isNotBlank()) {
                return fragment
            }
        }
        val text = parent.bodyText?.takeIf { it.isNotBlank() }
            ?: parent.preview.takeIf { it.isNotBlank() }
        return text?.let { HtmlText.toHtml(it) } ?: "<p>(no quoted body)</p>"
    }

    /**
     * The parent's inline parts the quoted body still points at, as
     * entries the send carries (issue #431). They reference the parent's
     * blobs, like the forwarded files do, so a `cid:` in the quote names
     * a part the outgoing message actually holds. An inline entry lives
     * in the body rather than in the chip strip (suite G8), so it does
     * not show as an attachment.
     *
     * A reference the parent has no part for is left in the body as the
     * original wrote it; there is nothing to carry for it.
     */
    fun quotedInlineAttachments(parent: Email, quote: String): List<ComposeAttachment> {
        val inline = parent.attachments.filter { it.isInline }
        if (inline.isEmpty()) return emptyList()
        return referencedCids(quote).mapNotNull { reference ->
            val part = inline.firstOrNull { it.cid?.trim('<', '>') == reference || it.name == reference }
            part?.let { reference to it }
        }.mapIndexed { index, (reference, part) ->
            ComposeAttachment(
                key = "quoted-$index-${part.blobId}",
                name = part.name,
                type = part.type.ifBlank { "application/octet-stream" },
                size = part.size,
                blobId = part.blobId,
                status = AttachmentStatus.READY,
                inline = true,
                cid = reference,
            )
        }
    }

    /** The `cid:` targets a body names, in the order it names them. */
    fun referencedCids(html: String): List<String> = inlineReference.findAll(html)
        .map { it.groupValues[2].ifEmpty { it.groupValues[3] } }
        .map { it.trim().trim('<', '>') }
        .filter { it.isNotBlank() }
        .distinct()
        .toList()

    private val inlineReference =
        Regex("""src\s*=\s*("cid:([^"]*)"|'cid:([^']*)')""", RegexOption.IGNORE_CASE)

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
