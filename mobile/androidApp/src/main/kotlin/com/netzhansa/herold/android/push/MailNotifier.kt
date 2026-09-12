package com.netzhansa.herold.android.push

import android.annotation.SuppressLint
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.view.View
import android.widget.RemoteViews
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import androidx.core.content.LocusIdCompat
import com.netzhansa.herold.android.home.ConversationShortcuts
import com.netzhansa.herold.android.MainActivity
import com.netzhansa.herold.android.R
import com.netzhansa.herold.shared.push.MailNotification
import com.netzhansa.herold.shared.push.PushChannels

/**
 * Posts and withdraws mail notifications (REQ-AND-PUSH-11/12/13/20/21).
 *
 * One notification per thread, tagged with the thread's key so a second
 * message on the same thread replaces it rather than stacking; every
 * notification of one account carries that account's group, under a
 * summary headed by the account's address. The shade shows the sender as
 * the title with their picture as the large icon, the subject as the body
 * and the preview under it when expanded, and the message's attachments as
 * chips. Tapping the body deep-links into the thread; Archive and Mark
 * Read run without opening the app; Reply opens the composer on the thread.
 */
object MailNotifier {

    /** Extras the tap intent carries into [MainActivity]. */
    const val EXTRA_ACCOUNT_ID = "com.netzhansa.herold.android.extra.ACCOUNT_ID"
    const val EXTRA_THREAD_ID = "com.netzhansa.herold.android.extra.THREAD_ID"

    /** Set on the Reply intent: the message the composer answers. */
    const val EXTRA_REPLY_EMAIL_ID = "com.netzhansa.herold.android.extra.REPLY_EMAIL_ID"

    private const val CHILD_ID = 1
    private const val SUMMARY_ID = 2

    /** At most this many attachment chips before the rest become "+n". */
    private const val VISIBLE_CHIPS = 2

