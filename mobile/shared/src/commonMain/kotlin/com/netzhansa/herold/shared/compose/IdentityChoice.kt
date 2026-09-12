package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity

/** One row of the From picker: an identity plus the account it belongs to. */
data class FromOption(
    val identity: Identity,
    val accountName: String,
    val isPrimaryAccount: Boolean,
)

/**
 * Which identity a compose sends from.
 *
 * The picker lists every identity of every account the principal holds
 * (suite REQ-MAIL-12, REQ-MAIL-SUB-05), and the default follows the
 * suite's reply rules (REQ-MAIL-12a): a message the user sent replies from
 * the identity that sent it; otherwise the first of the user's identities
 * named in To, then Cc, then the address herold delivered the message to
 * (`X-Herold-Recipient`), then that account's default.
 *
 * The cross-account constraint is structural here: a reply to a thread in
 * account A is composed on account A, so [defaultForReply] only ever
 * considers that account's identities (REQ-MAIL-EXT-12 via
 * REQ-MAIL-SUB-05). Choosing an identity from another account in the
 * picker moves the whole compose - and its draft - to that account.
 */
object IdentityChoice {

    /** Every identity, the account in scope first, then the primary account, then the rest. */
    fun options(
        identities: List<Identity>,
        accounts: List<Account>,
        accountInScope: String?,
    ): List<FromOption> {
        val byId = accounts.associateBy { it.id }
        return identities
            .map { identity ->
                val account = byId[identity.accountId]
                FromOption(
                    identity = identity,
                    accountName = account?.name ?: identity.accountId,
                    isPrimaryAccount = account?.isPrimary ?: false,
                )
            }
            .sortedWith(
                compareBy(
                    { if (it.identity.accountId == accountInScope) 0 else 1 },
                    { if (it.isPrimaryAccount) 0 else 1 },
                    { byId[it.identity.accountId]?.sortOrder ?: 0 },
                    // The account's default address heads its group
                    // (suite REQ-MAIL-12 ordering).
                    { if (it.identity.isDefault) 0 else 1 },
                    { it.identity.email },
                ),
            )
    }

    /** The identity a fresh compose starts on: the account in scope, else the primary account. */
    fun defaultForNew(
        identities: List<Identity>,
        accounts: List<Account>,
        accountInScope: String?,
    ): Identity? = options(identities, accounts, accountInScope).firstOrNull()?.identity

    /**
     * The identity a reply or forward starts on. Only identities of the
     * parent's own account are candidates, so a reply always leaves from
     * the account that received the message.
     */
    fun defaultForReply(
        parent: Email,
        identities: List<Identity>,
        accounts: List<Account>,
    ): Identity? {
        val candidates = identities.filter { it.accountId == parent.accountId }
        if (candidates.isEmpty()) return defaultForNew(identities, accounts, parent.accountId)
        val byEmail = candidates.associateBy { it.email.lowercase() }

        if (parent.deliveredTo.isNullOrBlank()) {
            byEmail[parent.fromEmail.lowercase()]?.let { return it }
        }
        (parent.toAddresses + parent.ccAddresses).forEach { address ->
            byEmail[address.email.lowercase()]?.let { return it }
        }
        parent.deliveredTo?.lowercase()?.let { delivered ->
            byEmail[delivered]?.let { return it }
            val domain = delivered.substringAfter('@', "")
            if (domain.isNotBlank()) {
                candidates.firstOrNull { it.email.substringAfter('@', "").equals(domain, ignoreCase = true) }
                    ?.let { return it }
            }
        }
        return defaultForNew(candidates, accounts, parent.accountId)
    }

    /** Every address the principal sends as, for the own-message and reply-all tests. */
    fun selfEmails(identities: List<Identity>): Set<String> =
        identities.map { it.email.lowercase() }.filter { it.isNotBlank() }.toSet()
}
