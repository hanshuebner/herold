package com.netzhansa.herold.shared.store

import app.cash.sqldelight.coroutines.asFlow
import app.cash.sqldelight.coroutines.mapToList
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.CategoryDisposition
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.outbox.NewOutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxEntry
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.OutboxState
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import com.netzhansa.herold.shared.domain.Account as DomainAccount
import com.netzhansa.herold.shared.domain.Email as DomainEmail
import com.netzhansa.herold.shared.domain.Identity as DomainIdentity
import com.netzhansa.herold.shared.domain.Mailbox as DomainMailbox
import com.netzhansa.herold.shared.domain.ManagedRule as DomainManagedRule
import com.netzhansa.herold.shared.domain.RuleAction as DomainRuleAction
import com.netzhansa.herold.shared.domain.RuleCondition as DomainRuleCondition
import com.netzhansa.herold.shared.domain.Thread as DomainThread

@Serializable
private data class AttachmentDto(
    val blobId: String,
    val name: String,
    val type: String,
    val size: Long,
    val cid: String? = null,
    val isInline: Boolean = false,
)

@Serializable
private data class AddressDto(val name: String? = null, val email: String)

@Serializable
private data class ConditionDto(val field: String, val op: String, val value: String = "")

@Serializable
private data class ActionDto(val kind: String, val params: Map<String, String> = emptyMap())

private val attachmentJson = Json { ignoreUnknownKeys = true }

/**
 * SQLDelight-backed [LocalStore]: the persistent source of truth the UI
 * renders from (REQ-AND-SYNC-01/02). Reads are `Flow`s over SQLDelight
 * queries, so a sync-engine write recomposes the affected screens without
 * the UI polling.
 *
 * @param blobFiles where cached blob bytes are held; the `blob_cache` row
 *   keeps the metadata and the file's path (issue #420).
 * @param blobBudgetBytes the blob cache's size budget; a write over budget
 *   evicts least-recently-used blobs until it fits (REQ-AND-SYNC-12).
 */
