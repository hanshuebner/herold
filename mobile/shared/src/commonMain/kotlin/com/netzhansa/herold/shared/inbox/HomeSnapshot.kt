package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox

/**
 * What the home-screen surfaces render (REQ-AND-SYS-20/22): the unread
 * inbox count and the newest conversations. It is folded from the local
 * store, so the widget and the launcher's conversation shortcuts are
 * populated without a connection.
 */
data class HomeSnapshot(
    val unread: Int,
    val threads: List<ThreadRow>,
) {
    companion object {
        /** How many conversations a home surface shows. */
        const val DEFAULT_LIMIT = 5

        val EMPTY = HomeSnapshot(0, emptyList())

        fun from(
            emails: List<Email>,
            accounts: List<Account>,
            mailboxes: List<Mailbox>,
            limit: Int = DEFAULT_LIMIT,
        ): HomeSnapshot {
            val rows = InboxAssembler.threadRows(emails, accounts, mailboxes)
            return HomeSnapshot(
                unread = rows.count { it.isUnread },
                threads = rows.take(limit),
            )
        }
    }
}
