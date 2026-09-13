package com.netzhansa.herold.shared.inbox

import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles

/** Which list the shell is showing (suite REQ-LBL-20, REQ-UI-13b/d). */
sealed interface MailDestination {
    /** The combined inbox stream with its category tabs. */
    data object Inbox : MailDestination

    /** The conversations the server holds a wake time for. */
    data object Snoozed : MailDestination

    /**
     * One mailbox across the accounts in scope: a system folder addressed
     * by [role], or a label addressed by [name]. A label of the same name
     * in two accounts is one destination, so the combined view lists both
     * accounts' mail and the scoped view lists one account's.
     */
    data class Folder(val role: String?, val name: String, val title: String) : MailDestination
}

/** A mailbox row of the drawer, folded over the accounts in scope. */
data class DrawerMailbox(
    val role: String?,
    val name: String,
    val title: String,
    /** Label nesting depth; 0 for a system folder and a top-level label. */
    val depth: Int,
    val unread: Int,
    /** The account-local mailboxes this row stands for. */
    val mailboxes: List<Mailbox>,
) {
    val destination: MailDestination.Folder get() = MailDestination.Folder(role, name, title)

    val ids: List<String> get() = mailboxes.map { it.id }
}

/**
 * The drawer's mailbox sections, folded from the synced mailboxes: the
 * system folders in the suite's sidebar order (`REQ-UI-13b`) and the label
 * tree below them, top-level labels first with their children indented and
 * alphabetical within each parent (`REQ-LBL-05/06`).
 *
 * Pure, so the ordering and the account fold are unit-tested without a
 * store or Compose.
 */
object DrawerModel {

    /**
     * The system folders the drawer offers, in the suite's order. The
     * inbox and the snoozed view are their own destinations above these.
     */
    val FOLDER_ROLES: List<Pair<String, String>> = listOf(
        MailboxRoles.SENT to "Sent",
        MailboxRoles.DRAFTS to "Drafts",
        MailboxRoles.ARCHIVE to "All Mail",
        MailboxRoles.JUNK to "Spam",
        MailboxRoles.TRASH to "Trash",
    )

    fun folders(mailboxes: List<Mailbox>, accountScope: String? = null): List<DrawerMailbox> {
        val inScope = mailboxes.filter { accountScope == null || it.accountId == accountScope }
        return FOLDER_ROLES.mapNotNull { (role, title) ->
            val matching = inScope.filter { it.role == role }
            if (matching.isEmpty()) return@mapNotNull null
            DrawerMailbox(
                role = role,
                name = title,
                title = title,
                depth = 0,
                unread = matching.sumOf { it.unreadEmails },
                mailboxes = matching,
            )
        }
    }

    /** The inbox row's own unread count, for the destination above the folders. */
    fun inboxUnread(mailboxes: List<Mailbox>, accountScope: String? = null): Int =
        mailboxes.filter { accountScope == null || it.accountId == accountScope }
            .filter { it.role == MailboxRoles.INBOX }
            .sumOf { it.unreadEmails }

    /**
     * The label tree, flattened depth-first: a label is a mailbox with no
     * role, and labels of the same name in different accounts are one row.
     */
    fun labels(mailboxes: List<Mailbox>, accountScope: String? = null): List<DrawerMailbox> {
        val inScope = mailboxes.filter { accountScope == null || it.accountId == accountScope }
        val labels = inScope.filter { it.role == null }
        val byId = labels.associateBy { it.accountId to it.id }
        // A label's path is what nests it, so the same name under two
        // parents stays two rows and the same path in two accounts is one.
        val rows = labels.groupBy { pathOf(it, byId) }
        val out = mutableListOf<DrawerMailbox>()
        rows.keys.sortedWith(pathOrder).forEach { path ->
            val group = rows.getValue(path)
            out += DrawerMailbox(
                role = null,
                name = group.first().name,
                title = path.joinToString("/"),
                depth = path.size - 1,
                unread = group.sumOf { it.unreadEmails },
                mailboxes = group,
            )
        }
        return out
    }

    /** The mailbox rows a destination stands for, for the store query. */
    fun mailboxesFor(
        destination: MailDestination.Folder,
        mailboxes: List<Mailbox>,
        accountScope: String? = null,
    ): List<Mailbox> = when (destination.role) {
        null -> labels(mailboxes, accountScope)
            .firstOrNull { it.title == destination.title }?.mailboxes.orEmpty()

        else -> folders(mailboxes, accountScope)
            .firstOrNull { it.role == destination.role }?.mailboxes.orEmpty()
    }

    /**
     * A destination as a string, so the shell's selection survives process
     * death in saved instance state.
     */
    fun key(destination: MailDestination): String = when (destination) {
        MailDestination.Inbox -> "inbox"
        MailDestination.Snoozed -> "snoozed"
        is MailDestination.Folder ->
            if (destination.role != null) "role:${destination.role}" else "label:${destination.title}"
    }

    /** The destination a [key] names; the inbox for anything unknown. */
    fun destination(key: String): MailDestination = when {
        key == "snoozed" -> MailDestination.Snoozed
        key.startsWith("role:") -> {
            val role = key.removePrefix("role:")
            val title = FOLDER_ROLES.firstOrNull { it.first == role }?.second ?: role
            MailDestination.Folder(role, title, title)
        }

        key.startsWith("label:") -> {
            val path = key.removePrefix("label:")
            MailDestination.Folder(null, path.substringAfterLast('/'), path)
        }

        else -> MailDestination.Inbox
    }

    /** The label's names from the root down, which is what nests the row. */
    private fun pathOf(label: Mailbox, byId: Map<Pair<String, String>, Mailbox>): List<String> {
        val path = ArrayDeque<String>()
        var current: Mailbox? = label
        val seen = mutableSetOf<String>()
        while (current != null && seen.add(current.id)) {
            path.addFirst(current.name)
            val parent = current.parentId ?: break
            current = byId[current.accountId to parent]
        }
        return path.toList()
    }

    /** Alphabetical within each parent, parents ahead of their children. */
    private val pathOrder = Comparator<List<String>> { left, right ->
        val shared = minOf(left.size, right.size)
        for (index in 0 until shared) {
            val order = left[index].compareTo(right[index], ignoreCase = true)
            if (order != 0) return@Comparator order
        }
        left.size - right.size
    }
}
