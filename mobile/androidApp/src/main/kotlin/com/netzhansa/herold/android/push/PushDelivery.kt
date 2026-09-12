package com.netzhansa.herold.android.push

import android.content.Context
import android.util.Log
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.media.ImageScaling
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.push.MailNotification
import com.netzhansa.herold.shared.push.PushEnvelope
import com.netzhansa.herold.shared.push.PushVerification
import com.netzhansa.herold.shared.push.mailNotification
import com.netzhansa.herold.android.work.OutboxWorker
import com.netzhansa.herold.shared.sync.SyncTypes
import kotlinx.coroutines.withTimeoutOrNull

/**
 * What a received push does on the device (architecture `04-push.md`): a
 * bounded reconcile pass through the sync engine so the local store holds
 * the new message, then the notification.
 *
 * The reconcile is bounded because the OS gives a woken messaging service a
 * short window; an unbounded pass would be killed mid-write. The same
 * window pays for what the shade shows beyond the payload - the sender's
 * picture and the message's attachments (issue #348). The notification is
 * posted whether or not the pass completed - the payload carries everything
 * it needs to render - so a slow network still produces a notification, and
 * opening it syncs the thread.
 */
class PushDelivery(private val context: Context) {

    /**
     * Handles one message's data map. Returns the notification that was
     * posted, or null when the payload was not a renderable mail event, the
     * user is already looking at the thread, or no session is held.
     */
    suspend fun deliver(data: Map<String, String>): MailNotification? {
        val container = (context.applicationContext as HeroldApplication).container
        container.restore()
        val session = container.session.value

        // The RFC 8620 section 7.2.2 handshake arrives under its own data
        // key and renders nothing: echoing the code back is what makes
        // herold deliver anything else to this subscription.
        PushVerification.fromData(data)?.let { handshake ->
            val registrar = session?.pushRegistrar ?: return null
            registrar.confirmVerification(handshake.subscriptionId, handshake.code)
            return null
        }

        // The wake is also the queue's chance to go out with the app
        // closed (REQ-AND-SYNC-31).
        if (session != null) OutboxWorker.schedule(context)

        val envelope = PushEnvelope.fromData(data) ?: return null
        val notification = envelope.mailNotification()

        val accountId = envelope.accountId
        var presentation = MailPresentation()
        if (session != null && accountId != null) {
            val types = envelope.changedTypes.filter { it in SyncTypes.ALL }
                .ifEmpty { listOf(SyncTypes.EMAIL, SyncTypes.THREAD) }
            val outcome = withTimeoutOrNull(RECONCILE_BUDGET_MS) {
                session.syncEngine.syncAccount(accountId, types)
                if (notification != null) presentation = present(session, container, notification)
            }
            if (outcome == null) Log.w(TAG, "reconcile pass for $accountId exceeded its budget")
        }

        if (notification == null) return null
        if (ActiveThread.isShowing(notification.accountId, notification.threadId)) return null
        MailNotifier.post(context, notification, presentation)
        return notification
    }

    /**
     * What the shade shows around the payload's text: the account the mail
     * arrived for, the sender's picture, and the message's attachments. The
     * attachment list comes from the same `Email/get` that caches the body,
     * so opening the notification finds the message already loaded.
     */
    private suspend fun present(
        session: SessionScope,
        container: AppContainer,
        notification: MailNotification,
    ): MailPresentation {
        val accountId = notification.accountId
        // The group header is the account the mail arrived for, which is
        // what tells two accounts' bundles apart in the shade.
        val accountLabel = runCatching { container.store.accountList() }.getOrNull()
            ?.firstOrNull { it.id == accountId }?.name.orEmpty()

        val avatarBytes = runCatching {
            session.syncEngine.senderAvatar(accountId, notification.senderAddress)
        }.getOrNull()
        val avatar = SenderAvatars.of(
            bytes = avatarBytes,
            name = notification.senderName,
            address = notification.senderAddress,
            sizePx = MailNotifier.largeIconSize(context),
        )

        val attachments = notification.emailId?.let { emailId ->
            runCatching { session.syncEngine.loadBody(accountId, emailId) }.getOrNull()
                ?.attachments.orEmpty().filter { !it.isInline }
        }.orEmpty()

        return MailPresentation(
            accountLabel = accountLabel,
            avatar = avatar,
            attachments = attachments.map { chip(session, accountId, it) },
        )
    }

    /** One chip: the file's name, with a thumbnail when the part is an image. */
    private suspend fun chip(
        session: SessionScope,
        accountId: String,
        attachment: Attachment,
    ): AttachmentChip {
        if (!ImageScaling.isImage(attachment.type)) return AttachmentChip(attachment.name)
        val bytes = runCatching {
            session.syncEngine.blob(accountId, attachment.blobId, attachment.type, attachment.name)
        }.getOrNull() ?: return AttachmentChip(attachment.name)
        return AttachmentChip(attachment.name, ImageScaling.decodeSampled(bytes, CHIP_THUMBNAIL_PX))
    }

    private companion object {
        const val TAG = "herold.push"

        /** The window a woken service reliably has for network work. */
        const val RECONCILE_BUDGET_MS = 15_000L

        /** A chip's thumbnail is 20 dp on screen; this covers it on any density. */
        const val CHIP_THUMBNAIL_PX = 96
    }
}
