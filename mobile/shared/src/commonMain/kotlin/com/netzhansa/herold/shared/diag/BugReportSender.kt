package com.netzhansa.herold.shared.diag

import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.compose.IdentityChoice
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import com.netzhansa.herold.shared.outbox.BlobSpool
import com.netzhansa.herold.shared.outbox.ComposePayload
import com.netzhansa.herold.shared.outbox.MailboxPayload
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxAddress
import com.netzhansa.herold.shared.outbox.OutboxAttachment
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * Hands a bug report to the durable outbox as mail to the user's own
 * address (REQ-AND-SYS-53). It is an ordinary queued send: the bundle's
 * parts are spooled like any attachment, the entry waits out the undo
 * window, and it leaves when there is a connection - so a report written
 * on a train goes out when the train arrives.
 *
 * Ahead of the send it makes sure the account holds the "Bug reports"
 * label, creating it through the same outbox `Mailbox/set` path a
 * category's label is created by. The label is enqueued first, so it
 * exists by the time the send drains and the sent copy can be filed
 * under it.
 */
class BugReportSender(
    private val store: LocalStore,
    private val outbox: Outbox,
    private val spool: BlobSpool,
    private val now: () -> Long = { 0L },
) {
    /**
     * Queues [bundle] for [accountId]. [holdMs] is the undo window the
     * entry waits out before the drain may submit it.
     */
    suspend fun queue(bundle: BugBundle, accountId: String, holdMs: Long = 0): ComposeResult {
        val accounts = store.accountList()
        val identities = store.identities().first()
        val identity = IdentityChoice.defaultForNew(identities, accounts, accountId)
            ?: return ComposeResult.Failed("This account has no address to send from")
        val mailboxes = store.mailboxList()
        val account = identity.accountId
        val drafts = mailboxes.firstOrNull { it.accountId == account && it.role == MailboxRoles.DRAFTS }?.id
            ?: return ComposeResult.Failed("This account has no Drafts mailbox")
        val sent = mailboxes.firstOrNull { it.accountId == account && it.role == MailboxRoles.SENT }?.id

        ensureLabel(account, mailboxes)

        val attachments = bundle.files.map { file ->
            OutboxAttachment(
                name = file.name,
                type = file.type,
                size = file.bytes.size.toLong(),
                spool = spool.put(file.bytes, file.name),
            )
        }
        val payload = ComposePayload(
            accountId = account,
            identityId = identity.id,
            identityName = identity.name,
            identityEmail = identity.email,
            to = listOf(OutboxAddress(identity.name.takeIf { it.isNotBlank() }, identity.email)),
            subject = bundle.subject,
            bodyHtml = HtmlSanitizer.fromPlainText(bundle.bodyText),
            attachments = attachments,
            draftsMailboxId = drafts,
            sentMailboxId = sent,
            sentLabels = listOf(BugBundleWriter.LABEL),
            sentUnread = true,
        )
        val heldUntil = if (holdMs > 0) now() + holdMs else 0
        val entryId = outbox.enqueueCompose(
            kind = OutboxKind.SEND,
            label = "Send: " + bundle.subject,
            payload = payload,
            holdUntilMs = heldUntil,
        )
        return ComposeResult.Queued(entryId, heldUntil)
    }

    /**
     * Queues the label's creation when the account does not hold it. A
     * placeholder row stands in until the drain brings the server's own,
     * so a second report queued before the first drains finds the label
     * and does not ask for it twice.
     */
    private suspend fun ensureLabel(accountId: String, mailboxes: List<Mailbox>) {
        val held = mailboxes.any {
            it.accountId == accountId && it.name.equals(BugBundleWriter.LABEL, ignoreCase = true)
        }
        if (held) return
        val placeholder = placeholderId(accountId)
        store.upsertMailboxes(
            listOf(Mailbox(accountId = accountId, id = placeholder, name = BugBundleWriter.LABEL)),
        )
        outbox.enqueueMailbox(
            accountId = accountId,
            label = "Create the \"${BugBundleWriter.LABEL}\" label",
            payload = MailboxPayload(
                accountId = accountId,
                creates = mapOf(placeholder to buildJsonObject { put("name", BugBundleWriter.LABEL) }),
            ),
        )
    }

    companion object {
        /** The store id the label carries until the server's own row arrives. */
        fun placeholderId(accountId: String): String = "pending-label:$accountId:${BugBundleWriter.LABEL}"
    }
}