    @SuppressLint("MissingPermission")
    fun post(context: Context, notification: MailNotification, presentation: MailPresentation = MailPresentation()) {
        val manager = NotificationManagerCompat.from(context)
        if (!manager.areNotificationsEnabled()) return
        // The tile's quiet period silences the shade without touching the
        // subscription: the push still reconciles, the mail is simply
        // waiting when the mute ends (REQ-AND-SYS-21).
        if (NotificationMute.isMuted(context)) return
        manager.notify(notification.tag, CHILD_ID, build(context, notification, presentation))
        manager.notify(
            notification.groupKey,
            SUMMARY_ID,
            buildSummary(context, notification, presentation),
        )
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

    /** The size the shade draws a large icon at. */
    fun largeIconSize(context: Context): Int =
        context.resources.getDimensionPixelSize(android.R.dimen.notification_large_icon_height)
            .takeIf { it > 0 } ?: DEFAULT_LARGE_ICON_PX

    private fun build(
        context: Context,
        notification: MailNotification,
        presentation: MailPresentation,
    ): android.app.Notification {
        // The conversation the mail belongs to is published as a
        // long-lived shortcut and named by the notification, which is what
        // ties the two together in the shade and puts the conversation in
        // the launcher's long-press menu (REQ-AND-PUSH-22, REQ-AND-SYS-22).
        val shortcutId = runCatching {
            ConversationShortcuts.publishFor(
                context = context,
                accountId = notification.accountId,
                threadId = notification.threadId,
                senderName = notification.senderName,
                senderAddress = notification.senderAddress,
                subject = notification.body,
            )
        }.getOrNull()

        val builder = NotificationCompat.Builder(context, PushChannels.MAIL)
            .setSmallIcon(R.drawable.ic_notification)
            .setColor(ContextCompat.getColor(context, R.color.ic_launcher_background))
            .setContentTitle(notification.title)
            .setContentText(notification.body)
            .setLargeIcon(presentation.avatar)
            .setCategory(NotificationCompat.CATEGORY_EMAIL)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setGroup(notification.groupKey)
            // The children alert; the summary is only the bundle's header,
            // so it must not raise a second heads-up of its own.
            .setGroupAlertBehavior(NotificationCompat.GROUP_ALERT_CHILDREN)
            .setAutoCancel(true)
            .setContentIntent(openThreadIntent(context, notification))
        if (shortcutId != null) {
            builder.setShortcutId(shortcutId).setLocusId(LocusIdCompat(shortcutId))
        }

        if (presentation.attachments.isEmpty()) {
            builder.setStyle(NotificationCompat.BigTextStyle().bigText(notification.expandedBody))
        } else {
            // Chips are not a shape the platform styles offer, so the
            // expanded view is drawn here and the system keeps the header,
            // the large icon and the actions around it.
            builder.setStyle(NotificationCompat.DecoratedCustomViewStyle())
                .setCustomBigContentView(expandedView(context, notification, presentation))
        }

        return builder
            .addAction(
                NotificationCompat.Action.Builder(
                    R.drawable.ic_action_archive,
                    ARCHIVE_TITLE,
                    actionIntent(context, NotificationActionReceiver.ACTION_ARCHIVE, notification),
                ).build(),
            )
            .addAction(
                NotificationCompat.Action.Builder(
                    R.drawable.ic_action_mark_read,
                    MARK_READ_TITLE,
                    actionIntent(context, NotificationActionReceiver.ACTION_MARK_READ, notification),
                ).build(),
            )
            .addAction(
                NotificationCompat.Action.Builder(
                    R.drawable.ic_action_reply,
                    REPLY_TITLE,
                    replyIntent(context, notification),
                ).build(),
            )
            .build()
    }

    /**
     * The bundle's header. Gmail heads it with the account the mail
     * arrived for, which is what tells two accounts' bundles apart.
     */
    private fun buildSummary(
        context: Context,
        notification: MailNotification,
        presentation: MailPresentation,
    ): android.app.Notification =
        NotificationCompat.Builder(context, PushChannels.MAIL)
            .setSmallIcon(R.drawable.ic_notification)
            .setColor(ContextCompat.getColor(context, R.color.ic_launcher_background))
            .setContentTitle(presentation.accountLabel.ifBlank { notification.title })
            .setGroup(notification.groupKey)
            .setGroupSummary(true)
            .setGroupAlertBehavior(NotificationCompat.GROUP_ALERT_CHILDREN)
            .setAutoCancel(true)
            .setStyle(
                NotificationCompat.InboxStyle()
                    .setSummaryText(presentation.accountLabel.ifBlank { null }),
            )
            .build()

    /** The expanded body: the message's text, then its attachment chips. */
    private fun expandedView(
        context: Context,
        notification: MailNotification,
        presentation: MailPresentation,
    ): RemoteViews {
        val views = RemoteViews(context.packageName, R.layout.notification_mail)
        views.setTextViewText(R.id.notification_sender, notification.title)
        views.setTextViewText(R.id.notification_subject, notification.body)
        views.setTextViewText(R.id.notification_preview, notification.preview)
        views.setViewVisibility(
            R.id.notification_preview,
            if (notification.preview.isBlank()) View.GONE else View.VISIBLE,
        )

        val chips = listOf(
            Triple(R.id.chip_one, R.id.chip_one_thumbnail, R.id.chip_one_label),
            Triple(R.id.chip_two, R.id.chip_two_thumbnail, R.id.chip_two_label),
            Triple(R.id.chip_three, R.id.chip_three_thumbnail, R.id.chip_three_label),
        )
        val shown = presentation.attachments.take(VISIBLE_CHIPS)
        val overflow = presentation.attachments.size - shown.size

        views.setViewVisibility(R.id.notification_chips, View.VISIBLE)
        shown.forEachIndexed { index, chip ->
            val (root, thumbnail, label) = chips[index]
            views.setViewVisibility(root, View.VISIBLE)
            views.setTextViewText(label, chip.name)
            if (chip.thumbnail != null) {
                views.setImageViewBitmap(thumbnail, chip.thumbnail)
                views.setViewVisibility(thumbnail, View.VISIBLE)
            } else {
                views.setViewVisibility(thumbnail, View.GONE)
            }
        }
        if (overflow > 0) {
            val (root, thumbnail, label) = chips[VISIBLE_CHIPS]
            views.setViewVisibility(root, View.VISIBLE)
            views.setViewVisibility(thumbnail, View.GONE)
            views.setTextViewText(label, "+$overflow")
        }
        return views
    }

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

    /**
     * Reply: the composer, opened on the message with its quote prepared
     * (REQ-AND-PUSH-21's deep-link form). The shade's inline reply is a
     * later milestone.
     */
    fun replyIntent(context: Context, notification: MailNotification): PendingIntent {
        val intent = Intent(context, MainActivity::class.java).apply {
            action = Intent.ACTION_VIEW
            data = Uri.parse(
                "herold://reply/${notification.accountId}/${notification.emailId ?: notification.threadId}",
            )
            putExtra(EXTRA_ACCOUNT_ID, notification.accountId)
            putExtra(EXTRA_THREAD_ID, notification.threadId)
            putExtra(EXTRA_REPLY_EMAIL_ID, notification.emailId)
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
    const val MARK_READ_TITLE = "Mark read"
    const val REPLY_TITLE = "Reply"

    private const val DEFAULT_LARGE_ICON_PX = 128
}

/**
 * What the device resolves for a notification beyond the payload: the
 * account the mail arrived for, the sender's picture, and the message's
 * attachments (issue #348). It is filled during the bounded reconcile the
 * messaging service already runs, and is empty when that pass did not get
 * far enough - the notification still posts.
 */
data class MailPresentation(
    val accountLabel: String = "",
    val avatar: Bitmap? = null,
    val attachments: List<AttachmentChip> = emptyList(),
)

/** One attachment chip: the file's name and, for an image, its thumbnail. */
data class AttachmentChip(val name: String, val thumbnail: Bitmap? = null)
