package com.netzhansa.herold.android.home

import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.compose.runtime.Composable
import androidx.compose.ui.unit.dp
import androidx.glance.GlanceId
import androidx.glance.GlanceModifier
import androidx.glance.GlanceTheme
import androidx.glance.LocalContext
import androidx.glance.action.clickable
import androidx.glance.appwidget.GlanceAppWidget
import androidx.glance.appwidget.action.actionStartActivity
import androidx.glance.appwidget.GlanceAppWidgetReceiver
import androidx.glance.appwidget.provideContent
import androidx.glance.appwidget.updateAll
import androidx.glance.background
import androidx.glance.layout.Alignment
import androidx.glance.layout.Column
import androidx.glance.layout.Row
import androidx.glance.layout.Spacer
import androidx.glance.layout.fillMaxSize
import androidx.glance.layout.fillMaxWidth
import androidx.glance.layout.padding
import androidx.glance.text.FontWeight
import androidx.glance.text.Text
import androidx.glance.text.TextStyle
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.android.MainActivity
import com.netzhansa.herold.shared.inbox.HomeSnapshot
import com.netzhansa.herold.shared.inbox.ThreadRow
import com.netzhansa.herold.shared.links.AppLinks
import kotlinx.coroutines.flow.first

/**
 * The home-screen widget (REQ-AND-SYS-20): the unread inbox count and the
 * newest conversations, each tapping through to its thread, with a button
 * that opens compose.
 *
 * It renders from the local store, so it is populated on a phone with no
 * connection and does not wake the network to paint itself. The sync
 * engine's writes are what refresh it, through [HomeSurfaces].
 */
class InboxWidget : GlanceAppWidget() {

    override suspend fun provideGlance(context: Context, id: GlanceId) {
        val container = (context.applicationContext as HeroldApplication).container
        val snapshot = runCatching {
            HomeSnapshot.from(
                emails = container.store.inboxEmails(WIDGET_EMAIL_LIMIT).first(),
                accounts = container.store.accountList(),
                mailboxes = container.store.mailboxList(),
                limit = HomeSnapshot.DEFAULT_LIMIT,
            )
        }.getOrDefault(HomeSnapshot.EMPTY)
        provideContent { WidgetBody(snapshot) }
    }

    companion object {
        /** How many messages are folded into the widget's conversations. */
        private const val WIDGET_EMAIL_LIMIT = 200L

        /** Repaints every placed widget from the local store. */
        suspend fun refresh(context: Context) {
            runCatching { InboxWidget().updateAll(context) }
        }
    }
}

/** The receiver the launcher places; the widget itself is the Glance one. */
class InboxWidgetReceiver : GlanceAppWidgetReceiver() {
    override val glanceAppWidget: GlanceAppWidget = InboxWidget()
}

@Composable
private fun WidgetBody(snapshot: HomeSnapshot) {
    val context = LocalContext.current
    GlanceTheme {
        Column(
            modifier = GlanceModifier
                .fillMaxSize()
                .background(GlanceTheme.colors.widgetBackground)
                .padding(12.dp),
        ) {
            Row(
                modifier = GlanceModifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = "Inbox",
                    style = TextStyle(
                        color = GlanceTheme.colors.onSurface,
                        fontWeight = FontWeight.Bold,
                    ),
                    modifier = GlanceModifier.clickable(actionStartActivity(inboxIntent(context))),
                )
                Spacer(modifier = GlanceModifier.defaultWeight())
                Text(
                    text = unreadLabel(snapshot.unread),
                    style = TextStyle(color = GlanceTheme.colors.onSurfaceVariant),
                )
            }
            Spacer(modifier = GlanceModifier.padding(4.dp))
            if (snapshot.threads.isEmpty()) {
                Text(
                    text = "No mail yet",
                    style = TextStyle(color = GlanceTheme.colors.onSurfaceVariant),
                )
            } else {
                Column(modifier = GlanceModifier.defaultWeight()) {
                    snapshot.threads.forEach { row -> ThreadLine(row) }
                }
            }
            Text(
                text = "Compose",
                style = TextStyle(color = GlanceTheme.colors.primary, fontWeight = FontWeight.Medium),
                modifier = GlanceModifier
                    .padding(top = 8.dp)
                    .clickable(actionStartActivity(composeIntent(context))),
            )
        }
    }
}

@Composable
private fun ThreadLine(row: ThreadRow) {
    val context = LocalContext.current
    Column(
        modifier = GlanceModifier
            .fillMaxWidth()
            .padding(vertical = 4.dp)
            .clickable(actionStartActivity(threadIntent(context, row))),
    ) {
        Text(
            text = row.senders.ifBlank { row.accountName },
            maxLines = 1,
            style = TextStyle(
                color = GlanceTheme.colors.onSurface,
                fontWeight = if (row.isUnread) FontWeight.Bold else FontWeight.Normal,
            ),
        )
        Text(
            text = row.subject,
            maxLines = 1,
            style = TextStyle(color = GlanceTheme.colors.onSurfaceVariant),
        )
    }
}

private fun unreadLabel(unread: Int): String = when (unread) {
    0 -> "all read"
    1 -> "1 unread"
    else -> "$unread unread"
}

private fun threadIntent(context: Context, row: ThreadRow): Intent =
    shellIntent(context, AppLinks.threadUri(row.accountId, row.threadId))

private fun composeIntent(context: Context): Intent = shellIntent(context, AppLinks.composeUri())

private fun inboxIntent(context: Context): Intent = shellIntent(context, "herold://inbox")

/** The shell, addressed by the internal deep-link scheme (REQ-AND-SYS-10). */
private fun shellIntent(context: Context, uri: String): Intent =
    Intent(context, MainActivity::class.java).apply {
        action = Intent.ACTION_VIEW
        data = Uri.parse(uri)
        flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
    }
