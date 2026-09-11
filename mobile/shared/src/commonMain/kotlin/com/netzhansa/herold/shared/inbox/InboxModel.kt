package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox

/** A conversation as the inbox list renders it. */
data class ThreadRow(
    val accountId: String,
    val accountName: String,
    val threadId: String,
    val latestEmailId: String,
    val subject: String,
    val senders: String,
    val preview: String,
    val receivedAt: Long,
    val messageCount: Int,
    val isUnread: Boolean,
    val isFlagged: Boolean,
    val labels: List<String>,
    val category: String?,
    val emailIds: List<String>,
)

/** A bundled category collapsed to one row, positioned by its newest member (REQ-CAT-10). */
data class BundleRow(
    val category: String,
    val threadCount: Int,
    val unreadCount: Int,
    val senders: String,
    val receivedAt: Long,
    val threads: List<ThreadRow>,
)

/** One entry of the inbox stream. */
sealed interface InboxItem {
    val receivedAt: Long

    data class Conversation(val row: ThreadRow) : InboxItem {
        override val receivedAt: Long get() = row.receivedAt
    }

    data class Bundle(val row: BundleRow) : InboxItem {
        override val receivedAt: Long get() = row.receivedAt
    }
}

/**
 * Which categories are tabs and which collapse into bundles.
 *
 * herold advertises category *names* (`CategorySettings.derivedCategories`
 * plus the `$category-<name>` keywords on messages) but no disposition
 * property, so the split is made here: the first [PINNED_LIMIT] names are
 * tabs (suite REQ-CAT-11's maximum), the rest are bundles (REQ-CAT-10).
 * When the server grows a disposition property this reads it instead.
 */
data class CategoryLanes(
    val pinned: List<String>,
    val bundled: List<String>,
) {
    companion object {
        const val PINNED_LIMIT = 5

        fun from(derived: List<String>, observed: Collection<String>): CategoryLanes {
            val names = LinkedHashSet<String>()
            derived.forEach { names.add(it) }
            observed.sorted().forEach { names.add(it) }
            val ordered = names.toList()
            return CategoryLanes(
                pinned = ordered.take(PINNED_LIMIT),
                bundled = ordered.drop(PINNED_LIMIT),
            )
        }
    }
}

/**
 * Folds the inbox messages of every account into the stream the UI renders:
 * one row per thread, newest first (suite REQ-MAIL-SUB-03), bundled
 * categories collapsed, a message shown once under its highest-priority
 * category (REQ-CAT-01).
 *
 * Pure: it is the inbox's whole presentation rule set and is unit-tested
 * without a store, a network, or Compose.
 */
object InboxAssembler {

    fun threadRows(
        emails: List<Email>,
        accounts: List<Account>,
        mailboxes: List<Mailbox>,
        accountScope: String? = null,
    ): List<ThreadRow> {
        val accountNames = accounts.associate { it.id to it.name }
        val labelNames = mailboxes.filter { it.role == null }
            .associate { (it.accountId to it.id) to it.name }
        return emails
            .filter { accountScope == null || it.accountId == accountScope }
            .groupBy { it.accountId to it.threadId }
            .map { (key, thread) ->
                val ordered = thread.sortedByDescending { it.receivedAt }
                val newest = ordered.first()
                ThreadRow(
                    accountId = key.first,
                    accountName = accountNames[key.first] ?: key.first,
                    threadId = key.second,
                    latestEmailId = newest.id,
                    subject = ordered.firstOrNull { it.subject.isNotBlank() }?.subject ?: "(no subject)",
                    senders = ordered.map { it.senderDisplay }.filter { it.isNotBlank() }
                        .distinct().take(3).joinToString(", "),
                    preview = newest.preview,
                    receivedAt = newest.receivedAt,
                    messageCount = ordered.size,
                    isUnread = ordered.any { it.isUnread },
                    isFlagged = ordered.any { it.isFlagged },
                    labels = ordered.flatMap { email ->
                        email.mailboxIds.mapNotNull { labelNames[email.accountId to it] }
                    }.distinct(),
                    category = ordered.firstNotNullOfOrNull { it.category },
                    emailIds = ordered.map { it.id },
                )
            }
            .sortedByDescending { it.receivedAt }
    }

    /**
     * The stream for the selected tab. [selectedCategory] null is the
     * complete stream: pinned categories stay inline, bundled ones collapse
     * to one row each.
     */
    fun stream(
        rows: List<ThreadRow>,
        lanes: CategoryLanes,
        selectedCategory: String? = null,
    ): List<InboxItem> {
        if (selectedCategory != null) {
            return rows.filter { it.category == selectedCategory }
                .map { InboxItem.Conversation(it) }
        }
        val bundledRows = rows.filter { it.category != null && lanes.bundled.contains(it.category) }
        val plainRows = rows.filter { it !in bundledRows }
        val bundles = bundledRows.groupBy { it.category!! }.map { (category, threads) ->
            InboxItem.Bundle(
                BundleRow(
                    category = category,
                    threadCount = threads.size,
                    unreadCount = threads.count { it.isUnread },
                    senders = threads.map { it.senders }.filter { it.isNotBlank() }
                        .distinct().take(3).joinToString(", "),
                    receivedAt = threads.maxOf { it.receivedAt },
                    threads = threads.sortedByDescending { it.receivedAt },
                ),
            )
        }
        return (plainRows.map { InboxItem.Conversation(it) } + bundles)
            .sortedByDescending { it.receivedAt }
    }

    /** Category names carried by the synced messages, for lane discovery. */
    fun observedCategories(emails: List<Email>): Set<String> =
        emails.mapNotNull { it.category }.toSet()
}
