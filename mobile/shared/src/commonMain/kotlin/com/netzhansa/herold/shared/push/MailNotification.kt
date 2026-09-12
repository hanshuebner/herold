package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.mail.SenderLine

/**
 * What a mail push renders as, derived from the payload alone
 * (REQ-AND-PUSH-11/12). Platform-free so the mapping is a host-JVM test;
 * the Android layer turns it into a `Notification`, adding what only the
 * device can resolve - the sender's picture and the message's attachments.
 *
 * [tag] is the per-thread coalescing key: a second message on the same
 * thread replaces the first notification rather than stacking beside it.
 * [groupKey] bundles every notification of one account under a summary.
 */
data class MailNotification(
    val accountId: String,
    val threadId: String,
    val emailId: String?,
    val inboxMailboxId: String?,
    /** The sender's display name, decoded, or the address when there is none. */
    val senderName: String,
    /** The sender's bare address, for the avatar lookup; may be empty. */
    val senderAddress: String,
    val subject: String,
    val preview: String,
    val tag: String,
    val groupKey: String,
) {
    /** The sender is the title, as in the suite's service worker and in Gmail. */
    val title: String get() = senderName.ifBlank { senderAddress.ifBlank { DEFAULT_TITLE } }

    /** The collapsed line under the title: what the message is about. */
    val body: String get() = subject.ifBlank { preview }

    /**
     * What the expanded notification shows: the subject, then the
     * server's bounded preview on its own line (suite REQ-PUSH-41; the
     * preview is already capped at 80 bytes server-side).
     */
    val expandedBody: String
        get() = listOf(subject, preview).filter { it.isNotBlank() }.joinToString("\n")

    companion object {
        const val DEFAULT_TITLE = "New message"

        fun tagFor(accountId: String, threadId: String): String = "thread:$accountId:$threadId"

        fun groupFor(accountId: String): String = "account:$accountId"
    }
}

/**
 * The notification a mail payload renders as, or null when the payload is
 * not mail or names no account/thread to route to.
 */
fun PushEnvelope.mailNotification(): MailNotification? {
    if (kind != PushKind.MAIL) return null
    val account = accountId ?: return null
    val thread = threadId ?: emailId?.let { "t$it" } ?: return null
    val sender = SenderLine.parse(from, fromAddress)
    return MailNotification(
        accountId = account,
        threadId = thread,
        emailId = emailId,
        inboxMailboxId = inboxMailboxId,
        senderName = sender.name.orEmpty().ifBlank { sender.email },
        senderAddress = sender.email,
        subject = subject,
        preview = preview,
        tag = MailNotification.tagFor(account, thread),
        groupKey = MailNotification.groupFor(account),
    )
}

/** The channel a payload's kind posts to (REQ-AND-PUSH-10). */
fun PushKind.channelId(): String = when (this) {
    PushKind.MAIL -> PushChannels.MAIL
    PushKind.CHAT -> PushChannels.CHAT
    PushKind.CALL -> PushChannels.CALLS
    PushKind.CALENDAR_INVITE -> PushChannels.CALENDAR
    PushKind.REACTION -> PushChannels.REACTIONS
}

/**
 * The per-kind notification channels the app creates at start
 * (REQ-AND-PUSH-10), so importance, sound and vibration are tuned per kind
 * in system settings. Named here rather than in the Android layer so the
 * mapping above stays platform-free.
 */
object PushChannels {
    const val MAIL = "mail"
    const val CHAT = "chat"
    const val CALLS = "calls"
    const val CALENDAR = "calendar"
    const val REACTIONS = "reactions"

    val ALL = listOf(MAIL, CHAT, CALLS, CALENDAR, REACTIONS)
}
