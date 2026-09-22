package com.netzhansa.herold.android.push

import android.content.Context
import com.netzhansa.herold.shared.push.MailDismissal
import com.netzhansa.herold.shared.push.MailNotification
import com.netzhansa.herold.shared.push.PostedNotification
import com.netzhansa.herold.shared.push.PostedNotifications
import org.json.JSONArray
import org.json.JSONObject

/**
 * What the shade is showing, per thread: the account, the thread, and the
 * messages the app posted a notification for (REQ-AND-PUSH-14). It is the
 * set the sync fold and a `mail-dismiss` push measure their messages
 * against.
 *
 * It lives in preferences because a notification outlives the process that
 * posted it: a push posts from a cold process, the fold that later finds
 * the message read runs in another one, and the shade still carries the
 * first one's notification. Only message and thread ids are written - the
 * same ids the shade's own intents already carry.
 */
object PostedMailNotifications {

    private const val FILE = "herold-notifications"
    private const val KEY = "posted"

    /** Records the message a notification was just posted for. */
    @Synchronized
    fun record(context: Context, accountId: String, threadId: String, emailId: String?) {
        val entries = read(context).toMutableList()
        val index = entries.indexOfFirst { it.accountId == accountId && it.threadId == threadId }
        val ids = if (emailId.isNullOrBlank()) emptySet() else setOf(emailId)
        if (index < 0) {
            entries += PostedNotification(accountId, threadId, ids)
        } else {
            entries[index] = entries[index].copy(emailIds = entries[index].emailIds + ids)
        }
        write(context, entries)
    }

    /** The notifications the app shows. */
    @Synchronized
    fun all(context: Context): List<PostedNotification> = read(context)

    /** Forgets a thread's notification, once it is out of the shade. */
    @Synchronized
    fun forget(context: Context, tag: String) {
        val entries = read(context).filterNot { MailNotification.tagFor(it.accountId, it.threadId) == tag }
        write(context, entries)
    }

    /**
     * Drops [emailId] from its thread's notified set and answers what is
     * left. A message that was never notified about leaves the set as it
     * stands; a null [emailId] takes the whole thread.
     */
    @Synchronized
    fun drop(context: Context, accountId: String, threadId: String, emailId: String?): Set<String> {
        val entries = read(context).toMutableList()
        val index = entries.indexOfFirst { it.accountId == accountId && it.threadId == threadId }
        if (index < 0) return emptySet()
        val remaining = if (emailId == null) emptySet() else entries[index].emailIds - emailId
        entries[index] = entries[index].copy(emailIds = remaining)
        write(context, entries)
        return remaining
    }

    /** Drops everything, for a sign-out or a test's clean slate. */
    @Synchronized
    fun clear(context: Context) {
        write(context, emptyList())
    }

    private fun read(context: Context): List<PostedNotification> {
        val text = context.getSharedPreferences(FILE, Context.MODE_PRIVATE).getString(KEY, null)
            ?: return emptyList()
        val array = runCatching { JSONArray(text) }.getOrNull() ?: return emptyList()
        return (0 until array.length()).mapNotNull { index ->
            val row = array.optJSONObject(index) ?: return@mapNotNull null
            val accountId = row.optString("accountId")
            val threadId = row.optString("threadId")
            if (accountId.isBlank() || threadId.isBlank()) return@mapNotNull null
            val ids = row.optJSONArray("emailIds")
            PostedNotification(
                accountId = accountId,
                threadId = threadId,
                emailIds = buildSet {
                    if (ids != null) {
                        (0 until ids.length()).forEach { add(ids.optString(it)) }
                    }
                }.filter { it.isNotBlank() }.toSet(),
            )
        }
    }

    private fun write(context: Context, entries: List<PostedNotification>) {
        val array = JSONArray()
        entries.forEach { entry ->
            array.put(
                JSONObject()
                    .put("accountId", entry.accountId)
                    .put("threadId", entry.threadId)
                    .put("emailIds", JSONArray(entry.emailIds.toList())),
            )
        }
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit()
            .putString(KEY, array.toString())
            .apply()
    }
}

/**
 * The shade's posted set as the shared reconciler and the push path see it
 * (issue #481): the fold hands it the messages it has just folded, a
 * `mail-dismiss` push hands it one message, and both end in
 * [MailNotifier.cancel] once a thread has no notified message left.
 */
class ShadeNotifications(context: Context) : PostedNotifications {

    private val context: Context = context.applicationContext

    override suspend fun posted(): List<PostedNotification> = PostedMailNotifications.all(context)

    override suspend fun withdraw(dismissal: MailDismissal) {
        val posted = PostedMailNotifications.all(context)
        val entry = when {
            dismissal.threadId != null ->
                posted.firstOrNull { it.accountId == dismissal.accountId && it.threadId == dismissal.threadId }

            dismissal.emailId != null ->
                posted.firstOrNull { it.accountId == dismissal.accountId && dismissal.emailId in it.emailIds }

            else -> null
        } ?: return
        val remaining = PostedMailNotifications.drop(
            context,
            entry.accountId,
            entry.threadId,
            dismissal.emailId,
        )
        // The thread's notification stands for every message notified on
        // it, so it goes when the last of them is resolved.
        if (remaining.isEmpty()) {
            MailNotifier.cancel(context, MailNotification.tagFor(entry.accountId, entry.threadId))
        }
    }
}
