package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.CategoryDisposition
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
    /** Every category the conversation's messages carry (REQ-CAT-01). */
    val categories: List<String>,
    val emailIds: List<String>,
    /** The wake time the server holds for the conversation, when it sleeps. */
    val wakeAt: String? = null,
    /** True when an unsent answer to this conversation is waiting (issue #371). */
    val hasDraft: Boolean = false,
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
 * The inbox lanes, as the server's `Mailbox.disposition` and
 * `Mailbox.priority` define them (issue #333, suite REQ-CAT-04/05/10/11).
 *
 * A category is a label, so its lane is a property of the label's mailbox:
 * `pinned` labels are the tabs above the stream in priority order,
 * `bundled` ones collapse to one row each, `daily`, `weekly` and `filed`
 * keep their messages out of the inbox stream, and `none` gives the
 * category no lane at all. [order] is the principal's one priority list,
 * which resolves a message carrying several categories to a single lane
 * (REQ-CAT-01/05).
 */
data class CategoryLanes(
    val pinned: List<String>,
    val bundled: List<String>,
    /** Categories whose messages the inbox stream leaves out. */
    val hidden: List<String> = emptyList(),
    /** Every known category, highest priority first. */
    val order: List<String> = pinned + bundled + hidden,
    val dispositions: Map<String, CategoryDisposition> = buildMap {
        pinned.forEach { put(it, CategoryDisposition.PINNED) }
        bundled.forEach { put(it, CategoryDisposition.BUNDLED) }
        hidden.forEach { put(it, CategoryDisposition.FILED) }
    },
) {
    /**
     * The category that decides where a message carrying [categories]
     * goes: the highest-priority one it holds (REQ-CAT-05). Null when it
     * carries none.
     */
    fun resolve(categories: Collection<String>): String? =
        categories.minWithOrNull(compareBy({ rank(it) }, { it }))

    /** [category]'s disposition, `none` for one the account has no label for. */
    fun dispositionOf(category: String?): CategoryDisposition =
        category?.let { dispositions[it] } ?: CategoryDisposition.NONE

    /** Where [category] sits in the priority list; an unranked one sorts last. */
    private fun rank(category: String): Int =
        order.indexOf(category).takeIf { it >= 0 } ?: Int.MAX_VALUE

    companion object {
        const val PINNED_LIMIT = CategoryDisposition.PINNED_LIMIT

        /** No lanes at all, for a store that holds no labels yet. */
        val EMPTY = CategoryLanes(pinned = emptyList(), bundled = emptyList())

        /**
         * The lanes the account's labels define. [accountScope] narrows to
         * one account; in the combined view the labels of every account
         * fold together by name, since a category's identity on the wire
         * is its case-folded name (the `$category-<name>` keyword).
         */
        fun from(mailboxes: List<Mailbox>, accountScope: String? = null): CategoryLanes {
            val folded = mailboxes
                .filter { it.role == null && (accountScope == null || it.accountId == accountScope) }
                .groupBy { it.categoryName }
                .mapValues { (_, rows) -> fold(rows) }
            val ordered = folded.entries
                .sortedWith(compareBy({ it.value.priority ?: Int.MAX_VALUE }, { it.key }))
            val dispositions = ordered.associate { it.key to it.value.disposition }
            return CategoryLanes(
                pinned = ordered.filter { it.value.disposition == CategoryDisposition.PINNED }
                    .map { it.key }.take(PINNED_LIMIT),
                bundled = ordered.filter { it.value.disposition == CategoryDisposition.BUNDLED }
                    .map { it.key },
                hidden = ordered.filter { it.value.disposition.hidesFromInbox }.map { it.key },
                order = ordered.map { it.key },
                dispositions = dispositions,
            )
        }

        /**
         * One category out of the same-named labels of several accounts:
         * the strongest disposition any of them carries, at the best rank
         * any of them holds.
         */
        private fun fold(rows: List<Mailbox>): Lane {
            val ranked = rows.sortedWith(compareBy({ it.priority ?: Int.MAX_VALUE }, { it.accountId }))
            return Lane(
                disposition = ranked.firstOrNull { it.disposition != CategoryDisposition.NONE }
                    ?.disposition ?: CategoryDisposition.NONE,
                priority = ranked.firstNotNullOfOrNull { it.priority },
            )
        }

        private data class Lane(val disposition: CategoryDisposition, val priority: Int?)
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
        /** The account's drafts, so a conversation with one is marked (issue #371). */
        drafts: List<Email> = emptyList(),
        /** Conversations with something in the outbox, by thread id. */
        pendingThreads: Set<String> = emptySet(),
    ): List<ThreadRow> =
        // A snoozed message is out of the stream until its wake time
        // (suite REQ-SNZ-10); a conversation whose every message sleeps
        // leaves the list with them, and the Snoozed destination lists it
        // instead (issue #353).
        fold(emails.filter { !it.isSnoozed }, accounts, mailboxes, accountScope, drafts, pendingThreads)
            .sortedByDescending { it.receivedAt }

    /**
     * The Snoozed destination's rows: the conversations with a wake time,
     * next to wake first (suite REQ-SNZ-14).
     */
    fun snoozedRows(
        emails: List<Email>,
        accounts: List<Account>,
        mailboxes: List<Mailbox>,
        accountScope: String? = null,
    ): List<ThreadRow> =
        fold(emails.filter { it.isSnoozed || it.snoozedUntil != null }, accounts, mailboxes, accountScope)
            .sortedBy { it.wakeAt ?: "" }

    /** One row per conversation, whichever set of messages it is given. */
    private fun fold(
        emails: List<Email>,
        accounts: List<Account>,
        mailboxes: List<Mailbox>,
        accountScope: String?,
        drafts: List<Email> = emptyList(),
        pendingThreads: Set<String> = emptySet(),
    ): List<ThreadRow> {
        val draftThreads = drafts.map { it.accountId to it.threadId }.toSet()
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
                    categories = ordered.flatMap { it.categories }.distinct().sorted(),
                    emailIds = ordered.map { it.id },
                    wakeAt = ordered.firstNotNullOfOrNull { it.snoozedUntil },
                    hasDraft = key in draftThreads || key.second in pendingThreads,
                )
            }
    }

    /**
     * The stream for the selected tab. [selectedCategory] null is the
     * complete stream: a `pinned` or plain conversation stays inline, a
     * `bundled` category collapses to one row, and a category the server
     * defers or files keeps its conversations out (REQ-CAT-10/16).
     *
     * A conversation carrying several categories appears once, under the
     * highest-priority one (REQ-CAT-01).
     */
    fun stream(
        rows: List<ThreadRow>,
        lanes: CategoryLanes,
        selectedCategory: String? = null,
    ): List<InboxItem> {
        val laneOf = rows.associateWith { lanes.resolve(it.categories) }
        val visible = rows.filter { !lanes.dispositionOf(laneOf[it]).hidesFromInbox }
        if (selectedCategory != null) {
            return visible.filter { laneOf[it] == selectedCategory }.map { InboxItem.Conversation(it) }
        }
        val bundledRows = visible.filter {
            lanes.dispositionOf(laneOf[it]) == CategoryDisposition.BUNDLED
        }
        val plainRows = visible.filter { it !in bundledRows }
        val bundles = bundledRows.groupBy { laneOf[it]!! }.map { (category, threads) ->
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

    /** Every category name the synced messages carry. */
    fun observedCategories(emails: List<Email>): Set<String> =
        emails.flatMap { it.categories }.toSet()
}
