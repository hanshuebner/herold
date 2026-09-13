package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged

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

/**
 * The snapshot as the local store changes it: every write the UI, a
 * notification action, the outbox drain or a sync makes reaches the home
 * surfaces from here, so an optimistic local change shows on them without
 * a server round trip (issue #381, REQ-AND-SYS-20/22).
 *
 * A sync writes a page of messages one statement at a time, so the
 * changes are settled for [SNAPSHOT_DEBOUNCE_MS] before a snapshot is
 * emitted and the surfaces repaint once.
 */
@OptIn(FlowPreview::class)
fun LocalStore.homeSnapshots(
    emailLimit: Long = HOME_EMAIL_LIMIT,
    limit: Int = HomeSnapshot.DEFAULT_LIMIT,
): Flow<HomeSnapshot> =
    combine(inboxEmails(emailLimit), accounts(), mailboxes()) { emails, accounts, mailboxes ->
        HomeSnapshot.from(emails, accounts, mailboxes, limit)
    }
        .debounce(SNAPSHOT_DEBOUNCE_MS)
        .distinctUntilChanged()

/** How many messages are folded into the home surfaces' conversations. */
const val HOME_EMAIL_LIMIT: Long = 200

/** How long the store's writes settle before the surfaces repaint. */
const val SNAPSHOT_DEBOUNCE_MS: Long = 250
