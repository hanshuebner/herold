package com.netzhansa.herold.android.work

import android.content.Context
import android.util.Log
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.netzhansa.herold.android.HeroldApplication
import java.util.concurrent.TimeUnit

/**
 * Drains the outbox with the app closed (REQ-AND-SYNC-31). The job's
 * network constraint is what makes queued mail leave when connectivity
 * returns rather than when the user next opens the app; an FCM wake
 * schedules it too, so a push that reconciles also carries out whatever
 * was waiting.
 *
 * The pass ends in `retry` while entries remain - a send still inside
 * its undo window, or one whose transient failure has not backed off
 * yet - so WorkManager brings the job back rather than the queue
 * sitting until the next user action.
 */
class OutboxWorker(
    context: Context,
    params: WorkerParameters,
) : CoroutineWorker(context, params) {

    override suspend fun doWork(): Result {
        val container = (applicationContext as HeroldApplication).container
        container.restore()
        val session = container.session.value ?: return Result.success()
        // With unlock on, the token is not released until the user has
        // authenticated; the queue waits for the next foreground rather
        // than draining behind a locked app (REQ-AND-AUTH-11).
        if (!container.unlock.isUnlocked()) return Result.retry()
        return try {
            val outcome = session.syncEngine.drainOutbox()
            if (outcome.submitted > 0) session.syncEngine.syncAll()
            if (outcome.hasMore) Result.retry() else Result.success()
        } catch (t: Throwable) {
            Log.w(TAG, "outbox drain failed: ${t.message}")
            Result.retry()
        }
    }

    companion object {
        private const val TAG = "herold.outbox"

        /** One drain job at a time, whoever asks for it. */
        const val WORK_NAME = "herold-outbox-drain"

        /**
         * Schedules a drain for as soon as there is a connection.
         * [delayMs] holds it back, which is how a send waits out its
         * undo window even if the app is closed in the meantime
         * (issue #354).
         */
        fun schedule(context: Context, delayMs: Long = 0) {
            val request = OneTimeWorkRequestBuilder<OutboxWorker>()
                .setConstraints(
                    Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build(),
                )
                .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, BACKOFF_SECONDS, TimeUnit.SECONDS)
                .apply { if (delayMs > 0) setInitialDelay(delayMs, TimeUnit.MILLISECONDS) }
                .build()
            WorkManager.getInstance(context.applicationContext)
                .enqueueUniqueWork(WORK_NAME, ExistingWorkPolicy.REPLACE, request)
        }

        private const val BACKOFF_SECONDS = 15L
    }
}