class SqlDelightLocalStore(
    private val database: HeroldDatabase,
    private val blobFiles: BlobFileStore,
    private val dispatcher: CoroutineDispatcher = Dispatchers.Default,
    private val blobBudgetBytes: Long = DEFAULT_BLOB_BUDGET_BYTES,
    private val now: () -> Long = { 0L },
    /**
     * The rows the client has taken away, held against the fetches
     * still on their way in (issue #371).
     */
    private val tombstones: Tombstones = Tombstones(now),
    /**
     * The rows an optimistic action changed, held against the fetches
     * still on their way in (issue #473).
     */
    private val membershipHolds: MembershipHolds = MembershipHolds(now),
) : LocalStore {

    override fun accounts(): Flow<List<DomainAccount>> =
        database.accountQueries.selectAll().asFlow().mapToList(dispatcher)
            .let { flow -> flow.mapList { it.toDomain() } }

    override suspend fun accountList(): List<DomainAccount> = withContext(dispatcher) {
        database.accountQueries.selectAll().executeAsList().map { it.toDomain() }
    }

    override suspend fun replaceAccounts(accounts: List<DomainAccount>) = withContext(dispatcher) {
        database.transaction {
            database.accountQueries.deleteAll()
            accounts.forEachIndexed { index, account ->
                database.accountQueries.upsert(
                    id = account.id,
                    name = account.name,
                    isPrimary = if (account.isPrimary) 1L else 0L,
                    isPersonal = if (account.isPersonal) 1L else 0L,
                    sortOrder = index.toLong(),
                )
            }
        }
    }

    override fun mailboxes(): Flow<List<DomainMailbox>> =
        database.mailboxQueries.selectAll().asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override suspend fun mailboxList(): List<DomainMailbox> = withContext(dispatcher) {
        database.mailboxQueries.selectAll().executeAsList().map { it.toDomain() }
    }

    override suspend fun upsertMailboxes(rows: List<DomainMailbox>) = withContext(dispatcher) {
        database.transaction {
            rows.forEach { mailbox ->
                database.mailboxQueries.upsert(
                    accountId = mailbox.accountId,
                    id = mailbox.id,
                    name = mailbox.name,
                    role = mailbox.role,
                    parentId = mailbox.parentId,
                    sortOrder = mailbox.sortOrder.toLong(),
                    totalEmails = mailbox.totalEmails.toLong(),
                    unreadEmails = mailbox.unreadEmails.toLong(),
                    disposition = mailbox.disposition.wire,
                    priority = mailbox.priority?.toLong(),
                )
            }
        }
    }

    override suspend fun deleteMailboxes(accountId: String, ids: List<String>) = withContext(dispatcher) {
        database.transaction { ids.forEach { database.mailboxQueries.deleteById(accountId, it) } }
    }

    override suspend fun clearMailboxes(accountId: String) = withContext(dispatcher) {
        database.mailboxQueries.deleteForAccount(accountId)
        Unit
    }

    override fun inboxEmails(limit: Long): Flow<List<DomainEmail>> =
        database.emailQueries.selectInbox(limit).asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override fun draftEmails(limit: Long): Flow<List<DomainEmail>> =
        database.emailQueries.selectDrafts(limit).asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override fun mailboxEmails(mailboxIds: Collection<String>, limit: Long): Flow<List<DomainEmail>> =
        if (mailboxIds.isEmpty()) {
            flowOf(emptyList())
        } else {
            database.emailQueries.selectInMailboxes(mailboxIds, limit).asFlow().mapToList(dispatcher)
                .mapList { it.toDomain() }
        }

    override fun snoozedEmails(limit: Long): Flow<List<DomainEmail>> =
        database.emailQueries.selectSnoozed(limit).asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override fun threadEmails(accountId: String, threadId: String): Flow<List<DomainEmail>> =
        database.emailQueries.selectByThread(accountId, threadId).asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override suspend fun threadEmailList(accountId: String, threadId: String): List<DomainEmail> =
        withContext(dispatcher) {
            database.emailQueries.selectByThread(accountId, threadId).executeAsList().map { it.toDomain() }
        }

    override suspend fun email(accountId: String, id: String): DomainEmail? = withContext(dispatcher) {
        database.emailQueries.selectById(accountId, id).executeAsOneOrNull()?.toDomain()
    }

    override suspend fun emailList(): List<DomainEmail> = withContext(dispatcher) {
        database.emailQueries.selectAll().executeAsList().map { it.toDomain() }
    }

    override suspend fun searchCached(query: String, limit: Long): List<DomainEmail> =
        withContext(dispatcher) {
            val pattern = "%" + query.escapeForLike() + "%"
            database.emailQueries.searchCached(pattern, limit).executeAsList().map { it.toDomain() }
        }

    override suspend fun upsertEmails(rows: List<DomainEmail>) {
        // A row the user took away stays away, whatever a fetch that
        // read it before the delete is carrying (issue #371).
        val writable = writable(rows)
        if (writable.isEmpty()) return
        // A row an action changed keeps that membership until the
        // action's outbox entry drains, whatever a response in flight
        // before the action is carrying (issue #473).
        val held = heldForMembership(writable)
        withContext(dispatcher) {
            database.transaction {
                writable.forEach { incoming ->
                    val email = if (incoming.id in held.getValue(incoming.accountId)) frozen(incoming) else incoming
                    database.emailQueries.insertIfAbsent(email.accountId, email.id, email.threadId)
                    database.emailQueries.updateMeta(
                        threadId = email.threadId,
                        blobId = email.blobId,
                        fromName = email.fromName,
                        fromEmail = email.fromEmail,
                        toLine = email.toLine,
                        toJson = email.toAddresses.encodeAddresses(),
                        ccJson = email.ccAddresses.encodeAddresses(),
                        messageIdJson = email.messageId.encodeStrings(),
                        inReplyToJson = email.inReplyTo.encodeStrings(),
                        referencesJson = email.references.encodeStrings(),
                        deliveredTo = email.deliveredTo,
                        listUnsubscribe = email.listUnsubscribe,
                        listUnsubscribePost = email.listUnsubscribePost,
                        subject = email.subject,
                        preview = email.preview,
                        receivedAt = email.receivedAt,
                        size = email.size,
                        hasAttachment = if (email.hasAttachment) 1L else 0L,
                        snoozedUntil = email.snoozedUntil,
                        keywords = email.keywords.joinToString(" "),
                        mailboxIds = email.mailboxIds.joinToString(" "),
                        accountId = email.accountId,
                        id = email.id,
                    )
                    writeMembership(email.accountId, email.id, email.mailboxIds)
                }
            }
        }
    }

    /** [rows] less the ones a local delete is still holding away. */
    private suspend fun writable(rows: List<DomainEmail>): List<DomainEmail> {
        if (rows.isEmpty()) return rows
        val byAccount = rows.groupBy { it.accountId }
        val held = byAccount.mapValues { (accountId, accountRows) ->
            tombstones.heldOf(accountId, accountRows.map { it.id })
        }
        return rows.filterNot { it.id in held.getValue(it.accountId) }
    }

    /** Of [rows], the ids an action still in flight is holding (issue #473). */
    private suspend fun heldForMembership(rows: List<DomainEmail>): Map<String, Set<String>> {
        val byAccount = rows.groupBy { it.accountId }
        return byAccount.mapValues { (accountId, accountRows) ->
            membershipHolds.heldOf(accountId, accountRows.map { it.id })
        }
    }

    /**
     * [incoming] with its membership, keywords and `snoozedUntil` replaced
     * by what the store currently holds, so an action still in flight is
     * not undone by this response (issue #473). Everything else of
     * [incoming] - subject, preview, a refreshed body - still applies.
     */
    private fun frozen(incoming: DomainEmail): DomainEmail {
        val current = database.emailQueries.selectById(incoming.accountId, incoming.id).executeAsOneOrNull()
            ?: return incoming
        return incoming.copy(
            keywords = current.keywords.splitTokens(),
            mailboxIds = current.mailboxIds.splitTokens(),
            snoozedUntil = current.snoozedUntil,
        )
    }

    override suspend fun holdMembership(accountId: String, ids: Collection<String>) {
        membershipHolds.mark(accountId, ids)
    }

    override suspend fun releaseMembershipHold(accountId: String, ids: Collection<String>) {
        membershipHolds.forget(accountId, ids)
    }

    override suspend fun deleteEmails(accountId: String, ids: List<String>) {
        tombstones.mark(accountId, ids)
        withContext(dispatcher) {
            database.transaction {
                ids.forEach {
                    database.emailQueries.deleteById(accountId, it)
                    database.emailQueries.deleteMembership(accountId, it)
                }
            }
        }
    }

    override suspend fun clearEmails(accountId: String) = withContext(dispatcher) {
        database.transaction {
            database.emailQueries.deleteForAccount(accountId)
            database.emailQueries.deleteMembershipForAccount(accountId)
        }
    }

    override suspend fun updateMembership(
        accountId: String,
        id: String,
        keywords: Set<String>,
        mailboxIds: Set<String>,
        snoozedUntil: String?,
    ) = withContext(dispatcher) {
        database.transaction {
            database.emailQueries.updateMembership(
                keywords = keywords.joinToString(" "),
                mailboxIds = mailboxIds.joinToString(" "),
                snoozedUntil = snoozedUntil,
                accountId = accountId,
                id = id,
            )
            writeMembership(accountId, id, mailboxIds)
        }
    }

    override suspend fun storeBody(
        accountId: String,
        id: String,
        html: String?,
        text: String?,
        attachments: List<Attachment>,
        fetchedAt: Long,
    ) = withContext(dispatcher) {
        database.emailQueries.storeBody(
            bodyHtml = html,
            bodyText = text,
            attachmentsJson = attachmentJson.encodeToString(attachments.map { it.toDto() }),
            bodyFetchedAt = fetchedAt,
            accountId = accountId,
            id = id,
        )
        Unit
    }

    override suspend fun thread(accountId: String, id: String): DomainThread? = withContext(dispatcher) {
        database.threadQueries.selectById(accountId, id).executeAsOneOrNull()?.toDomain()
    }

    override suspend fun upsertThreads(rows: List<DomainThread>) = withContext(dispatcher) {
        database.transaction {
            rows.forEach {
                database.threadQueries.upsert(it.accountId, it.id, it.emailIds.joinToString(" "))
            }
        }
    }

    override suspend fun deleteThreads(accountId: String, ids: List<String>) = withContext(dispatcher) {
        database.transaction { ids.forEach { database.threadQueries.deleteById(accountId, it) } }
    }

    override suspend fun clearThreads(accountId: String) = withContext(dispatcher) {
        database.threadQueries.deleteForAccount(accountId)
        Unit
    }

    override fun identities(): Flow<List<DomainIdentity>> =
        database.identityQueries.selectAll().asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override suspend fun upsertIdentities(rows: List<DomainIdentity>) = withContext(dispatcher) {
        database.transaction {
            rows.forEach {
                database.identityQueries.upsert(
                    accountId = it.accountId,
                    id = it.id,
                    name = it.name,
                    email = it.email,
                    mayDelete = if (it.mayDelete) 1L else 0L,
                    isDefault = if (it.isDefault) 1L else 0L,
                )
            }
        }
    }

    override suspend fun deleteIdentities(accountId: String, ids: List<String>) = withContext(dispatcher) {
        database.transaction { ids.forEach { database.identityQueries.deleteById(accountId, it) } }
    }

    override suspend fun clearIdentities(accountId: String) = withContext(dispatcher) {
        database.identityQueries.deleteForAccount(accountId)
        Unit
    }

    override fun managedRules(): Flow<List<DomainManagedRule>> =
        database.managedRuleQueries.selectAll().asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override suspend fun managedRuleList(): List<DomainManagedRule> = withContext(dispatcher) {
        database.managedRuleQueries.selectAll().executeAsList().map { it.toDomain() }
    }

    override suspend fun upsertManagedRules(rows: List<DomainManagedRule>) = withContext(dispatcher) {
        database.transaction {
            rows.forEach { rule ->
                database.managedRuleQueries.upsert(
                    accountId = rule.accountId,
                    id = rule.id,
                    name = rule.name,
                    enabled = if (rule.enabled) 1L else 0L,
                    sortOrder = rule.order.toLong(),
                    conditionsJson = rule.conditions.encodeConditions(),
                    actionsJson = rule.actions.encodeActions(),
                )
            }
        }
    }

    override suspend fun deleteManagedRules(accountId: String, ids: List<String>) = withContext(dispatcher) {
        database.transaction { ids.forEach { database.managedRuleQueries.deleteById(accountId, it) } }
    }

    override suspend fun clearManagedRules(accountId: String) = withContext(dispatcher) {
        database.managedRuleQueries.deleteForAccount(accountId)
        Unit
    }

    override suspend fun syncState(accountId: String, type: String): String? = withContext(dispatcher) {
        database.syncStateQueries.get(accountId, type).executeAsOneOrNull()
    }

    override suspend fun setSyncState(accountId: String, type: String, state: String?) = withContext(dispatcher) {
        if (state == null) {
            database.syncStateQueries.delete(accountId, type)
        } else {
            database.syncStateQueries.upsert(accountId, type, state)
        }
        Unit
    }

    override suspend fun cachedBlob(accountId: String, blobId: String): CachedBlob? = withContext(dispatcher) {
        val row = database.blobCacheQueries.get(accountId, blobId).executeAsOneOrNull() ?: return@withContext null
        val bytes = blobFiles.read(row.path) ?: run {
            // The cache directory is the system's to reclaim; a row whose
            // file is gone is dropped here and the blob downloads again.
            database.blobCacheQueries.delete(accountId, blobId)
            return@withContext null
        }
        database.blobCacheQueries.touch(now(), accountId, blobId)
        CachedBlob(contentType = row.contentType, bytes = bytes)
    }

    override suspend fun cacheBlob(
        accountId: String,
        blobId: String,
        contentType: String,
        bytes: ByteArray,
    ) = withContext(dispatcher) {
        val path = blobPath(accountId, blobId)
        // The bytes land in the file first: a row is only written for a
        // blob that can be read back.
        if (!blobFiles.write(path, bytes)) return@withContext
        val evicted = database.transactionWithResult {
            database.blobCacheQueries.upsert(
                accountId = accountId,
                blobId = blobId,
                contentType = contentType,
                path = path,
                size = bytes.size.toLong(),
                lastUsedAt = now(),
            )
            val dropped = mutableListOf<String>()
            var total = database.blobCacheQueries.totalSize().executeAsOne()
            while (total > blobBudgetBytes) {
                val victims = database.blobCacheQueries.oldest(EVICTION_BATCH).executeAsList()
                if (victims.isEmpty()) break
                victims.forEach { victim ->
                    if (victim.accountId == accountId && victim.blobId == blobId) return@forEach
                    database.blobCacheQueries.delete(victim.accountId, victim.blobId)
                    dropped += victim.path
                    total -= victim.size
                }
                if (victims.size == 1 && victims[0].accountId == accountId && victims[0].blobId == blobId) break
            }
            dropped
        }
        // The files follow the rows: eviction frees the bytes, not only
        // the metadata that accounts for them (REQ-AND-SYNC-12).
        evicted.forEach { blobFiles.delete(it) }
    }

    /**
     * Where a blob's bytes are held, relative to the file store's own
     * directory: one directory per account, one file per blob. The name
     * keeps the readable part of the id and carries a hash of the whole,
     * so two ids that differ only in characters a file name cannot hold
     * still land in two files.
     */
    private fun blobPath(accountId: String, blobId: String): String =
        fileSegment(accountId) + "/" + fileSegment(blobId)

    private fun fileSegment(value: String): String {
        val readable = value.filter { it.isLetterOrDigit() || it == '-' || it == '_' }.takeLast(48)
        return if (readable.isEmpty()) hashOf(value) else readable + "-" + hashOf(value)
    }

    /** FNV-1a over the id's UTF-8 bytes, so a path is the same every run. */
    private fun hashOf(value: String): String {
        var hash = 2166136261u
        value.encodeToByteArray().forEach { byte ->
            hash = hash xor (byte.toUInt() and 0xffu)
            hash *= 16777619u
        }
        return hash.toString(16)
    }

    override fun outbox(): Flow<List<OutboxEntry>> =
        database.outboxQueries.selectAll().asFlow().mapToList(dispatcher)
            .mapList { it.toDomain() }

    override suspend fun outboxList(): List<OutboxEntry> = withContext(dispatcher) {
        database.outboxQueries.selectAll().executeAsList().map { it.toDomain() }
    }

    override suspend fun outboxEntry(id: Long): OutboxEntry? = withContext(dispatcher) {
        database.outboxQueries.selectById(id).executeAsOneOrNull()?.toDomain()
    }

    override suspend fun enqueueOutbox(entry: NewOutboxEntry): Long = withContext(dispatcher) {
        database.transactionWithResult {
            database.outboxQueries.insert(
                accountId = entry.accountId,
                kind = entry.kind.name,
                label = entry.label,
                payload = entry.payload,
                revertJson = entry.revertJson,
                entityIds = entry.entityIds.joinToString(" "),
                createdAt = entry.createdAt,
            )
            database.outboxQueries.lastInsertedId().executeAsOne()
        }
    }

    override suspend fun updateOutboxState(
        id: Long,
        state: OutboxState,
        attempts: Int,
        lastError: String?,
        permanent: Boolean,
        nextAttemptAt: Long,
    ) = withContext(dispatcher) {
        database.outboxQueries.updateState(
            state = state.wire,
            attempts = attempts.toLong(),
            lastError = lastError,
            permanent = if (permanent) 1L else 0L,
            nextAttemptAt = nextAttemptAt,
            id = id,
        )
        Unit
    }

    override suspend fun updateOutboxPayload(id: Long, payload: String) = withContext(dispatcher) {
        database.outboxQueries.updatePayload(payload, id)
        Unit
    }

    override suspend fun deleteOutbox(id: Long) = withContext(dispatcher) {
        database.outboxQueries.delete(id)
        Unit
    }

    override suspend fun pushRegistration(): PushRegistration? = withContext(dispatcher) {
        database.pushRegistrationQueries.get().executeAsOneOrNull()?.let {
            PushRegistration(
                subscriptionId = it.subscriptionId,
                deviceClientId = it.deviceClientId,
                tokenFingerprint = it.tokenFingerprint,
                registeredAt = it.registeredAt,
                transport = it.transport,
            )
        }
    }

    override suspend fun setPushRegistration(registration: PushRegistration?): Unit = withContext(dispatcher) {
        if (registration == null) {
            database.managedRuleQueries.deleteAll()
            database.pushRegistrationQueries.deleteAll()
        } else {
            database.pushRegistrationQueries.upsert(
                subscriptionId = registration.subscriptionId,
                deviceClientId = registration.deviceClientId,
                tokenFingerprint = registration.tokenFingerprint,
                registeredAt = registration.registeredAt,
                transport = registration.transport,
            )
        }
    }

    override suspend fun blobCacheSize(): Long = withContext(dispatcher) {
        database.blobCacheQueries.totalSize().executeAsOne()
    }

    override suspend fun clearAll(): List<OutboxEntry> = withContext(dispatcher) {
        // Read before the delete: what goes with the account is what the
        // caller has to account for (issue #420).
        val dropped = database.outboxQueries.selectAll().executeAsList().map { it.toDomain() }
        database.transaction {
            database.accountList().forEach { accountId ->
                database.mailboxQueries.deleteForAccount(accountId)
                database.emailQueries.deleteForAccount(accountId)
                database.emailQueries.deleteMembershipForAccount(accountId)
                database.threadQueries.deleteForAccount(accountId)
                database.identityQueries.deleteForAccount(accountId)
                database.managedRuleQueries.deleteForAccount(accountId)
            }
            database.syncStateQueries.deleteAll()
            database.blobCacheQueries.deleteAll()
            database.outboxQueries.deleteAll()
            database.managedRuleQueries.deleteAll()
            database.pushRegistrationQueries.deleteAll()
            database.accountQueries.deleteAll()
        }
        blobFiles.deleteAll()
        tombstones.clear()
        membershipHolds.clear()
        dropped
    }

    private fun writeMembership(accountId: String, emailId: String, mailboxIds: Set<String>) {
        database.emailQueries.deleteMembership(accountId, emailId)
        mailboxIds.forEach { database.emailQueries.insertMembership(accountId, emailId, it) }
    }

    private fun HeroldDatabase.accountList(): List<String> =
        accountQueries.selectAll().executeAsList().map { it.id }

    companion object {
        /** 64 MiB of cached bodies and inline images (REQ-AND-SYNC-12). */
        const val DEFAULT_BLOB_BUDGET_BYTES: Long = 64L * 1024 * 1024
        private const val EVICTION_BATCH = 16L
    }
}

