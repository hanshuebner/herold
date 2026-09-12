package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * The snapshot an undo or a rejected drain restores: the membership each
 * message had before the action (suite REQ-OPT-02).
 */
data class ActionSnapshot(val emails: List<Email>)

/**
 * An action whose optimistic write is in the local store and whose
 * `Email/set` has not been queued yet. It exists so the UI can offer its
 * undo the moment the rows change rather than after the round trip: the
 * phone removes the row instantly, and an undo that waits for the server
 * is an undo the user never sees on a slow link (issue #338).
 */
class PendingAction internal constructor(
    /** What the messages looked like before the action; what an undo restores. */
    val snapshot: ActionSnapshot,
    /** What the outbox screen calls this entry. */
    val label: String,
    internal val optimistic: List<Email>,
    internal val patches: Map<String, JsonObject>,
) {
    /** True when the action changed nothing, so there is nothing to send. */
    val isEmpty: Boolean get() = patches.isEmpty()

    /** The outbox entries [MailActions.commit] queued, one per account. */
    val entryIds: MutableList<Long> = mutableListOf()
}

/**
 * Optimistic mail actions: star, read/unread, archive, label, snooze
 * (issue #327, suite REQ-OPT-01/02). Every action writes the intended
 * state into the local store and queues a durable outbox entry carrying
 * the `Email/set` it stands for (REQ-AND-SYNC-20); the sync engine's
 * drainer is the only thing that talks to the server. With a connection
 * the entry leaves at once, without one it waits, and a refusal the
 * server answered with puts the rows back and leaves the entry listed
 * with its reason (REQ-AND-SYNC-23).
 */
