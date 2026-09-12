package com.netzhansa.herold.android.push

import android.content.Context
import android.util.Log
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.shared.push.MailNotification
import com.netzhansa.herold.shared.push.PushEnvelope
import com.netzhansa.herold.shared.push.mailNotification
import com.netzhansa.herold.shared.sync.SyncTypes
import kotlinx.coroutines.withTimeoutOrNull

/**
 * What a received push does on the device (architecture `04-push.md`): a
 * bounded reconcile pass through the sync engine so the local store holds
 * the new message, then the notification.
 *
 * The reconcile is bounded because the OS gives a woken messaging service a
 * short window; an unbounded pass would be killed mid-write. The
 * notification is posted whether or not the pass completed — the payload
 * carries everything it renders — so a slow network still produces a
 * notification, and opening it syncs the thread.
 */
class PushDelivery(private val context: Context) {

    /**
     * Handles one message's data map. Returns the notification that was
     * posted, or null when the payload was not a renderable mail event, the
     * user is already looking at the thread, or no session is held.
     */
    suspend fun deliver(data: Map<String, String>): MailNotification? {
        val envelope = PushEnvelope.fromData(data) ?: return null
        val container = (context.applicationContext as HeroldApplication).container
        container.restore()
        val session = container.session.value

        val accountId = envelope.accountId
        if (session != null && accountId != null) {
            val types = envelope.changedTypes.filter { it in SyncTypes.ALL }
                .ifEmpty { listOf(SyncTypes.EMAIL, SyncTypes.THREAD) }
            val outcome = withTimeoutOrNull(RECONCILE_BUDGET_MS) {
                session.syncEngine.syncAccount(accountId, types)
            }
            if (outcome == null) Log.w(TAG, "reconcile pass for $accountId exceeded its budget")
        }

        val notification = envelope.mailNotification() ?: return null
        if (ActiveThread.isShowing(notification.accountId, notification.threadId)) return null
        MailNotifier.post(context, notification)
        return notification
    }

    private companion object {
        const val TAG = "herold.push"

        /** The window a woken service reliably has for network work. */
        const val RECONCILE_BUDGET_MS = 15_000L
    }
}
