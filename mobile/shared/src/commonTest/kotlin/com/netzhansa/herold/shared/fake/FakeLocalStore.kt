package com.netzhansa.herold.shared.fake

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.domain.Thread
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

    override fun threadEmails(accountId: String, threadId: String): Flow<List<Email>> = emailRows.map { rows ->
        rows.filter { it.accountId == accountId && it.threadId == threadId }.sortedBy { it.receivedAt }
    }

    override suspend fun email(accountId: String, id: String): Email? =
        emailRows.value.firstOrNull { it.accountId == accountId && it.id == id }

    override suspend fun emailList(): List<Email> = emailRows.value

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

    override suspend fun syncState(accountId: String, type: String): String? = states[accountId to type]

    override suspend fun setSyncState(accountId: String, type: String, state: String?) {
        if (state == null) states.remove(accountId to type) else states[accountId to type] = state
    }

    override suspend fun cachedBlob(accountId: String, blobId: String): CachedBlob? = blobs[accountId to blobId]

    override suspend fun cacheBlob(accountId: String, blobId: String, contentType: String, bytes: ByteArray) {
        blobs[accountId to blobId] = CachedBlob(contentType, bytes)
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
        states.clear()
        blobs.clear()
        pushRow = null
    }
}