class MailActions(
    private val store: LocalStore,
    private val outbox: Outbox,
    /** Asks the sync engine for a drain; a no-op when nothing can run one. */
    private val requestDrain: () -> Unit = {},
) {
    suspend fun setFlagged(emails: List<Email>, flagged: Boolean) {
        keywordAction(emails, Keywords.FLAGGED, flagged, if (flagged) Labels.STAR else Labels.UNSTAR)
    }

    suspend fun setSeen(emails: List<Email>, seen: Boolean) {
        keywordAction(emails, Keywords.SEEN, seen, if (seen) Labels.MARK_READ else Labels.MARK_UNREAD)
    }

    suspend fun setCategory(emails: List<Email>, category: String) {
        val snapshot = ActionSnapshot(emails)
        val optimistic = emails.map { email ->
            val stripped = email.keywords.filterNot { it.startsWith(Keywords.CATEGORY_PREFIX, ignoreCase = true) }.toSet()
            email.copy(keywords = stripped + Keywords.categoryKeyword(category))
        }
        val patches = emails.associate { email ->
            email.id to buildJsonObject {
                email.keywords.filter { it.startsWith(Keywords.CATEGORY_PREFIX, ignoreCase = true) }.forEach {
                    put("keywords/$it", JsonPrimitive(null as String?))
                }
                put("keywords/${Keywords.categoryKeyword(category)}", true)
            }
        }
        apply(PendingAction(snapshot, Labels.CATEGORISE, optimistic, patches))
    }

    /** Adds or removes a label, which in the suite's model is a custom mailbox. */
    suspend fun setLabel(emails: List<Email>, label: Mailbox, applied: Boolean) {
        val snapshot = ActionSnapshot(emails)
        val optimistic = emails.map { email ->
            val ids = if (applied) email.mailboxIds + label.id else email.mailboxIds - label.id
            email.copy(mailboxIds = ids)
        }
        val patches = emails.associate { email ->
            email.id to buildJsonObject {
                if (applied) put("mailboxIds/${label.id}", true) else put("mailboxIds/${label.id}", JsonPrimitive(null as String?))
            }
        }
        apply(PendingAction(snapshot, Labels.LABEL, optimistic, patches))
    }

    /**
     * Archive: out of the inbox, into the archive mailbox when the account
     * has one. Returns the snapshot the undo snackbar restores.
     */
    suspend fun archive(emails: List<Email>, mailboxes: List<Mailbox>): ActionSnapshot {
        val pending = archiveLocally(emails, mailboxes)
        commit(pending)
        return pending.snapshot
    }

    /**
     * The local half of an archive: the rows leave the inbox in the store
     * at once and the outbox entry is left for [commit]. A caller that
     * shows an undo affordance uses this pair so the affordance appears
     * with the change (issue #338).
     */
    suspend fun archiveLocally(emails: List<Email>, mailboxes: List<Mailbox>): PendingAction {
        val snapshot = ActionSnapshot(emails)
        val optimistic = mutableListOf<Email>()
        val patches = mutableMapOf<String, JsonObject>()
        emails.forEach { email ->
            val inbox = mailboxes.firstOrNull { it.accountId == email.accountId && it.role == MailboxRoles.INBOX }
            val archive = mailboxes.firstOrNull { it.accountId == email.accountId && it.role == MailboxRoles.ARCHIVE }
            var ids = email.mailboxIds
            val patch = buildJsonObject {
                if (inbox != null && ids.contains(inbox.id)) {
                    put("mailboxIds/${inbox.id}", JsonPrimitive(null as String?))
                    ids = ids - inbox.id
                }
                if (archive != null && !ids.contains(archive.id)) {
                    put("mailboxIds/${archive.id}", true)
                    ids = ids + archive.id
                }
            }
            if (patch.isNotEmpty()) {
                optimistic.add(email.copy(mailboxIds = ids))
                patches[email.id] = patch
            }
        }
        optimistic.forEach { write(it) }
        return PendingAction(snapshot, Labels.ARCHIVE, optimistic, patches)
    }

    /** Queues what [archiveLocally] or [snoozeLocally] wrote. */
    suspend fun commit(pending: PendingAction) {
        if (pending.isEmpty) return
        enqueue(pending)
    }

    /**
     * Takes an action back. An entry the drain has not reached yet is
     * simply dropped and the rows put back, so an undo with no
     * connectivity costs no round trip; once the server has the change,
     * the inverse `Email/set` is queued instead.
     */
    suspend fun undo(pending: PendingAction) {
        val cancelled = pending.entryIds.count { outbox.cancelIfQueued(it) != null }
        if (cancelled > 0 && cancelled == pending.entryIds.size) {
            pending.snapshot.emails.forEach { write(it) }
            return
        }
        restore(pending.snapshot)
    }

    /** Puts the messages back exactly as they were before an action. */
    suspend fun restore(snapshot: ActionSnapshot) {
        val current = snapshot.emails.mapNotNull { store.email(it.accountId, it.id) }
        val patches = snapshot.emails.associate { original ->
            val live = current.firstOrNull { it.id == original.id } ?: original
            original.id to buildJsonObject {
                (live.mailboxIds - original.mailboxIds).forEach {
                    put("mailboxIds/$it", JsonPrimitive(null as String?))
                }
                (original.mailboxIds - live.mailboxIds).forEach { put("mailboxIds/$it", true) }
                (live.keywords - original.keywords).forEach {
                    put("keywords/$it", JsonPrimitive(null as String?))
                }
                (original.keywords - live.keywords).forEach { put("keywords/$it", true) }
                if (live.snoozedUntil != original.snoozedUntil) {
                    if (original.snoozedUntil == null) {
                        put("snoozedUntil", JsonPrimitive(null as String?))
                    } else {
                        put("snoozedUntil", original.snoozedUntil)
                    }
                }
            }
        }.filterValues { it.isNotEmpty() }
        if (patches.isEmpty()) return
        apply(PendingAction(ActionSnapshot(current), Labels.UNDO, snapshot.emails, patches))
    }

    /**
     * Snooze: `snoozedUntil` plus the `$snoozed` keyword, which herold keeps
     * as an atomic pair (`internal/protojmap/mail/email/set.go`), so the
     * client sends only `snoozedUntil`.
     *
     * The wake destination is left to the server, which resolves the
     * account's inbox at snooze time (`snoozeWakeMailboxId`, issue #274) -
     * the same destination the suite's picker preselects.
     */
    suspend fun snooze(emails: List<Email>, wakeAt: String) = commit(snoozeLocally(emails, wakeAt))

    /**
     * The local half of a snooze, the counterpart of [archiveLocally]: the
     * rows carry the wake time in the store at once and the outbox entry
     * is left for [commit] (issue #345).
     */
    suspend fun snoozeLocally(emails: List<Email>, wakeAt: String): PendingAction {
        val snapshot = ActionSnapshot(emails)
        val optimistic = emails.map {
            it.copy(snoozedUntil = wakeAt, keywords = it.keywords + Keywords.SNOOZED)
        }
        val patches = emails.associate { email ->
            email.id to buildJsonObject { put("snoozedUntil", wakeAt) }
        }
        optimistic.forEach { write(it) }
        return PendingAction(snapshot, Labels.SNOOZE, optimistic, patches)
    }

    /**
     * Cancels a snooze: the wake time goes, and herold clears the `$snoozed`
     * keyword with it, so the conversation is back in the inbox at once
     * (suite REQ-SNZ-12).
     */
    suspend fun unsnooze(emails: List<Email>) {
        val target = emails.filter { it.snoozedUntil != null || it.isSnoozed }
        if (target.isEmpty()) return
        val snapshot = ActionSnapshot(target)
        val optimistic = target.map { email ->
            email.copy(
                snoozedUntil = null,
                keywords = email.keywords.filterNot { it.equals(Keywords.SNOOZED, ignoreCase = true) }.toSet(),
            )
        }
        val patches = target.associate { email ->
            email.id to buildJsonObject { put("snoozedUntil", JsonPrimitive(null as String?)) }
        }
        apply(PendingAction(snapshot, Labels.UNSNOOZE, optimistic, patches))
    }

    private suspend fun keywordAction(
        emails: List<Email>,
        keyword: String,
        present: Boolean,
        label: String,
    ) {
        val target = emails.filter { it.keywords.contains(keyword) != present }
        if (target.isEmpty()) return
        val snapshot = ActionSnapshot(target)
        val optimistic = target.map {
            it.copy(keywords = if (present) it.keywords + keyword else it.keywords - keyword)
        }
        val patches = target.associate { email ->
            email.id to buildJsonObject {
                if (present) put("keywords/$keyword", true) else put("keywords/$keyword", JsonPrimitive(null as String?))
            }
        }
        apply(PendingAction(snapshot, label, optimistic, patches))
    }

    /** Writes the optimistic rows and queues the change behind them. */
    private suspend fun apply(pending: PendingAction) {
        pending.optimistic.forEach { write(it) }
        enqueue(pending)
    }

    /**
     * One outbox entry per account the action touched, so each account's
     * queue stays a single ordered stream.
     */
    private suspend fun enqueue(pending: PendingAction) {
        val byAccount = pending.optimistic.groupBy { it.accountId }
        for ((accountId, accountEmails) in byAccount) {
            val patches = accountEmails.mapNotNull { email ->
                pending.patches[email.id]?.let { email.id to it }
            }.toMap()
            if (patches.isEmpty()) continue
            val ids = patches.keys
            val id = outbox.enqueueAction(
                accountId = accountId,
                label = pending.label,
                patches = patches,
                snapshot = pending.snapshot.emails.filter { it.accountId == accountId && it.id in ids },
            )
            pending.entryIds.add(id)
        }
        requestDrain()
    }

    private suspend fun write(email: Email) {
        store.updateMembership(
            accountId = email.accountId,
            id = email.id,
            keywords = email.keywords,
            mailboxIds = email.mailboxIds,
            snoozedUntil = email.snoozedUntil,
        )
    }

    /** What the outbox screen calls each kind of action. */
    object Labels {
        const val ARCHIVE = "Archive"
        const val SNOOZE = "Snooze"
        const val UNSNOOZE = "Unsnooze"
        const val STAR = "Star"
        const val UNSTAR = "Unstar"
        const val MARK_READ = "Mark read"
        const val MARK_UNREAD = "Mark unread"
        const val LABEL = "Labels"
        const val CATEGORISE = "Category"
        const val UNDO = "Undo"
        const val FILTER_CREATE = "New filter"
        const val FILTER_UPDATE = "Filter"
        const val FILTER_ENABLE = "Enable filter"
        const val FILTER_DISABLE = "Disable filter"
        const val FILTER_REORDER = "Filter order"
        const val FILTER_DELETE = "Delete filter"
        const val MUTE = "Mute conversation"
        const val UNMUTE = "Unmute conversation"
        const val BLOCK = "Block sender"
    }
}
