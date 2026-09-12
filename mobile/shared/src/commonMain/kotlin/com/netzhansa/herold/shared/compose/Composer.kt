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
import com.netzhansa.herold.shared.outbox.BlobSpool
import com.netzhansa.herold.shared.outbox.ComposePayload
import com.netzhansa.herold.shared.outbox.InMemoryBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxAddress
import com.netzhansa.herold.shared.outbox.OutboxAttachment
import com.netzhansa.herold.shared.outbox.OutboxKind
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

    /**
     * The message is in the durable outbox and will leave when the drain
     * reaches it: at once when connected and the hold has passed, later
     * otherwise (REQ-AND-SYNC-21).
     *
     * @param heldUntilMs the instant the drain may first submit it, which
     *   is the end of the undo window after Send (issue #354)
     */
    data class Queued(val entryId: Long, val heldUntilMs: Long = 0) : ComposeResult

    /** The compose cannot be sent as it stands; it stays open. */
    data class Failed(val message: String, val offline: Boolean = false) : ComposeResult
}

/** The outcome of adding a file to the compose. */
sealed interface AttachResult {
    data class Added(val attachment: ComposeAttachment) : AttachResult

    data class Rejected(val message: String) : AttachResult
}

/**
 * Compose's server side: it opens a compose from a parent message, takes
 * attachments in, writes the draft into the account's Drafts mailbox and
 * hands a send to the durable outbox.
 *
 * A send is one outbox entry that drains as uploads, `Email/set` and
 * `EmailSubmission/set` in that order (REQ-AND-SYNC-21), so composing and
 * sending work with no connectivity and the message leaves when there is
 * some. Attachments are copied into app-private storage as they are
 * picked, because the picker's grant on the chosen URI does not outlive
 * the compose.
 */
class Composer(
    private val api: JmapApi,
    private val outbox: Outbox? = null,
    private val spool: BlobSpool = InMemoryBlobSpool(),
    private val now: () -> Long = { 0L },
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
        // The copy comes first: the picker's grant on the chosen URI is
        // gone by the time a queued send drains.
        val handle = runCatching { spool.put(bytes, name) }.getOrNull()
        val cid = if (inline) newCid() else null
        val uploaded = try {
            api.uploadBlob(accountId, bytes, type, name)
        } catch (e: JmapException) {
            return AttachResult.Rejected(e.message ?: "the upload was rejected")
        } catch (t: Throwable) {
            if (handle == null) return AttachResult.Rejected("$name could not be kept for sending")
            return AttachResult.Added(
                ComposeAttachment(
                    key = handle,
                    name = name,
                    type = type,
                    size = bytes.size.toLong(),
                    status = AttachmentStatus.PENDING,
                    inline = inline,
                    cid = cid,
                    bytes = if (inline) bytes else null,
                    spool = handle,
                ),
            )
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
                cid = cid,
                bytes = if (inline) bytes else null,
                spool = handle,
            ),
        )
    }

    /**
     * Writes the compose into the account's Drafts mailbox: a create the
     * first time, an update of the same message after that (suite
     * REQ-DFT-02). With no connection the draft is queued instead, so what
     * was typed reaches the server's Drafts mailbox on reconnect rather
     * than being lost (REQ-AND-SYNC-21).
     */
    suspend fun saveDraft(state: ComposeState, mailboxes: List<Mailbox>): ComposeResult {
        val draftsId = roleMailbox(mailboxes, state.accountId, MailboxRoles.DRAFTS)
            ?: return ComposeResult.Failed("This account has no Drafts mailbox")
        if (state.attachments.any { it.status == AttachmentStatus.PENDING }) {
            return queueCompose(state, mailboxes, OutboxKind.DRAFT, draftLabel(state), 0)
        }
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
            queueCompose(state, mailboxes, OutboxKind.DRAFT, draftLabel(state), 0)
        }
    }

    /** Drops a draft the user discarded (suite REQ-DFT-42). */
    suspend fun discardDraft(accountId: String, draftId: String) {
        runCatching { api.emailDestroy(accountId, listOf(draftId)) }
    }

    /**
     * Hands the message to the durable outbox. It leaves as soon as the
     * drain reaches it - after [holdMs], the undo window the user can take
     * the send back in (issue #354), and once there is a connection.
     */
    suspend fun send(state: ComposeState, mailboxes: List<Mailbox>, holdMs: Long = 0): ComposeResult {
        if (state.identity == null) return ComposeResult.Failed("Choose an address to send from")
        if (!state.hasRecipient) return ComposeResult.Failed("Add at least one recipient")
        if (state.uploading) return ComposeResult.Failed("Wait for the attachments to finish uploading")
        return queueCompose(state, mailboxes, OutboxKind.SEND, sendLabel(state), holdMs)
    }

    /** The one path a draft save and a send both queue through. */
    private suspend fun queueCompose(
        state: ComposeState,
        mailboxes: List<Mailbox>,
        kind: OutboxKind,
        label: String,
        holdMs: Long,
    ): ComposeResult {
        val queue = outbox ?: return ComposeResult.Failed("No connection - the message was not sent", offline = true)
        val identity = state.identity ?: return ComposeResult.Failed("Choose an address to send from")
        val draftsId = roleMailbox(mailboxes, state.accountId, MailboxRoles.DRAFTS)
            ?: return ComposeResult.Failed("This account has no Drafts mailbox")
        // A send supersedes the draft save this compose queued earlier, so
        // the two do not each create their own message.
        state.draftEntryId?.let { queue.cancelIfQueued(it) }
        val heldUntil = if (holdMs > 0) now() + holdMs else 0
        val entryId = queue.enqueueCompose(
            kind = kind,
            label = label,
            payload = payloadOf(state, identity, draftsId, roleMailbox(mailboxes, state.accountId, MailboxRoles.SENT)),
            holdUntilMs = heldUntil,
        )
        return ComposeResult.Queued(entryId, heldUntil)
    }

    /** The compose as the outbox entry carries it. */
    fun payloadOf(
        state: ComposeState,
        identity: Identity,
        draftsMailboxId: String,
        sentMailboxId: String?,
    ): ComposePayload {
        val parent = state.replyContext?.takeIf { it.accountId == state.accountId }
        return ComposePayload(
            accountId = state.accountId,
            identityId = identity.id,
            identityName = identity.name,
            identityEmail = identity.email,
            to = state.to.map { OutboxAddress(it.name, it.email) },
            cc = state.cc.map { OutboxAddress(it.name, it.email) },
            bcc = state.bcc.map { OutboxAddress(it.name, it.email) },
            subject = state.subject,
            bodyHtml = state.bodyHtml,
            attachments = state.attachments.filter { it.isCarried }.map {
                OutboxAttachment(
                    name = it.name,
                    type = it.type,
                    size = it.size,
                    inline = it.inline,
                    cid = it.cid,
                    blobId = it.blobId,
                    spool = it.spool,
                )
            },
            draftsMailboxId = draftsMailboxId,
            sentMailboxId = sentMailboxId,
            draftId = state.draftId,
            parentId = parent?.parentId,
            parentKeyword = parent?.parentKeyword,
            inReplyTo = parent?.inReplyTo ?: emptyList(),
            references = parent?.references ?: emptyList(),
        )
    }

    private fun sendLabel(state: ComposeState): String =
        "Send: " + state.subject.ifBlank { "(no subject)" }

    private fun draftLabel(state: ComposeState): String =
        "Draft: " + state.subject.ifBlank { "(no subject)" }

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
