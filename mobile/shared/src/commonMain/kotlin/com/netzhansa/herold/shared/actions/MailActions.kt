package com.netzhansa.herold.shared.actions

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.store.LocalStore
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** Outcome of an optimistic action (suite REQ-OPT-02). */
sealed interface ActionResult {
    data object Applied : ActionResult

    /** The server rejected it or was unreachable; the local state was put back. */
    data class Reverted(val message: String, val offline: Boolean) : ActionResult
}

/**
 * The snapshot an undo restores: the membership each message had before the
 * action (suite REQ-OPT-02, undo snackbar on archive).
 */
data class ActionSnapshot(val emails: List<Email>)

/**
 * Optimistic mail actions: star, read/unread, archive, label, snooze
 * (issue #327, suite REQ-OPT-01/02). Each action writes the intended state
 * into the local store first so the UI reflects it at once, then sends the
 * `Email/set`. A rejection or a dead connection reverts the store rows and
 * reports why; milestone 1a queues nothing - there is no durable outbox, so
 * an action without connectivity fails visibly.
 */
class MailActions(
    private val api: JmapApi,
    private val store: LocalStore,
) {
    suspend fun setFlagged(emails: List<Email>, flagged: Boolean): ActionResult =
        keywordAction(emails, Keywords.FLAGGED, flagged)

    suspend fun setSeen(emails: List<Email>, seen: Boolean): ActionResult =
        keywordAction(emails, Keywords.SEEN, seen)

    suspend fun setCategory(emails: List<Email>, category: String): ActionResult {
        val snapshot = ActionSnapshot(emails)
        val optimistic = emails.map { email ->
            val stripped = email.keywords.filterNot { it.startsWith(Keywords.CATEGORY_PREFIX) }.toSet()
            email.copy(keywords = stripped + Keywords.categoryKeyword(category))
        }
        val patches = emails.associate { email ->
            email.id to buildJsonObject {
                email.keywords.filter { it.startsWith(Keywords.CATEGORY_PREFIX) }.forEach {
                    put("keywords/$it", JsonPrimitive(null as String?))
                }
                put("keywords/${Keywords.categoryKeyword(category)}", true)
            }
        }
        return apply(snapshot, optimistic, patches)
    }

    /** Adds or removes a label, which in the suite's model is a custom mailbox. */
    suspend fun setLabel(emails: List<Email>, label: Mailbox, applied: Boolean): ActionResult {
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
        return apply(snapshot, optimistic, patches)
    }

    /**
     * Archive: out of the inbox, into the archive mailbox when the account
     * has one. Returns the snapshot the undo snackbar restores.
     */
    suspend fun archive(emails: List<Email>, mailboxes: List<Mailbox>): Pair<ActionResult, ActionSnapshot> {
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
        if (patches.isEmpty()) return ActionResult.Applied to snapshot
        return apply(snapshot, optimistic, patches) to snapshot
    }

    /** Puts the messages back exactly as they were before an action. */
    suspend fun restore(snapshot: ActionSnapshot): ActionResult {
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
        if (patches.isEmpty()) return ActionResult.Applied
        return apply(ActionSnapshot(current), snapshot.emails, patches)
    }

    /**
     * Snooze: `snoozedUntil` plus the `$snoozed` keyword, which herold keeps
     * as an atomic pair (`internal/protojmap/mail/email/set.go`), so the
     * client sends only `snoozedUntil`.
     */
    suspend fun snooze(emails: List<Email>, wakeAt: String): ActionResult {
        val snapshot = ActionSnapshot(emails)
        val optimistic = emails.map {
            it.copy(snoozedUntil = wakeAt, keywords = it.keywords + Keywords.SNOOZED)
        }
        val patches = emails.associate { email ->
            email.id to buildJsonObject { put("snoozedUntil", wakeAt) }
        }
        return apply(snapshot, optimistic, patches)
    }

    private suspend fun keywordAction(emails: List<Email>, keyword: String, present: Boolean): ActionResult {
        val target = emails.filter { it.keywords.contains(keyword) != present }
        if (target.isEmpty()) return ActionResult.Applied
        val snapshot = ActionSnapshot(target)
        val optimistic = target.map {
            it.copy(keywords = if (present) it.keywords + keyword else it.keywords - keyword)
        }
        val patches = target.associate { email ->
            email.id to buildJsonObject {
                if (present) put("keywords/$keyword", true) else put("keywords/$keyword", JsonPrimitive(null as String?))
            }
        }
        return apply(snapshot, optimistic, patches)
    }

    private suspend fun apply(
        snapshot: ActionSnapshot,
        optimistic: List<Email>,
        patches: Map<String, JsonObject>,
    ): ActionResult {
        optimistic.forEach { write(it) }

        val byAccount = optimistic.groupBy { it.accountId }
        val rejected = mutableMapOf<String, String>()
        for ((accountId, accountEmails) in byAccount) {
            val accountPatches = accountEmails.mapNotNull { email ->
                patches[email.id]?.let { email.id to it }
            }.toMap()
            if (accountPatches.isEmpty()) continue
            val outcome = try {
                api.emailSet(accountId, accountPatches)
            } catch (e: JmapException) {
                revert(snapshot)
                return ActionResult.Reverted(
                    message = e.message ?: "the server rejected the change",
                    offline = false,
                )
            } catch (t: Throwable) {
                revert(snapshot)
                return ActionResult.Reverted(
                    message = "No connection - the change was not saved",
                    offline = true,
                )
            }
            rejected.putAll(outcome.notUpdated)
        }
        if (rejected.isNotEmpty()) {
            revert(snapshot)
            return ActionResult.Reverted(
                message = rejected.values.first(),
                offline = false,
            )
        }
        return ActionResult.Applied
    }

    private suspend fun revert(snapshot: ActionSnapshot) {
        snapshot.emails.forEach { write(it) }
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
}
