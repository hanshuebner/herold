package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.ManagedRule
import com.netzhansa.herold.shared.domain.RuleAction
import com.netzhansa.herold.shared.domain.RuleCondition
import com.netzhansa.herold.shared.domain.Thread
import com.netzhansa.herold.shared.jmap.WireAddress
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.jmap.WireIdentity
import com.netzhansa.herold.shared.jmap.WireMailbox
import com.netzhansa.herold.shared.jmap.WireManagedRule
import com.netzhansa.herold.shared.jmap.WireThread
import kotlinx.datetime.Instant
import kotlinx.serialization.json.JsonPrimitive

/**
 * Mapping from the JMAP wire objects onto the local store's rows. The sync
 * engine is the only place the two shapes meet.
 */

internal fun WireMailbox.toDomain(accountId: String) = Mailbox(
    accountId = accountId,
    id = id,
    name = name,
    role = role,
    parentId = parentId,
    sortOrder = sortOrder.toInt(),
    totalEmails = totalEmails.toInt(),
    unreadEmails = unreadEmails.toInt(),
)

internal fun WireThread.toDomain(accountId: String) = Thread(
    accountId = accountId,
    id = id,
    emailIds = emailIds,
)

internal fun WireIdentity.toDomain(accountId: String) = Identity(
    accountId = accountId,
    id = id,
    name = name,
    email = email,
    mayDelete = mayDelete,
    isDefault = isDefault,
)

internal fun WireManagedRule.toDomain(accountId: String) = ManagedRule(
    accountId = accountId,
    id = id,
    name = name,
    enabled = enabled,
    order = order,
    conditions = conditions.map { RuleCondition(it.field, it.op, it.value) },
    actions = actions.map { action ->
        RuleAction(
            kind = action.kind,
            params = action.params.mapValues { (_, value) ->
                (value as? JsonPrimitive)?.content ?: value.toString()
            },
        )
    },
)

internal fun WireAddress.toDomain() = MailAddress(name?.takeIf { it.isNotBlank() }, email)

/**
 * herold returns Message-IDs both bare and in angle brackets depending on
 * the path they took; the client keeps the bare form so a reply's
 * `inReplyTo` matches the parent's `messageId` whichever way it arrived.
 */
internal fun String.normaliseMessageId(): String = trim().removePrefix("<").removeSuffix(">")

internal fun WireEmail.toDomain(accountId: String): Email {
    val htmlPart = htmlBody?.firstOrNull { it.type == "text/html" } ?: htmlBody?.firstOrNull()
    val textPart = textBody?.firstOrNull()
    return Email(
        accountId = accountId,
        id = id,
        threadId = threadId.ifBlank { id },
        blobId = blobId,
        fromName = from?.firstOrNull()?.name.orEmpty(),
        fromEmail = from?.firstOrNull()?.email.orEmpty(),
        toLine = to.orEmpty().joinToString(", ") { it.name?.takeIf(String::isNotBlank) ?: it.email },
        toAddresses = to.orEmpty().map { it.toDomain() },
        ccAddresses = cc.orEmpty().map { it.toDomain() },
        messageId = messageId.orEmpty().map { it.normaliseMessageId() },
        inReplyTo = inReplyTo.orEmpty().map { it.normaliseMessageId() },
        references = references.orEmpty().map { it.normaliseMessageId() },
        deliveredTo = deliveredTo,
        listUnsubscribe = listUnsubscribe,
        listUnsubscribePost = listUnsubscribePost,
        subject = subject.orEmpty(),
        preview = preview.orEmpty(),
        receivedAt = parseJmapDate(receivedAt),
        size = size,
        hasAttachment = hasAttachment,
        snoozedUntil = snoozedUntil,
        keywords = keywords.filterValues { it }.keys,
        mailboxIds = mailboxIds.filterValues { it }.keys,
        bodyHtml = htmlPart?.partId?.let { bodyValues?.get(it)?.value },
        bodyText = textPart?.partId?.let { bodyValues?.get(it)?.value },
        attachments = attachments.orEmpty().mapNotNull { part ->
            val blob = part.blobId ?: return@mapNotNull null
            Attachment(
                blobId = blob,
                name = part.name ?: part.cid ?: "attachment",
                type = part.type,
                size = part.size,
                cid = part.cid,
                isInline = part.disposition == "inline" || part.cid != null,
            )
        },
    )
}

/**
 * The wire-to-store mapping, public so a caller outside the engine - an
 * instrumented test comparing the screen against the server - maps the same
 * way the sync engine does.
 */
fun WireEmail.toStoreRow(accountId: String): Email = toDomain(accountId)

/** The same for a filter rule, for the outbox drain and for tests. */
fun WireManagedRule.toRuleRow(accountId: String): ManagedRule = toDomain(accountId)

/**
 * JMAP UTCDate ("2026-09-11T08:30:00Z") to epoch milliseconds. An
 * unparseable or absent value sorts oldest rather than failing the sync.
 */
internal fun parseJmapDate(value: String?): Long {
    if (value.isNullOrBlank()) return 0
    return runCatching { Instant.parse(value).toEpochMilliseconds() }.getOrDefault(0)
}
