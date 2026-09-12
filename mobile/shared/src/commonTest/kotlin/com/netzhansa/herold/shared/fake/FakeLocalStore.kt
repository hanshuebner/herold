package com.netzhansa.herold.shared.fake

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.Thread
import com.netzhansa.herold.shared.outbox.NewOutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.store.CachedBlob
import com.netzhansa.herold.shared.store.LocalStore
import com.netzhansa.herold.shared.store.PushRegistration
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.map

/**
 * In-memory [LocalStore] for the reconciliation and action tests. It holds
 * the same invariants the SQLDelight store does (rows keyed by account id,
 * a metadata refresh keeps a cached body) so a test that passes here
 * exercises the logic the device runs.
 */
class FakeLocalStore : LocalStore {
    private val accountRows = MutableStateFlow<List<Account>>(emptyList())
    private val mailboxRows = MutableStateFlow<List<Mailbox>>(emptyList())
    private val emailRows = MutableStateFlow<List<Email>>(emptyList())
    private val threadRows = MutableStateFlow<List<Thread>>(emptyList())
    private val identityRows = MutableStateFlow<List<Identity>>(emptyList())
    private val ruleRows = MutableStateFlow<List<ManagedRule>>(emptyList())
    private val states = mutableMapOf<Pair<String, String>, String>()
    private val blobs = mutableMapOf<Pair<String, String>, CachedBlob>()

    override fun accounts(): Flow<List<Account>> = accountRows

    override suspend fun accountList(): List<Account> = accountRows.value

    override suspend fun replaceAccounts(accounts: List<Account>) {
        accountRows.value = accounts
    }

    override fun mailboxes(): Flow<List<Mailbox>> = mailboxRows

    override suspend fun mailboxList(): List<Mailbox> = mailboxRows.value

    override suspend fun upsertMailboxes(rows: List<Mailbox>) {
        val byKey = mailboxRows.value.associateBy { it.accountId to it.id }.toMutableMap()
        rows.forEach { byKey[it.accountId to it.id] = it }
        mailboxRows.value = byKey.values.toList()
    }

    override suspend fun deleteMailboxes(accountId: String, ids: List<String>) {
        mailboxRows.value = mailboxRows.value.filterNot { it.accountId == accountId && it.id in ids }
    }

    override suspend fun clearMailboxes(accountId: String) {
        mailboxRows.value = mailboxRows.value.filterNot { it.accountId == accountId }
    }

    override fun inboxEmails(limit: Long): Flow<List<Email>> = emailRows.map { rows ->
        val inboxIds = mailboxRows.value.filter { it.role == MailboxRoles.INBOX }
            .map { it.accountId to it.id }.toSet()
        rows.filter { email -> email.mailboxIds.any { (email.accountId to it) in inboxIds } }
            .sortedByDescending { it.receivedAt }
            .take(limit.toInt())
    }

    override fun snoozedEmails(limit: Long): Flow<List<Email>> = emailRows.map { rows ->
        rows.filter { it.snoozedUntil != null }
            .sortedBy { it.snoozedUntil }
            .take(limit.toInt())
    }

    override fun threadEmails(accountId: String, threadId: String): Flow<List<Email>> = emailRows.map { rows ->
        rows.filter { it.accountId == accountId && it.threadId == threadId }.sortedBy { it.receivedAt }
    }

    override suspend fun threadEmailList(accountId: String, threadId: String): List<Email> =
        emailRows.value.filter { it.accountId == accountId && it.threadId == threadId }
            .sortedBy { it.receivedAt }

    override suspend fun email(accountId: String, id: String): Email? =
        emailRows.value.firstOrNull { it.accountId == accountId && it.id == id }

    override suspend fun emailList(): List<Email> = emailRows.value

    override suspend fun searchCached(query: String, limit: Long): List<Email> {
        val needle = query.trim().lowercase()
        return emailRows.value
            .filter { row ->
                row.fromName.lowercase().contains(needle) ||
                    row.fromEmail.lowercase().contains(needle) ||
                    row.subject.lowercase().contains(needle) ||
                    row.preview.lowercase().contains(needle)
            }
            .sortedByDescending { it.receivedAt }
            .take(limit.toInt())
    }

    override suspend fun upsertEmails(rows: List<Email>) {
        val byKey = emailRows.value.associateBy { it.accountId to it.id }.toMutableMap()
        rows.forEach { incoming ->
            val existing = byKey[incoming.accountId to incoming.id]
            byKey[incoming.accountId to incoming.id] = incoming.copy(
                bodyHtml = incoming.bodyHtml ?: existing?.bodyHtml,
                bodyText = incoming.bodyText ?: existing?.bodyText,
                attachments = incoming.attachments.ifEmpty { existing?.attachments ?: emptyList() },
            )
        }
        emailRows.value = byKey.values.toList()
    }

    override suspend fun deleteEmails(accountId: String, ids: List<String>) {
        emailRows.value = emailRows.value.filterNot { it.accountId == accountId && it.id in ids }
    }

    override suspend fun clearEmails(accountId: String) {
        emailRows.value = emailRows.value.filterNot { it.accountId == accountId }
    }

    override suspend fun updateMembership(
        accountId: String,
        id: String,
        keywords: Set<String>,
        mailboxIds: Set<String>,
        snoozedUntil: String?,
    ) {
        emailRows.value = emailRows.value.map { row ->
            if (row.accountId == accountId && row.id == id) {
                row.copy(keywords = keywords, mailboxIds = mailboxIds, snoozedUntil = snoozedUntil)
            } else {
                row
            }
        }
    }

