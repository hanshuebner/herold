package com.netzhansa.herold.shared.fake

import com.netzhansa.herold.shared.push.DismissReason
import com.netzhansa.herold.shared.push.MailDismissal
import com.netzhansa.herold.shared.push.PostedNotification
import com.netzhansa.herold.shared.push.PostedNotifications

/**
 * The shade, as the reconciler sees it. It keeps the rule the Android
 * implementation keeps: a withdrawal drops one notified message, and the
 * thread's notification goes only when the last of them is gone.
 */
class FakePostedNotifications : PostedNotifications {

    private val entries = mutableListOf<PostedNotification>()

    /** The threads whose notification was cancelled, in order. */
    val cancelled = mutableListOf<String>()

    /** Every withdrawal the reconciler asked for. */
    val withdrawals = mutableListOf<MailDismissal>()

    /** Posts a notification for [emailIds] on [threadId]. */
    fun post(accountId: String, threadId: String, vararg emailIds: String) {
        entries += PostedNotification(accountId, threadId, emailIds.toSet())
    }

    /** True while the thread's notification is in the shade. */
    fun shows(threadId: String): Boolean = entries.any { it.threadId == threadId }

    /** Why the reconciler withdrew [emailId], or null when it did not. */
    fun reasonFor(emailId: String): DismissReason? =
        withdrawals.firstOrNull { it.emailId == emailId }?.reason

    override suspend fun posted(): List<PostedNotification> = entries.toList()

    override suspend fun withdraw(dismissal: MailDismissal) {
        val index = entries.indexOfFirst {
            it.accountId == dismissal.accountId && it.threadId == dismissal.threadId
        }
        if (index < 0) return
        withdrawals += dismissal
        val remaining = dismissal.emailId
            ?.let { entries[index].emailIds - it }
            ?: emptySet()
        if (remaining.isEmpty()) {
            cancelled += entries.removeAt(index).threadId
        } else {
            entries[index] = entries[index].copy(emailIds = remaining)
        }
    }
}
