package com.netzhansa.herold.android.push

import android.annotation.SuppressLint
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import com.netzhansa.herold.android.MainActivity
import com.netzhansa.herold.shared.push.MailNotification
import com.netzhansa.herold.shared.push.PushChannels

/**
 * Posts and withdraws mail notifications (REQ-AND-PUSH-11/12/13/20).
 *
 * One notification per thread, tagged with the thread's key so a second
 * message on the same thread replaces it rather than stacking; every
 * notification of one account carries that account's group, under a summary
 * so the shade shows one bundled entry. Tapping the body deep-links into the
 * thread; Archive and Mark Read run without opening the app.
 */
object MailNotifier {

    /** Extras the tap intent carries into [MainActivity]. */
    const val EXTRA_ACCOUNT_ID = "com.netzhansa.herold.android.extra.ACCOUNT_ID"
    const val EXTRA_THREAD_ID = "com.netzhansa.herold.android.extra.THREAD_ID"

    private const val CHILD_ID = 1
    private const val SUMMARY_ID = 2
    private const val SMALL_ICON = android.R.drawable.stat_notify_chat

    @SuppressLint("MissingPermission")
    fun post(context: Context, notification: MailNotification) {
        val manager = NotificationManagerCompat.from(context)
        if (!manager.areNotificationsEnabled()) return
        manager.notify(notification.tag, CHILD_ID, build(context, notification))
        manager.notify(notification.groupKey, SUMMARY_ID, buildSummary(context, notification))
    }

    /**
     * Withdraws a thread's notification, after an action resolved it or the
     * user opened the thread, and takes the account's summary with it once
     * it has no children left - a summary alone in the shade is noise.
     */
    fun cancel(context: Context, tag: String) {
        val manager = NotificationManagerCompat.from(context)
        manager.cancel(tag, CHILD_ID)
        val system = context.getSystemService(NotificationManager::class.java) ?: return
        val childrenLeft = system.activeNotifications.any {
            it.id == CHILD_ID && it.tag != tag
        }
        if (!childrenLeft) {
            system.activeNotifications.filter { it.id == SUMMARY_ID }
                .forEach { manager.cancel(it.tag, SUMMARY_ID) }
        }
    }

    private fun build(context: Context, notification: MailNotification): android.app.Notification =
        NotificationCompat.Builder(context, PushChannels.MAIL)
            .setSmallIcon(SMALL_ICON)
            .setContentTitle(notification.title)
            .setContentText(notification.body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(notification.body))
            .setCategory(NotificationCompat.CATEGORY_EMAIL)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setGroup(notification.groupKey)
            // The children alert; the summary is only the bundle's header,
            // so it must not raise a second heads-up of its own.
            .setGroupAlertBehavior(NotificationCompat.GROUP_ALERT_CHILDREN)
            .setAutoCancel(true)
            .setContentIntent(openThreadIntent(context, notification))
            .addAction(
                NotificationCompat.Action.Builder(
                    android.R.drawable.ic_menu_delete,
                    ARCHIVE_TITLE,
                    actionIntent(context, NotificationActionReceiver.ACTION_ARCHIVE, notification),
                ).build(),
            )
            .addAction(
                NotificationCompat.Action.Builder(
                    android.R.drawable.ic_menu_view,
                    MARK_READ_TITLE,
                    actionIntent(context, NotificationActionReceiver.ACTION_MARK_READ, notification),
                ).build(),
            )
            .build()

    private fun buildSummary(context: Context, notification: MailNotification): android.app.Notification =
        NotificationCompat.Builder(context, PushChannels.MAIL)
            .setSmallIcon(SMALL_ICON)
            .setContentTitle(SUMMARY_TITLE)
            .setGroup(notification.groupKey)
            .setGroupSummary(true)
            .setGroupAlertBehavior(NotificationCompat.GROUP_ALERT_CHILDREN)
            .setAutoCancel(true)
            .setStyle(NotificationCompat.InboxStyle().setSummaryText(SUMMARY_TITLE))
            .build()

    /** The tap target: the shell, routed at the thread the push named. */
    fun openThreadIntent(context: Context, notification: MailNotification): PendingIntent {
        val intent = Intent(context, MainActivity::class.java).apply {
            action = Intent.ACTION_VIEW
            // A distinct data URI per thread keeps the PendingIntents distinct,
            // so two threads do not share one tap target.
            data = Uri.parse("herold://thread/${notification.accountId}/${notification.threadId}")
            putExtra(EXTRA_ACCOUNT_ID, notification.accountId)
            putExtra(EXTRA_THREAD_ID, notification.threadId)
            flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
        }
        return PendingIntent.getActivity(
            context,
            0,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    private fun actionIntent(
        context: Context,
        action: String,
        notification: MailNotification,
    ): PendingIntent {
        val intent = NotificationActionReceiver.intent(context, action, notification)
        return PendingIntent.getBroadcast(
            context,
            0,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    const val ARCHIVE_TITLE = "Archive"
    const val MARK_READ_TITLE = "Mark Read"
    private const val SUMMARY_TITLE = "New mail"
}
