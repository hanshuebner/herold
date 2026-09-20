package com.netzhansa.herold.android

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords

/**
 * Conversations written straight into the local store, for the checks
 * whose subject is a list longer than the screen (issue #439).
 *
 * The store is what every screen renders from (REQ-AND-SYNC-03), so a
 * list of forty rows is seeded here rather than delivered forty times
 * over SMTP. The rows carry ids of their own, which no `Email/changes`
 * names and no `Email/get` is asked for, so a sync pass running
 * underneath leaves them alone.
 */
object SeededRows {

    /** How far apart the seeded conversations are in time. */
    private const val SPACING_MS = 60_000L

    /**
     * Puts [count] conversations into [mailboxId], newest first, each one
     * its own thread and each subject carrying [token] so the cached
     * search finds exactly this set. [keywords] is what each row carries:
     * a `$category-<name>` keyword among them gives the set a lane of its
     * own in the inbox.
     *
     * Returns them in the order the list shows them.
     */
    suspend fun seed(
        app: HeroldApplication,
        accountId: String,
        mailboxId: String,
        token: String,
        count: Int,
        keywords: Set<String> = setOf(Keywords.SEEN),
    ): List<Email> {
        val base = System.currentTimeMillis()
        val rows = (0 until count).map { position ->
            Email(
                accountId = accountId,
                id = "$token-mail-$position",
                threadId = "$token-thread-$position",
                fromName = "Seed $position",
                fromEmail = "seed$position@acceptance.test",
                toLine = DevInstance.email,
                subject = "$token row $position",
                preview = "Row $position of the seeded list.",
                receivedAt = base - position * SPACING_MS,
                // Read already, so opening one writes nothing to a server
                // that has never heard of it.
                keywords = keywords,
                mailboxIds = setOf(mailboxId),
                bodyText = "Row $position of the seeded list.",
            )
        }
        app.container.store.upsertEmails(rows)
        return rows
    }

    /** The account's mailbox of [role], as the store holds it. */
    suspend fun mailboxId(app: HeroldApplication, accountId: String, role: String): String =
        app.container.store.mailboxList()
            .first { it.accountId == accountId && it.role == role }
            .id

    /** The signed-in principal's mail account. */
    suspend fun accountId(app: HeroldApplication): String =
        app.container.store.accountList().first { it.isPrimary }.id
}