private fun <T, R> Flow<List<T>>.mapList(transform: (T) -> R): Flow<List<R>> =
    map { list -> list.map(transform) }

private fun Account.toDomain() = DomainAccount(
    id = id,
    name = name,
    isPrimary = isPrimary != 0L,
    isPersonal = isPersonal != 0L,
    sortOrder = sortOrder.toInt(),
)

private fun Mailbox.toDomain() = DomainMailbox(
    accountId = accountId,
    id = id,
    name = name,
    role = role,
    parentId = parentId,
    sortOrder = sortOrder.toInt(),
    totalEmails = totalEmails.toInt(),
    unreadEmails = unreadEmails.toInt(),
    disposition = CategoryDisposition.from(disposition),
    priority = priority?.toInt(),
)

private fun Email.toDomain() = DomainEmail(
    accountId = accountId,
    id = id,
    threadId = threadId,
    blobId = blobId,
    fromName = fromName,
    fromEmail = fromEmail,
    toLine = toLine,
    toAddresses = toJson.decodeAddresses(),
    ccAddresses = ccJson.decodeAddresses(),
    messageId = messageIdJson.decodeStrings(),
    inReplyTo = inReplyToJson.decodeStrings(),
    references = referencesJson.decodeStrings(),
    deliveredTo = deliveredTo,
    listUnsubscribe = listUnsubscribe,
    listUnsubscribePost = listUnsubscribePost,
    subject = subject,
    preview = preview,
    receivedAt = receivedAt,
    size = size,
    hasAttachment = hasAttachment != 0L,
    snoozedUntil = snoozedUntil,
    keywords = keywords.splitTokens(),
    mailboxIds = mailboxIds.splitTokens(),
    bodyHtml = bodyHtml,
    bodyText = bodyText,
    attachments = attachmentsJson?.let { json ->
        runCatching { attachmentJson.decodeFromString<List<AttachmentDto>>(json) }
            .getOrDefault(emptyList())
            .map { it.toDomain() }
    } ?: emptyList(),
)

