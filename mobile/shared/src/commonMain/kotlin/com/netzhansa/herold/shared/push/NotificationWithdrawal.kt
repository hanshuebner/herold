package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.store.LocalStore

/**
 * Why a notification is withdrawn: the `reason` of the server's
 * `mail-dismiss` payload, and what the fold found when it made the
 * notification stale (issue #481).
 */
enum class DismissReason(val wire: String) {
    /** The message carries `$seen`, set on this or on any other client. */
    SEEN("seen"),

    /** The message holds no membership in an Inbox-role mailbox any more. */
    LEFT_INBOX("left-inbox"),

    /** The message is gone. */
    DESTROYED("destroyed"),
    ;

    companion object {
        fun fromWire(value: String?): DismissReason? = entries.firstOrNull { it.wire == value }
    }
}

/**
 * One notification the app currently shows: the thread it is tagged with
 * and the messages it was posted for. A thread's notification stands for
 * every message notified on it, so it is withdrawn once the last of them
 * is resolved, not with the first.
 */
data class PostedNotification(
    val accountId: String,
    val threadId: String,
    val emailIds: Set<String>,
)

/**
 * One message's notification, withdrawn. [emailId] null withdraws the
 * thread's notification whatever it was posted for; [threadId] null leaves
 * the thread to be resolved from the message.
 */
data class MailDismissal(
    val accountId: String,
    val threadId: String?,
    val emailId: String?,
    /**
     * What made the notification stale. The withdrawal happens whatever
     * the reason says, so a reason this client does not know still
     * clears the shade.
     */
    val reason: DismissReason? = null,
)

/**
 * The notifications the app shows and the hook that withdraws one
 * (REQ-AND-PUSH-14). The set is the platform's, so the interface lives
 * here and the implementation sits in the Android layer, which keeps the
 * reconciler platform-free.
 *
 * The posted set survives process death: a push posts a notification from
 * a cold process, and the fold that later finds the message read runs in
 * another one.
 */
interface PostedNotifications {

    /** The notifications currently in the shade. */
    suspend fun posted(): List<PostedNotification>

    /**
     * Drops the dismissal's message from its thread's notified set and
     * cancels the notification once no notified message is left, taking
     * the account summary with it when it has no child left. A dismissal
     * naming a notification the app does not show changes nothing.
     */
    suspend fun withdraw(dismissal: MailDismissal)
}

/**
 * Withdraws the notifications a fold made stale (issue #481): after the
 * `Email/changes` fold and after a full fill, every notified message is
 * measured against the local store, which by then holds what any client's
 * change did to it. A message that is read, out of the Inbox-role
 * mailboxes, or destroyed no longer has anything to notify about.
 *
 * A message the store does not hold is treated as destroyed only when the
 * fold named it destroyed. A cold push posts its notification whether or
 * not its bounded reconcile got the message into the store, so absence on
 * its own means "not fetched yet" as often as it means "gone", and reading
 * it as gone would withdraw notifications for mail that is still waiting.
 */
class NotificationReconciler(
    private val store: LocalStore,
    private val notifications: PostedNotifications,
) {

    /**
     * Measures [accountId]'s notified messages against the store.
     * [destroyed] are the ids the fold itself reported gone.
     */
    suspend fun reconcile(accountId: String, destroyed: Collection<String> = emptyList()) {
        val posted = notifications.posted().filter { it.accountId == accountId }
        if (posted.isEmpty()) return
        val gone = destroyed.toSet()
        val inboxIds = store.mailboxList()
            .filter { it.accountId == accountId && it.role == MailboxRoles.INBOX }
            .map { it.id }
            .toSet()
        posted.forEach { notification ->
            notification.emailIds.forEach { emailId ->
                val reason = reasonFor(accountId, emailId, gone, inboxIds) ?: return@forEach
                notifications.withdraw(
                    MailDismissal(
                        accountId = accountId,
                        threadId = notification.threadId,
                        emailId = emailId,
                        reason = reason,
                    ),
                )
            }
        }
    }

    /** Why [emailId]'s notification is stale, or null while it stands. */
    private suspend fun reasonFor(
        accountId: String,
        emailId: String,
        destroyed: Set<String>,
        inboxIds: Set<String>,
    ): DismissReason? {
        if (emailId in destroyed) return DismissReason.DESTROYED
        val email = store.email(accountId, emailId) ?: return null
        if (!email.isUnread) return DismissReason.SEEN
        // Where the account's mailboxes have not been synced there is no
        // inbox to be out of, so membership decides nothing.
        if (inboxIds.isNotEmpty() && email.mailboxIds.none { it in inboxIds }) {
            return DismissReason.LEFT_INBOX
        }
        return null
    }
}
