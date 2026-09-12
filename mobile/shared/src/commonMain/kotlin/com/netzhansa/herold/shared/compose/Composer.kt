package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.Envelope
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.add
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject

/** What a save or a send produced. */
sealed interface ComposeResult {
    data class Saved(val draftId: String) : ComposeResult

    data class Sent(val emailId: String?) : ComposeResult

    /** The server refused, or it could not be reached; the compose stays open. */
    data class Failed(val message: String, val offline: Boolean = false) : ComposeResult
}

/** The outcome of adding a file to the compose. */
sealed interface AttachResult {
    data class Added(val attachment: ComposeAttachment) : AttachResult

    data class Rejected(val message: String) : AttachResult
}

/**
 * Compose's server side: it opens a compose from a parent message, uploads
 * attachments, writes the draft into the account's Drafts mailbox and
 * sends through `EmailSubmission/set`.
 *
 * Milestone 1c sends online only. A send without connectivity fails
 * visibly rather than queueing; the durable outbox is milestone 2
 * (REQ-AND-SYNC-21).
 */
class Composer(
    private val api: JmapApi,
    private val newCid: () -> String = { "inl-" + randomToken() + "@herold.local" },
) {

    /** A compose with no parent, on the account in scope. */
    fun openNew(
        identities: List<Identity>,
        accounts: List<Account>,
        accountInScope: String?,
        to: List<com.netzhansa.herold.shared.domain.MailAddress> = emptyList(),
    ): ComposeState {
        val identity = IdentityChoice.defaultForNew(identities, accounts, accountInScope)
        return ComposeState(
            mode = ComposeMode.NEW,
            accountId = identity?.accountId ?: accountInScope ?: accounts.firstOrNull()?.id.orEmpty(),
            identity = identity,
            to = to,
        )
    }

    /**
     * A reply, reply-all or forward of [parent]. The compose lives on the
     * parent's account, which is what keeps a reply on the account that
     * received the message (suite REQ-MAIL-SUB-05).
     */
    fun openFrom(
        mode: ComposeMode,
        parent: Email,
        identities: List<Identity>,
        accounts: List<Account>,
        sentAtLabel: String?,
    ): ComposeState {
        val identity = IdentityChoice.defaultForReply(parent, identities, accounts)
        val selfEmails = IdentityChoice.selfEmails(identities)
        return when (mode) {
            ComposeMode.FORWARD -> ComposeState(
                mode = mode,
                accountId = parent.accountId,
                identity = identity,
                subject = ReplyBuilder.forwardSubject(parent.subject),
                bodyHtml = ReplyBuilder.forwardQuote(parent, sentAtLabel),
                attachments = ReplyBuilder.forwardAttachments(parent),
                replyContext = ReplyContext(
                    accountId = parent.accountId,
                    parentId = parent.id,
                    parentKeyword = ParentKeywords.FORWARDED,
                    inReplyTo = ReplyBuilder.inReplyTo(parent),
                    references = ReplyBuilder.references(parent),
                ),
            )

            else -> {
                val cc = if (mode == ComposeMode.REPLY_ALL) {
                    ReplyBuilder.replyAllCc(parent, selfEmails)
                } else {
                    emptyList()
                }
                ComposeState(
                    mode = mode,
                    accountId = parent.accountId,
                    identity = identity,
                    to = ReplyBuilder.replyTo(parent, selfEmails),
                    cc = cc,
                    showCc = cc.isNotEmpty(),
                    subject = ReplyBuilder.replySubject(parent.subject),
                    bodyHtml = ReplyBuilder.replyQuote(parent, sentAtLabel),
                    replyContext = ReplyContext(
                        accountId = parent.accountId,
                        parentId = parent.id,
                        parentKeyword = ParentKeywords.ANSWERED,
                        inReplyTo = ReplyBuilder.inReplyTo(parent),
                        references = ReplyBuilder.references(parent),
                    ),
                )
            }
        }
    }

    /**
     * Uploads one file and returns the entry the chip strip renders. An
     * upload over the session's `maxSizeUpload` is refused before any
     * bytes leave the device (suite REQ-ATT-04).
     */
    suspend fun attach(
        accountId: String,
        name: String,
        type: String,
        bytes: ByteArray,
        inline: Boolean,
    ): AttachResult {
        val limit = runCatching { api.session().maxSizeUpload }.getOrNull()
        if (limit != null && bytes.size > limit) {
            return AttachResult.Rejected(
                "$name is ${formatSize(bytes.size.toLong())}; this server accepts at most ${formatSize(limit)}",
            )
        }
        val uploaded = try {
            api.uploadBlob(accountId, bytes, type, name)
        } catch (e: JmapException) {
            return AttachResult.Rejected(e.message ?: "the upload was rejected")
        } catch (t: Throwable) {
            return AttachResult.Rejected("No connection - $name was not uploaded")
        }
        return AttachResult.Added(
            ComposeAttachment(
                key = uploaded.blobId + ":" + name,
                name = name,
                type = uploaded.type.ifBlank { type },
                size = if (uploaded.size > 0) uploaded.size else bytes.size.toLong(),
                blobId = uploaded.blobId,
                status = AttachmentStatus.READY,
                inline = inline,
                cid = if (inline) newCid() else null,
                bytes = if (inline) bytes else null,
            ),
        )
    }

    /**
     * Writes the compose into the account's Drafts mailbox: a create the
     * first time, an update of the same message after that (suite
     * REQ-DFT-02).
     */
    suspend fun saveDraft(state: ComposeState, mailboxes: List<Mailbox>): ComposeResult {
        val draftsId = roleMailbox(mailboxes, state.accountId, MailboxRoles.DRAFTS)
            ?: return ComposeResult.Failed("This account has no Drafts mailbox")
        val email = buildEmail(state, draftsId)
        return try {
            val outcome = if (state.draftId == null) {
                api.emailCreate(state.accountId, email)
            } else {
                api.emailReplace(state.accountId, state.draftId, email)
            }
            val id = outcome.id
            if (outcome.error != null || id == null) {
                ComposeResult.Failed(outcome.error ?: "the draft was not saved")
            } else {
                ComposeResult.Saved(id)
            }
        } catch (e: JmapException) {
            ComposeResult.Failed(e.message ?: "the draft was not saved")
        } catch (t: Throwable) {
            ComposeResult.Failed("No connection - the draft was not saved", offline = true)
        }
    }

    /** Drops a draft the user discarded (suite REQ-DFT-42). */
    suspend fun discardDraft(accountId: String, draftId: String) {
        runCatching { api.emailDestroy(accountId, listOf(draftId)) }
    }

    /**
     * Sends: `Email/set` writes the draft and `EmailSubmission/set` hands
     * it to the queue, with `onSuccessUpdateEmail` moving it into Sent and
     * clearing `$draft` in the same round trip.
     */
    suspend fun send(state: ComposeState, mailboxes: List<Mailbox>): ComposeResult {
        if (state.identity == null) return ComposeResult.Failed("Choose an address to send from")
        if (!state.hasRecipient) return ComposeResult.Failed("Add at least one recipient")
        if (state.uploading) return ComposeResult.Failed("Wait for the attachments to finish uploading")
        val draftsId = roleMailbox(mailboxes, state.accountId, MailboxRoles.DRAFTS)
            ?: return ComposeResult.Failed("This account has no Drafts mailbox")
        val sentId = roleMailbox(mailboxes, state.accountId, MailboxRoles.SENT)

        val onSuccessUpdate = buildJsonObject {
            put("mailboxIds/$draftsId", JsonPrimitive(null as String?))
            if (sentId != null) put("mailboxIds/$sentId", true)
            put("keywords/${Keywords.DRAFT}", JsonPrimitive(null as String?))
            put("keywords/${Keywords.SEEN}", true)
        }
        val parent = state.replyContext?.takeIf { it.accountId == state.accountId }
        return try {
            val outcome = api.sendEmail(
                accountId = state.accountId,
                email = buildEmail(state, draftsId),
                draftId = state.draftId,
                identityId = state.identity.id,
                envelope = Envelope(
                    mailFrom = state.identity.email,
                    rcptTo = state.recipients.map { it.email }.filter { it.isNotBlank() }.distinct(),
                ),
                onSuccessUpdate = onSuccessUpdate,
                parentId = parent?.parentId,
                parentKeyword = parent?.parentKeyword,
            )
            if (outcome.error != null) {
                ComposeResult.Failed(outcome.error)
            } else {
                ComposeResult.Sent(outcome.emailId)
            }
        } catch (e: JmapException) {
            ComposeResult.Failed(e.message ?: "the message was not sent")
        } catch (t: Throwable) {
            ComposeResult.Failed("No connection - the message was not sent", offline = true)
        }
    }

    /**
     * The `Email` object a draft and a send both write (RFC 8621 section
     * 4.1.4): the two body alternatives, the `bodyStructure` the inline
     * images and attachments hang off, and the threading headers.
     */
    fun buildEmail(state: ComposeState, draftsMailboxId: String): JsonObject {
        val ready = state.attachments.filter { it.isReady }
        val bodyHtml = bodyForWire(state.bodyHtml, ready)
        val bodyText = HtmlText.toPlainText(bodyHtml)
        return buildJsonObject {
            putJsonObject("mailboxIds") { put(draftsMailboxId, true) }
            putJsonObject("keywords") {
                put(Keywords.DRAFT, true)
                put(Keywords.SEEN, true)
            }
            putJsonArray("from") {
                addJsonObject {
                    put("name", JsonPrimitive(state.identity?.name?.takeIf { it.isNotBlank() }))
                    put("email", state.identity?.email.orEmpty())
                }
            }
            putJsonArray("to") { state.to.forEach { add(it.toWire()) } }
            if (state.cc.isNotEmpty()) putJsonArray("cc") { state.cc.forEach { add(it.toWire()) } }
            if (state.bcc.isNotEmpty()) putJsonArray("bcc") { state.bcc.forEach { add(it.toWire()) } }
            put("subject", state.subject)
            putJsonObject("bodyValues") {
                putJsonObject("1") {
                    put("value", bodyText)
                    put("isTruncated", false)
                    put("isEncodingProblem", false)
                }
                putJsonObject("2") {
                    put("value", bodyHtml)
                    put("isTruncated", false)
                    put("isEncodingProblem", false)
                }
            }
            putJsonArray("textBody") {
                addJsonObject {
                    put("partId", "1")
                    put("type", "text/plain")
                    put("charset", "utf-8")
                }
            }
            putJsonArray("htmlBody") {
                addJsonObject {
                    put("partId", "2")
                    put("type", "text/html")
                    put("charset", "utf-8")
                }
            }
            put("bodyStructure", bodyStructure(ready))
            if (ready.isNotEmpty()) {
                putJsonArray("attachments") { ready.forEach { add(it.toPart()) } }
            }
            put("hasAttachment", ready.any { !it.inline })
            state.replyContext?.let { context ->
                if (context.inReplyTo.isNotEmpty()) {
                    putJsonArray("inReplyTo") { context.inReplyTo.forEach { add(it) } }
                }
                if (context.references.isNotEmpty()) {
                    putJsonArray("references") { context.references.forEach { add(it) } }
                }
            }
        }
    }

    /**
     * The editor writes inline images as the reading pane's inline scheme,
     * which the WebView resolves out of memory; the wire form is the
     * `cid:` URL the MIME part carries.
     */
    fun bodyForWire(bodyHtml: String, ready: List<ComposeAttachment>): String {
        var out = bodyHtml
        ready.filter { it.inline && it.cid != null }.forEach { attachment ->
            out = out.replace(HtmlSanitizer.INLINE_SCHEME + attachment.cid, "cid:${attachment.cid}")
        }
        return out
    }

    private fun bodyStructure(ready: List<ComposeAttachment>): JsonObject {
        val alternative = buildJsonObject {
            put("type", "multipart/alternative")
            putJsonArray("subParts") {
                addJsonObject {
                    put("partId", "1")
                    put("type", "text/plain")
                    put("charset", "utf-8")
                }
                addJsonObject {
                    put("partId", "2")
                    put("type", "text/html")
                    put("charset", "utf-8")
                }
            }
        }
        val inlines = ready.filter { it.inline }
        val attached = ready.filter { !it.inline }
        val related = if (inlines.isEmpty()) {
            alternative
        } else {
            buildJsonObject {
                put("type", "multipart/related")
                put(
                    "subParts",
                    buildJsonArray {
                        add(alternative)
                        inlines.forEach { add(it.toPart()) }
                    },
                )
            }
        }
        if (attached.isEmpty()) return related
        return buildJsonObject {
            put("type", "multipart/mixed")
            put(
                "subParts",
                buildJsonArray {
                    add(related)
                    attached.forEach { add(it.toPart()) }
                },
            )
        }
    }

    private fun roleMailbox(mailboxes: List<Mailbox>, accountId: String, role: String): String? =
        mailboxes.firstOrNull { it.accountId == accountId && it.role == role }?.id

    private companion object {
        fun randomToken(): String = (1..8)
            .map { "abcdefghijklmnopqrstuvwxyz0123456789".random() }
            .joinToString("")

        fun formatSize(bytes: Long): String = when {
            bytes >= 1024 * 1024 -> "${bytes / (1024 * 1024)} MB"
            bytes >= 1024 -> "${bytes / 1024} kB"
            else -> "$bytes bytes"
        }
    }
}

private fun com.netzhansa.herold.shared.domain.MailAddress.toWire(): JsonObject = buildJsonObject {
    put("name", JsonPrimitive(name?.takeIf { it.isNotBlank() }))
    put("email", email)
}

private fun ComposeAttachment.toPart(): JsonObject = buildJsonObject {
    put("blobId", blobId.orEmpty())
    put("type", type)
    put("name", name)
    put("size", size)
    put("disposition", if (inline) "inline" else "attachment")
    if (inline) put("cid", JsonPrimitive(cid))
}