private fun Managed_rule.toDomain() = DomainManagedRule(
    accountId = accountId,
    id = id,
    name = name,
    enabled = enabled != 0L,
    order = sortOrder.toInt(),
    conditions = conditionsJson.decodeConditions(),
    actions = actionsJson.decodeActions(),
)

private fun List<DomainRuleCondition>.encodeConditions(): String =
    attachmentJson.encodeToString(map { ConditionDto(it.field, it.op, it.value) })

private fun String.decodeConditions(): List<DomainRuleCondition> =
    runCatching { attachmentJson.decodeFromString<List<ConditionDto>>(this) }
        .getOrDefault(emptyList())
        .map { DomainRuleCondition(it.field, it.op, it.value) }

private fun List<DomainRuleAction>.encodeActions(): String =
    attachmentJson.encodeToString(map { ActionDto(it.kind, it.params) })

private fun String.decodeActions(): List<DomainRuleAction> =
    runCatching { attachmentJson.decodeFromString<List<ActionDto>>(this) }
        .getOrDefault(emptyList())
        .map { DomainRuleAction(it.kind, it.params) }

private fun Outbox.toDomain() = OutboxEntry(
    id = id,
    accountId = accountId,
    kind = OutboxKind.from(kind),
    label = label,
    payload = payload,
    revertJson = revertJson,
    entityIds = entityIds.split(" ").filter { it.isNotBlank() },
    createdAt = createdAt,
    state = OutboxState.from(state),
    attempts = attempts.toInt(),
    lastError = lastError,
    permanent = permanent != 0L,
    nextAttemptAt = nextAttemptAt,
)

