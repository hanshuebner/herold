package com.netzhansa.herold.android.push

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log
import androidx.core.app.RemoteInput
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.ui.settings.UndoSendPreference
import com.netzhansa.herold.android.work.ReplySendWorker
import com.netzhansa.herold.shared.compose.ComposeMode
import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import com.netzhansa.herold.shared.push.MailNotification
import com.netzhansa.herold.shared.store.LocalStore
import com.netzhansa.herold.shared.sync.SyncTypes
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

/**
 * The shade's actions (REQ-AND-PUSH-20/21), all applied without opening
 * the app.
 *
 * Archive and Mark Read run the same optimistic action path the inbox
 * uses: the local store reflects the change immediately, the outbox
 * carries it, and the drain the receiver runs puts it on the wire. Reply
 * takes the text the platform collected in the notification's
 * `RemoteInput`, builds the reply the composer would have built, and
 * hands it to the outbox; the notification follows it from "Sending" to
 * "Reply sent" or to the refusal. With no connection the change waits in
 * the queue rather than being lost.
 */
class NotificationActionReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        val action = intent.action ?: return
        val accountId = intent.getStringExtra(MailNotifier.EXTRA_ACCOUNT_ID) ?: return
        val emailId = intent.getStringExtra(EXTRA_EMAIL_ID) ?: return
        val tag = intent.getStringExtra(EXTRA_TAG)
        val pending = goAsync()
        val app = context.applicationContext as HeroldApplication
        CoroutineScope(Dispatchers.IO).launch {
            var withdraw = true
            try {
                withdraw = apply(app, context, action, accountId, emailId, intent)
            } catch (t: Throwable) {
                Log.w(TAG, "notification action $action failed: ${t.message}")
            } finally {
                // A reply keeps the thread's notification: it is what
                // reports the send. Everything else resolved the message,
                // so the shade drops it.
                if (withdraw) tag?.let { MailNotifier.cancel(context, it) }
                pending.finish()
            }
        }
    }

    /** Runs the action; returns true when the notification is to be withdrawn. */
    private suspend fun apply(
        app: HeroldApplication,
        context: Context,
        action: String,
        accountId: String,
        emailId: String,
        intent: Intent,
    ): Boolean {
        val container = app.container
        container.restore()
        val session = container.session.value ?: return true
        if (action == ACTION_UNDO_REPLY) return undoReply(app, context, intent)
        val email = container.store.email(accountId, emailId)
            ?: session.syncEngine.loadBody(accountId, emailId)
            ?: return true
        when (action) {
            ACTION_ARCHIVE -> session.actions.archive(listOf(email), container.store.mailboxList())
            ACTION_MARK_READ -> session.actions.setSeen(listOf(email), true)
            ACTION_REPLY -> return reply(app, context, session, email, intent)
            else -> return true
        }
        // The action is in the store and in the outbox; the drain here is
        // what carries it to the server while the receiver is still alive.
        val outcome = session.syncEngine.drainOutbox()
        if (outcome.rejected > 0) Log.w(TAG, "notification action $action was refused")
        return true
    }

    /**
     * The reply the user typed in the shade. It is built the way the
     * composer builds one - the thread's reply recipients, the reply
     * subject, `inReplyTo`/`references` and the quoted original, sent from
     * the identity of the account that received the message - with the
     * typed text as its body, and handed to the outbox under the undo
     * window the user chose. A blank reply is nothing to send, so it opens
     * the composer instead.
     */
    private suspend fun reply(
        app: HeroldApplication,
        context: Context,
        session: SessionScope,
        parent: Email,
        intent: Intent,
    ): Boolean {
        val notification = notificationOf(intent, parent)
        val text = RemoteInput.getResultsFromIntent(intent)
            ?.getCharSequence(MailNotifier.REPLY_RESULT_KEY)?.toString()?.trim()
        if (text.isNullOrBlank()) {
            MailNotifier.replyIntent(context, notification).send()
            return false
        }

        val container = app.container
        val identities = identitiesFor(session, container.store, parent.accountId)
        val accounts = container.store.accountList()
        val mailboxes = container.store.mailboxList().ifEmpty {
            session.syncEngine.syncAccount(parent.accountId, listOf(SyncTypes.MAILBOX))
            container.store.mailboxList()
        }
        val prepared = session.composer.openFrom(
            mode = ComposeMode.REPLY,
            parent = parent,
            identities = identities,
            accounts = accounts,
            sentAtLabel = null,
        )
        // The typed text goes above the quote the composer prepared, as
        // plain text: the shade has no editor, so the reply carries no
        // formatting of its own.
        val state = prepared.copy(bodyHtml = HtmlSanitizer.fromPlainText(text) + prepared.bodyHtml)
        val hold = UndoSendPreference.current(context).millis
        return when (val result = session.composer.send(state, mailboxes, hold)) {
            is ComposeResult.Queued -> {
                MailNotifier.postReplyStatus(
                    context = context,
                    notification = notification,
                    status = ReplyStatus.SENDING,
                    text = text,
                    entryId = result.entryId,
                )
                // The drain runs as a job, so the send leaves and the
                // notification is resolved with the app closed and after
                // the hold outlasts this receiver (REQ-AND-SYNC-31).
                ReplySendWorker.schedule(context, notification, result.entryId, text, hold)
                false
            }

            is ComposeResult.Failed -> {
                MailNotifier.postReplyStatus(
                    context = context,
                    notification = notification,
                    status = ReplyStatus.FAILED,
                    text = text,
                    error = result.message,
                )
                false
            }

            else -> false
        }
    }

    /**
     * Takes back a reply still inside its undo window: the queued send is
     * dropped and the message's own notification goes back into the shade,
     * where replying again starts from the message.
     */
    private suspend fun undoReply(app: HeroldApplication, context: Context, intent: Intent): Boolean {
        val entryId = intent.getLongExtra(EXTRA_ENTRY_ID, 0)
        if (entryId > 0) {
            ReplySendWorker.cancel(context, entryId)
            app.container.outbox.cancelIfQueued(entryId)
        }
        MailNotifier.post(context, notificationOf(intent, null))
        return false
    }

    /**
     * The notification the action came from, rebuilt from what the intent
     * carries so the status notification lands on the same tag and the
     * same account bundle as the message's own.
     */
    private fun notificationOf(intent: Intent, parent: Email?): MailNotification {
        val accountId = intent.getStringExtra(MailNotifier.EXTRA_ACCOUNT_ID).orEmpty()
        val threadId = intent.getStringExtra(MailNotifier.EXTRA_THREAD_ID).orEmpty()
        return MailNotification(
            accountId = accountId,
            threadId = threadId,
            emailId = intent.getStringExtra(EXTRA_EMAIL_ID),
            inboxMailboxId = null,
            senderName = intent.getStringExtra(EXTRA_SENDER_NAME)
                ?: parent?.senderDisplay.orEmpty(),
            senderAddress = intent.getStringExtra(EXTRA_SENDER_ADDRESS)
                ?: parent?.fromEmail.orEmpty(),
            subject = intent.getStringExtra(EXTRA_SUBJECT) ?: parent?.subject.orEmpty(),
            preview = "",
            tag = intent.getStringExtra(EXTRA_TAG) ?: MailNotification.tagFor(accountId, threadId),
            groupKey = MailNotification.groupFor(accountId),
        )
    }

    /**
     * The account's identities. A push that woke a cold process can find
     * the store without them, and a reply with no identity has no address
     * to come from, so they are fetched before the reply is built.
     */
    private suspend fun identitiesFor(
        session: SessionScope,
        store: LocalStore,
        accountId: String,
    ): List<Identity> {
        val held = store.identities().first()
        if (held.any { it.accountId == accountId }) return held
        session.syncEngine.syncAccount(accountId, listOf(SyncTypes.IDENTITY))
        return store.identities().first()
    }

    companion object {
        const val ACTION_ARCHIVE = "com.netzhansa.herold.android.action.ARCHIVE"
        const val ACTION_MARK_READ = "com.netzhansa.herold.android.action.MARK_READ"

        /** The inline reply the shade collected (REQ-AND-PUSH-21). */
        const val ACTION_REPLY = "com.netzhansa.herold.android.action.REPLY"

        /** Takes a queued inline reply back while it is still held. */
        const val ACTION_UNDO_REPLY = "com.netzhansa.herold.android.action.UNDO_REPLY"

        const val EXTRA_EMAIL_ID = "com.netzhansa.herold.android.extra.EMAIL_ID"
        const val EXTRA_TAG = "com.netzhansa.herold.android.extra.TAG"
        const val EXTRA_SENDER_NAME = "com.netzhansa.herold.android.extra.SENDER_NAME"
        const val EXTRA_SENDER_ADDRESS = "com.netzhansa.herold.android.extra.SENDER_ADDRESS"
        const val EXTRA_SUBJECT = "com.netzhansa.herold.android.extra.SUBJECT"

        /** The outbox entry an undo drops. */
        const val EXTRA_ENTRY_ID = "com.netzhansa.herold.android.extra.ENTRY_ID"

        private const val TAG = "herold.push"

        fun intent(context: Context, action: String, notification: MailNotification): Intent =
            Intent(context, NotificationActionReceiver::class.java).apply {
                this.action = action
                // Distinct per thread and action, so the PendingIntents the
                // shade holds do not collapse onto one another.
                data = Uri.parse(
                    "herold://action/$action/${notification.accountId}/${notification.threadId}",
                )
                putExtra(MailNotifier.EXTRA_ACCOUNT_ID, notification.accountId)
                putExtra(MailNotifier.EXTRA_THREAD_ID, notification.threadId)
                putExtra(EXTRA_EMAIL_ID, notification.emailId)
                putExtra(EXTRA_TAG, notification.tag)
                putExtra(EXTRA_SENDER_NAME, notification.senderName)
                putExtra(EXTRA_SENDER_ADDRESS, notification.senderAddress)
                putExtra(EXTRA_SUBJECT, notification.subject)
            }
    }
}
