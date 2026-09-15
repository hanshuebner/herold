package com.netzhansa.herold.android.work

import android.content.Context
import android.util.Log
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.Data
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.android.push.MailNotifier
import com.netzhansa.herold.android.push.ReplyStatus
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.push.MailNotification
import java.util.concurrent.TimeUnit

/**
 * Carries a reply typed in the notification shade to the server and
 * reports it back to the shade (REQ-AND-PUSH-21).
 *
 * The job is what outlives the broadcast receiver that queued the reply:
 * it starts when the undo window is up, so a reply taken back never
 * leaves, and it runs with the app closed (REQ-AND-SYNC-31). Each pass
 * drains the outbox and then reads the entry: gone means the server has
 * the message, a failed entry carries the refusal, and an entry still
 * queued means the send is waiting for a connection or a backoff, so the
 * job comes back for it.
 */
class ReplySendWorker(
    context: Context,
    params: WorkerParameters,
) : CoroutineWorker(context, params) {

    override suspend fun doWork(): Result {
        val entryId = inputData.getLong(KEY_ENTRY_ID, 0)
        if (entryId <= 0) return Result.success()
        val container = (applicationContext as HeroldApplication).container
        container.restore()
        val session = container.session.value ?: return Result.success()
        // With unlock on, the token is not released until the user has
        // authenticated; the reply waits in the queue and the shade keeps
        // showing it as sending (REQ-AND-AUTH-11).
        if (!container.unlock.isUnlocked()) return Result.retry()

        val notification = notificationOf(inputData)
        val text = inputData.getString(KEY_TEXT).orEmpty()
        // Nothing left to submit: an earlier drain already took it.
        if (container.outbox.entry(entryId) == null) return sent(notification, text, session)

        return try {
            session.syncEngine.drainOutbox()
            when (val entry = container.outbox.entry(entryId)) {
                null -> sent(notification, text, session)

                else -> if (entry.state == OutboxState.FAILED) {
                    MailNotifier.postReplyStatus(
                        context = applicationContext,
                        notification = notification,
                        status = ReplyStatus.FAILED,
                        text = text,
                        error = entry.lastError,
                    )
                    Result.success()
                } else {
                    Result.retry()
                }
            }
        } catch (t: Throwable) {
            Log.w(TAG, "reply drain failed: ${t.message}")
            Result.retry()
        }
    }

    /** The reply is on the server; the shade says so and the Sent copy is fetched. */
    private suspend fun sent(
        notification: MailNotification,
        text: String,
        session: SessionScope,
    ): Result {
        MailNotifier.postReplyStatus(
            context = applicationContext,
            notification = notification,
            status = ReplyStatus.SENT,
            text = text,
        )
        runCatching { session.syncEngine.syncAll() }
        return Result.success()
    }

    private fun notificationOf(data: Data): MailNotification {
        val accountId = data.getString(KEY_ACCOUNT_ID).orEmpty()
        val threadId = data.getString(KEY_THREAD_ID).orEmpty()
        return MailNotification(
            accountId = accountId,
            threadId = threadId,
            emailId = data.getString(KEY_EMAIL_ID),
            inboxMailboxId = null,
            senderName = data.getString(KEY_SENDER_NAME).orEmpty(),
            senderAddress = data.getString(KEY_SENDER_ADDRESS).orEmpty(),
            subject = data.getString(KEY_SUBJECT).orEmpty(),
            preview = "",
            tag = data.getString(KEY_TAG) ?: MailNotification.tagFor(accountId, threadId),
            groupKey = MailNotification.groupFor(accountId),
        )
    }

    companion object {
        private const val TAG = "herold.reply"

        private const val KEY_ENTRY_ID = "entryId"
        private const val KEY_TEXT = "text"
        private const val KEY_ACCOUNT_ID = "accountId"
        private const val KEY_THREAD_ID = "threadId"
        private const val KEY_EMAIL_ID = "emailId"
        private const val KEY_SENDER_NAME = "senderName"
        private const val KEY_SENDER_ADDRESS = "senderAddress"
        private const val KEY_SUBJECT = "subject"
        private const val KEY_TAG = "tag"

        private const val BACKOFF_SECONDS = 15L

        /** One job per queued reply, so two replies do not displace each other. */
        fun workName(entryId: Long): String = "herold-reply-$entryId"

        /**
         * Schedules the reply's drain for the end of its undo window
         * [holdMs], as soon as there is a connection.
         */
        fun schedule(
            context: Context,
            notification: MailNotification,
            entryId: Long,
            text: String,
            holdMs: Long,
        ) {
            val data = Data.Builder()
                .putLong(KEY_ENTRY_ID, entryId)
                .putString(KEY_TEXT, text)
                .putString(KEY_ACCOUNT_ID, notification.accountId)
                .putString(KEY_THREAD_ID, notification.threadId)
                .putString(KEY_EMAIL_ID, notification.emailId)
                .putString(KEY_SENDER_NAME, notification.senderName)
                .putString(KEY_SENDER_ADDRESS, notification.senderAddress)
                .putString(KEY_SUBJECT, notification.subject)
                .putString(KEY_TAG, notification.tag)
                .build()
            val request = OneTimeWorkRequestBuilder<ReplySendWorker>()
                .setConstraints(
                    Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build(),
                )
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, BACKOFF_SECONDS, TimeUnit.SECONDS)
                .setInputData(data)
                .apply { if (holdMs > 0) setInitialDelay(holdMs, TimeUnit.MILLISECONDS) }
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(workName(entryId), ExistingWorkPolicy.REPLACE, request)
        }

        /** Drops the job of a reply the user took back. */
        fun cancel(context: Context, entryId: Long) {
            WorkManager.getInstance(context.applicationContext).cancelUniqueWork(workName(entryId))
        }
    }
}