private fun Thread.toDomain() = DomainThread(
    accountId = accountId,
    id = id,
    emailIds = emailIds.split(" ").filter { it.isNotBlank() },
)

private fun Identity.toDomain() = DomainIdentity(
    accountId = accountId,
    id = id,
    name = name,
    email = email,
    mayDelete = mayDelete != 0L,
    isDefault = isDefault != 0L,
)

private fun String.splitTokens(): Set<String> =
    split(" ").filter { it.isNotBlank() }.toSet()

private fun List<MailAddress>.encodeAddresses(): String? =
    if (isEmpty()) null else attachmentJson.encodeToString(map { AddressDto(it.name, it.email) })

private fun String?.decodeAddresses(): List<MailAddress> =
    this?.let { json ->
        runCatching { attachmentJson.decodeFromString<List<AddressDto>>(json) }
            .getOrDefault(emptyList())
            .map { MailAddress(it.name, it.email) }
    } ?: emptyList()

private fun List<String>.encodeStrings(): String? =
    if (isEmpty()) null else attachmentJson.encodeToString(this)

private fun String?.decodeStrings(): List<String> =
    this?.let { json ->
        runCatching { attachmentJson.decodeFromString<List<String>>(json) }.getOrDefault(emptyList())
    } ?: emptyList()

/** Neutralises the wildcards of a LIKE pattern, which the caller wraps in `%`. */
private fun String.escapeForLike(): String =
    replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")

private fun Attachment.toDto() = AttachmentDto(blobId, name, type, size, cid, isInline)

private fun AttachmentDto.toDomain() = Attachment(blobId, name, type, size, cid, isInline)
