package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.MailAddress
import kotlin.test.Test
import kotlin.test.assertEquals

/** The From picker's contents and its default (suite REQ-MAIL-12/12a, REQ-MAIL-SUB-05). */
class IdentityChoiceTest {

    private val accounts = listOf(
        Account(id = "a2", name = "alice@example.local", isPrimary = true, sortOrder = 0),
        Account(id = "a5", name = "Classic Computing", isPrimary = false, sortOrder = 1),
    )

    private val identities = listOf(
        Identity("a5", "default", "Vorsitz", "vorsitz@classic-computing.example", isDefault = true),
        Identity("a2", "default", "Alice", "alice@example.local", isDefault = true),
        Identity("a2", "800001", "Alice elsewhere", "alice@foreign.example"),
        Identity("a2", "800003", "Alice broken", "alice-broken@foreign.example"),
    )

    @Test
    fun thePickerListsEveryIdentityOfEveryAccount() {
        val options = IdentityChoice.options(identities, accounts, accountInScope = null)
        assertEquals(4, options.size)
        // The account's default address heads its group, then the rest
        // alphabetically; the sub-account's identities follow.
        assertEquals(
            listOf(
                "alice@example.local",
                "alice-broken@foreign.example",
                "alice@foreign.example",
                "vorsitz@classic-computing.example",
            ),
            options.map { it.identity.email },
        )
        assertEquals("Classic Computing", options.last().accountName)
    }

    @Test
    fun theAccountInScopeComesFirst() {
        val options = IdentityChoice.options(identities, accounts, accountInScope = "a5")
        assertEquals("vorsitz@classic-computing.example", options.first().identity.email)
    }

    @Test
    fun aNewComposeDefaultsToTheAccountInScope() {
        assertEquals(
            "vorsitz@classic-computing.example",
            IdentityChoice.defaultForNew(identities, accounts, "a5")?.email,
        )
        assertEquals(
            "alice@example.local",
            IdentityChoice.defaultForNew(identities, accounts, null)?.email,
        )
    }

    @Test
    fun aReplyDefaultsToTheIdentityTheMessageWasAddressedTo() {
        val parent = Email(
            accountId = "a2",
            id = "1",
            threadId = "t1",
            fromEmail = "bob@example.local",
            toAddresses = listOf(MailAddress(null, "alice@foreign.example")),
            deliveredTo = "alice@foreign.example",
        )
        assertEquals("alice@foreign.example", IdentityChoice.defaultForReply(parent, identities, accounts)?.email)
    }

    @Test
    fun aReplyToMailForTheSubAccountStaysOnThatAccount() {
        val parent = Email(
            accountId = "a5",
            id = "3",
            threadId = "t3",
            fromEmail = "member@classic-computing.example",
            toAddresses = listOf(MailAddress(null, "vorsitz@classic-computing.example")),
            deliveredTo = "vorsitz@classic-computing.example",
        )
        val chosen = IdentityChoice.defaultForReply(parent, identities, accounts)
        assertEquals("a5", chosen?.accountId)
        assertEquals("vorsitz@classic-computing.example", chosen?.email)
    }

    @Test
    fun bccMailFallsBackToTheDeliveryAddress() {
        val parent = Email(
            accountId = "a2",
            id = "4",
            threadId = "t4",
            fromEmail = "list@example.com",
            toAddresses = listOf(MailAddress(null, "undisclosed-recipients@example.com")),
            deliveredTo = "alice@foreign.example",
        )
        assertEquals("alice@foreign.example", IdentityChoice.defaultForReply(parent, identities, accounts)?.email)
    }

    @Test
    fun anOwnSentMessageRepliesFromTheIdentityThatSentIt() {
        val parent = Email(
            accountId = "a2",
            id = "5",
            threadId = "t5",
            fromEmail = "alice@foreign.example",
            toAddresses = listOf(MailAddress(null, "bob@example.local")),
            deliveredTo = null,
        )
        assertEquals("alice@foreign.example", IdentityChoice.defaultForReply(parent, identities, accounts)?.email)
    }
}
