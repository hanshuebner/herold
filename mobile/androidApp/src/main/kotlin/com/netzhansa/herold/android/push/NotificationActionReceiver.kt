package com.netzhansa.herold.android.push

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.shared.actions.ActionResult
import com.netzhansa.herold.shared.push.MailNotification
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

/**
 * Archive and Mark Read from the shade (REQ-AND-PUSH-20). Both run the same
 * optimistic action path the inbox uses, so the local store reflects the
 * change immediately and an `Email/set` carries it to the server; the
 * notification is withdrawn either way, and a rejected change reverts in the
 * store as it does on screen.
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
            try {
                apply(app, action, accountId, emailId)
            } catch (t: Throwable) {
                Log.w(TAG, "notification action $action failed: ${t.message}")
            } finally {
                tag?.let { MailNotifier.cancel(context, it) }
                pending.finish()
            }
        }
    }

    private suspend fun apply(
        app: HeroldApplication,
        action: String,
        accountId: String,
        emailId: String,
    ) {
        val container = app.container
        container.restore()
        val session = container.session.value ?: return
        val email = container.store.email(accountId, emailId) ?: return
        val result = when (action) {
            ACTION_ARCHIVE -> session.actions.archive(
                listOf(email),
                container.store.mailboxList(),
            ).first

            ACTION_MARK_READ -> session.actions.setSeen(listOf(email), true)
            else -> return
        }
        if (result is ActionResult.Reverted) {
            Log.w(TAG, "notification action $action reverted: ${result.message}")
        }
    }

    companion object {
        const val ACTION_ARCHIVE = "com.netzhansa.herold.android.action.ARCHIVE"
        const val ACTION_MARK_READ = "com.netzhansa.herold.android.action.MARK_READ"
        const val EXTRA_EMAIL_ID = "com.netzhansa.herold.android.extra.EMAIL_ID"
        const val EXTRA_TAG = "com.netzhansa.herold.android.extra.TAG"

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
            }
    }
}
