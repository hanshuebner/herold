package com.netzhansa.herold.shared.domain

/**
 * Domain models the UI renders. They are the local store's row shapes, not
 * the JMAP wire shapes: the sync engine maps wire objects onto these before
 * they reach the database (docs/design/android/architecture/03-sync-and-state.md).
 */

/**
 * One JMAP account of the signed-in principal: the primary mail account or a
 * sub-account (`https://netzhansa.com/jmap/sub-accounts`, suite
 * REQ-MAIL-SUB-01..09). Every store row is keyed by [id].
 */
data class Account(
    val id: String,
    val name: String,
    val isPrimary: Boolean,
    val isPersonal: Boolean = true,
    val sortOrder: Int = 0,
)

/**
 * A JMAP Mailbox (RFC 8621 section 2). A mailbox with a null [role] is a
 * label in the suite's model, so the label chips and the label picker read
 * these same rows.
 */
data class Mailbox(
    val accountId: String,
    val id: String,
    val name: String,
    val role: String? = null,
    val parentId: String? = null,
    val sortOrder: Int = 0,
    val totalEmails: Int = 0,
    val unreadEmails: Int = 0,
)

/** An attachment of a message, as listed in the thread view. */
data class Attachment(
    val blobId: String,
    val name: String,
    val type: String,
    val size: Long,
    val cid: String? = null,
    val isInline: Boolean = false,
)

/**
 * A JMAP Email. [keywords] and [mailboxIds] carry the membership the
 * optimistic actions patch; [bodyHtml]/[bodyText] are the cached rendered
 * body, absent until the message has been opened online once
 * (REQ-AND-SYNC-12).
 */
data class Email(
    val accountId: String,
    val id: String,
    val threadId: String,
    val blobId: String? = null,
    val fromName: String = "",
    val fromEmail: String = "",
    val toLine: String = "",
    val subject: String = "",
    val preview: String = "",
    val receivedAt: Long = 0,
    val size: Long = 0,
    val hasAttachment: Boolean = false,
    val snoozedUntil: String? = null,
    val keywords: Set<String> = emptySet(),
    val mailboxIds: Set<String> = emptySet(),
    val bodyHtml: String? = null,
    val bodyText: String? = null,
    val attachments: List<Attachment> = emptyList(),
) {
    val isUnread: Boolean get() = !keywords.contains(Keywords.SEEN)
    val isFlagged: Boolean get() = keywords.contains(Keywords.FLAGGED)
    val isSnoozed: Boolean get() = keywords.contains(Keywords.SNOOZED)

    /** The category this message carries, from its `$category-<name>` keyword. */
    val category: String? get() = keywords.firstNotNullOfOrNull { Keywords.categoryName(it) }

    val senderDisplay: String get() = fromName.ifBlank { fromEmail }
}

/** A JMAP Thread (RFC 8621 section 3). */
data class Thread(
    val accountId: String,
    val id: String,
    val emailIds: List<String> = emptyList(),
)

/** A JMAP Identity (RFC 8621 section 6). */
data class Identity(
    val accountId: String,
    val id: String,
    val name: String = "",
    val email: String = "",
    val mayDelete: Boolean = false,
)

/** IMAP/JMAP keywords the client reads and writes. */
object Keywords {
    const val SEEN = "\$seen"
    const val FLAGGED = "\$flagged"
    const val DRAFT = "\$draft"
    const val ANSWERED = "\$answered"
    const val SNOOZED = "\$snoozed"
    const val CATEGORY_PREFIX = "\$category-"

    /** The category keyword for [name], e.g. `Promotions` -> `$category-Promotions`. */
    fun categoryKeyword(name: String): String = CATEGORY_PREFIX + name

    /** The category name carried by [keyword], or null when it is not a category keyword. */
    fun categoryName(keyword: String): String? =
        if (keyword.startsWith(CATEGORY_PREFIX)) keyword.removePrefix(CATEGORY_PREFIX) else null
}

/** Mailbox roles the client routes on (RFC 8621 section 2, `role`). */
object MailboxRoles {
    const val INBOX = "inbox"
    const val ARCHIVE = "archive"
    const val SENT = "sent"
    const val DRAFTS = "drafts"
    const val TRASH = "trash"
    const val JUNK = "junk"
}
