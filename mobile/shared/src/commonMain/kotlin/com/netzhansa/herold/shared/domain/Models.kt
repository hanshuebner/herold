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

/**
 * A mail address as a header carries it. Compose needs the structured
 * form - a reply's To list is built from the parent's addresses, not from
 * a rendered line (suite REQ-MAIL-30).
 */
data class MailAddress(
    val name: String? = null,
    val email: String,
) {
    /** "Name <addr>" when a display name is present, the bare address otherwise. */
    fun format(): String = if (!name.isNullOrBlank()) "$name <$email>" else email

    val display: String get() = name?.takeIf { it.isNotBlank() } ?: email
}

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
    val toAddresses: List<MailAddress> = emptyList(),
    val ccAddresses: List<MailAddress> = emptyList(),
    /** RFC 5322 Message-ID(s), In-Reply-To and References, for threading a reply. */
    val messageId: List<String> = emptyList(),
    val inReplyTo: List<String> = emptyList(),
    val references: List<String> = emptyList(),
    /** The address herold delivered this message to (server REQ-FLOW-34). */
    val deliveredTo: String? = null,
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
    val isUnread: Boolean get() = keywords.none { it.equals(Keywords.SEEN, ignoreCase = true) }
    val isFlagged: Boolean get() = keywords.any { it.equals(Keywords.FLAGGED, ignoreCase = true) }
    val isSnoozed: Boolean get() = keywords.any { it.equals(Keywords.SNOOZED, ignoreCase = true) }

    /** The category this message carries, from its `$category-<name>` keyword. */
    val category: String? get() = keywords.firstNotNullOfOrNull { Keywords.categoryName(it) }

    val senderDisplay: String get() = fromName.ifBlank { fromEmail }

    val fromAddress: MailAddress get() = MailAddress(fromName.takeIf { it.isNotBlank() }, fromEmail)

    val isDraft: Boolean get() = keywords.any { it.equals(Keywords.DRAFT, ignoreCase = true) }
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

    /**
     * The category keyword for [name]. herold stores keywords case-folded
     * (IMAP keywords are case-insensitive), so a category's identity on the
     * client is its lower-cased name and presentation capitalises it.
     */
    fun categoryKeyword(name: String): String = CATEGORY_PREFIX + name.lowercase()

    /** The category name carried by [keyword], or null when it is not a category keyword. */
    fun categoryName(keyword: String): String? =
        if (keyword.startsWith(CATEGORY_PREFIX, ignoreCase = true)) {
            keyword.substring(CATEGORY_PREFIX.length).lowercase()
        } else {
            null
        }

    /** A category name as the UI shows it. */
    fun categoryLabel(name: String): String = name.replaceFirstChar { it.uppercase() }
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