    override suspend fun storeBody(
        accountId: String,
        id: String,
        html: String?,
        text: String?,
        attachments: List<Attachment>,
        fetchedAt: Long,
    ) {
        emailRows.value = emailRows.value.map { row ->
            if (row.accountId == accountId && row.id == id) {
                row.copy(bodyHtml = html, bodyText = text, attachments = attachments)
            } else {
                row
            }
        }
    }

    override suspend fun thread(accountId: String, id: String): Thread? =
        threadRows.value.firstOrNull { it.accountId == accountId && it.id == id }

    override suspend fun upsertThreads(rows: List<Thread>) {
        val byKey = threadRows.value.associateBy { it.accountId to it.id }.toMutableMap()
        rows.forEach { byKey[it.accountId to it.id] = it }
        threadRows.value = byKey.values.toList()
    }

    override suspend fun deleteThreads(accountId: String, ids: List<String>) {
        threadRows.value = threadRows.value.filterNot { it.accountId == accountId && it.id in ids }
    }

    override suspend fun clearThreads(accountId: String) {
        threadRows.value = threadRows.value.filterNot { it.accountId == accountId }
    }

    override fun identities(): Flow<List<Identity>> = identityRows

    override suspend fun upsertIdentities(rows: List<Identity>) {
        val byKey = identityRows.value.associateBy { it.accountId to it.id }.toMutableMap()
        rows.forEach { byKey[it.accountId to it.id] = it }
        identityRows.value = byKey.values.toList()
    }

    override suspend fun deleteIdentities(accountId: String, ids: List<String>) {
        identityRows.value = identityRows.value.filterNot { it.accountId == accountId && it.id in ids }
    }

    override suspend fun clearIdentities(accountId: String) {
        identityRows.value = identityRows.value.filterNot { it.accountId == accountId }
    }

    override fun managedRules(): Flow<List<ManagedRule>> = ruleRows.map { rows ->
        rows.sortedWith(compareBy({ it.order }, { it.id }))
    }

    override suspend fun managedRuleList(): List<ManagedRule> =
        ruleRows.value.sortedWith(compareBy({ it.order }, { it.id }))

    override suspend fun upsertManagedRules(rows: List<ManagedRule>) {
        val byKey = ruleRows.value.associateBy { it.accountId to it.id }.toMutableMap()
        rows.forEach { byKey[it.accountId to it.id] = it }
        ruleRows.value = byKey.values.toList()
    }

    override suspend fun deleteManagedRules(accountId: String, ids: List<String>) {
        ruleRows.value = ruleRows.value.filterNot { it.accountId == accountId && it.id in ids }
    }

    override suspend fun clearManagedRules(accountId: String) {
        ruleRows.value = ruleRows.value.filterNot { it.accountId == accountId }
    }

    override suspend fun syncState(accountId: String, type: String): String? = states[accountId to type]

    override suspend fun setSyncState(accountId: String, type: String, state: String?) {
        if (state == null) states.remove(accountId to type) else states[accountId to type] = state
    }

    override suspend fun cachedBlob(accountId: String, blobId: String): CachedBlob? = blobs[accountId to blobId]

    override suspend fun cacheBlob(accountId: String, blobId: String, contentType: String, bytes: ByteArray) {
        blobs[accountId to blobId] = CachedBlob(contentType, bytes)
    }

    private val outboxRows = MutableStateFlow<List<OutboxEntry>>(emptyList())
    private var nextOutboxId = 1L

    override fun outbox(): Flow<List<OutboxEntry>> = outboxRows

    override suspend fun outboxList(): List<OutboxEntry> = outboxRows.value

    override suspend fun outboxEntry(id: Long): OutboxEntry? = outboxRows.value.firstOrNull { it.id == id }

    override suspend fun enqueueOutbox(entry: NewOutboxEntry): Long {
        val id = nextOutboxId++
        outboxRows.value = outboxRows.value + OutboxEntry(
            id = id,
            accountId = entry.accountId,
            kind = entry.kind,
            label = entry.label,
            payload = entry.payload,
            revertJson = entry.revertJson,
            entityIds = entry.entityIds,
            createdAt = entry.createdAt,
            state = OutboxState.QUEUED,
            attempts = 0,
            lastError = null,
            permanent = false,
            nextAttemptAt = 0,
        )
        return id
    }

    override suspend fun updateOutboxState(
        id: Long,
        state: OutboxState,
        attempts: Int,
        lastError: String?,
        permanent: Boolean,
        nextAttemptAt: Long,
    ) {
        outboxRows.value = outboxRows.value.map { row ->
            if (row.id != id) {
                row
            } else {
                row.copy(
                    state = state,
                    attempts = attempts,
                    lastError = lastError,
                    permanent = permanent,
                    nextAttemptAt = nextAttemptAt,
                )
            }
        }
    }

    override suspend fun updateOutboxPayload(id: Long, payload: String) {
        outboxRows.value = outboxRows.value.map { if (it.id == id) it.copy(payload = payload) else it }
    }

    override suspend fun deleteOutbox(id: Long) {
        outboxRows.value = outboxRows.value.filterNot { it.id == id }
    }

    var pushRow: PushRegistration? = null

    override suspend fun pushRegistration(): PushRegistration? = pushRow

    override suspend fun setPushRegistration(registration: PushRegistration?) {
        pushRow = registration
    }

    override suspend fun blobCacheSize(): Long = blobs.values.sumOf { it.bytes.size.toLong() }

    override suspend fun clearAll() {
        accountRows.value = emptyList()
        mailboxRows.value = emptyList()
        emailRows.value = emptyList()
        threadRows.value = emptyList()
        identityRows.value = emptyList()
        ruleRows.value = emptyList()
        states.clear()
        blobs.clear()
        outboxRows.value = emptyList()
        pushRow = null
    }
}
