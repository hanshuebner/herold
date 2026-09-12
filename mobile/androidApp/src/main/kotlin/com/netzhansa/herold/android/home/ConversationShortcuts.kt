package com.netzhansa.herold.android.home

import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.core.app.Person
import androidx.core.content.pm.ShortcutInfoCompat
import androidx.core.content.pm.ShortcutManagerCompat
import androidx.core.graphics.drawable.IconCompat
import com.netzhansa.herold.android.MainActivity
import com.netzhansa.herold.android.push.SenderAvatars
import com.netzhansa.herold.shared.inbox.ThreadRow
import com.netzhansa.herold.shared.links.AppLinks

/**
 * The launcher's conversation shortcuts (REQ-AND-SYS-22) and the identity
 * a mail notification is rendered against (REQ-AND-PUSH-22).
 *
 * One shortcut per recent conversation, long-lived so the shade can point
 * a notification at it, published from the local store so the launcher's
 * long-press menu is populated without a connection. The static Compose
 * shortcut is declared in `res/xml/shortcuts.xml`.
 */
object ConversationShortcuts {

    /** How many conversations the launcher is given. */
    const val LIMIT = 4

    /** Marks a shortcut as one a notification can be attached to. */
    private const val CONVERSATION_CATEGORY = "android.shortcut.conversation"

    private const val AVATAR_PX = 128

    fun idFor(accountId: String, threadId: String): String = "thread:$accountId:$threadId"

    /**
     * Republishes the launcher's conversations from a store snapshot,
     * newest first. Shortcuts of conversations that fell out of the set
     * are dropped, so the menu does not accumulate stale entries.
     */
    fun publish(context: Context, threads: List<ThreadRow>) {
        val wanted = threads.take(LIMIT)
        val shortcuts = wanted.mapIndexed { index, row ->
            build(
                context = context,
                accountId = row.accountId,
                threadId = row.threadId,
                label = row.senders.ifBlank { row.subject },
                longLabel = row.subject,
                address = null,
                rank = index,
            )
        }
        val live = wanted.map { idFor(it.accountId, it.threadId) }.toSet()
        val stale = ShortcutManagerCompat.getDynamicShortcuts(context)
            .map { it.id }
            .filter { it.startsWith("thread:") && it !in live }
        if (stale.isNotEmpty()) ShortcutManagerCompat.removeDynamicShortcuts(context, stale)
        shortcuts.forEach { ShortcutManagerCompat.pushDynamicShortcut(context, it) }
    }

    /**
     * Publishes the conversation a notification is about and returns its
     * shortcut id, which the notification carries so the shade ties the
     * two together.
     */
    fun publishFor(
        context: Context,
        accountId: String,
        threadId: String,
        senderName: String,
        senderAddress: String,
        subject: String,
    ): String {
        val shortcut = build(
            context = context,
            accountId = accountId,
            threadId = threadId,
            label = senderName.ifBlank { senderAddress }.ifBlank { subject },
            longLabel = subject.ifBlank { senderName },
            address = senderAddress,
            rank = 0,
        )
        ShortcutManagerCompat.pushDynamicShortcut(context, shortcut)
        return shortcut.id
    }

    /** Drops every conversation shortcut, on sign-out. */
    fun clear(context: Context) {
        val ids = ShortcutManagerCompat.getDynamicShortcuts(context)
            .map { it.id }
            .filter { it.startsWith("thread:") }
        if (ids.isNotEmpty()) ShortcutManagerCompat.removeDynamicShortcuts(context, ids)
    }

    private fun build(
        context: Context,
        accountId: String,
        threadId: String,
        label: String,
        longLabel: String,
        address: String?,
        rank: Int,
    ): ShortcutInfoCompat {
        val short = label.ifBlank { "Conversation" }.take(24)
        val icon = IconCompat.createWithAdaptiveBitmap(
            SenderAvatars.of(bytes = null, name = label, address = address.orEmpty(), sizePx = AVATAR_PX),
        )
        val person = Person.Builder()
            .setName(short)
            .setKey(address.orEmpty().ifBlank { threadId })
            .setIcon(icon)
            .build()
        return ShortcutInfoCompat.Builder(context, idFor(accountId, threadId))
            .setShortLabel(short)
            .setLongLabel(longLabel.ifBlank { short }.take(48))
            .setLongLived(true)
            .setRank(rank)
            .setCategories(setOf(CONVERSATION_CATEGORY))
            .setPerson(person)
            .setIcon(icon)
            .setIntent(
                Intent(context, MainActivity::class.java).apply {
                    action = Intent.ACTION_VIEW
                    data = Uri.parse(AppLinks.threadUri(accountId, threadId))
                },
            )
            .build()
    }
}
