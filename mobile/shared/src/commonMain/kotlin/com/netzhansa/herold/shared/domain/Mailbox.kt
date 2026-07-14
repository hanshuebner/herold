package com.netzhansa.herold.shared.domain

/**
 * A mailbox as rendered by the Compose UI. Mirrors the JMAP Mailbox object
 * (RFC 8621 section 2) with only the fields the mailbox list needs; the full
 * shape grows as later phases add folder management.
 */
data class Mailbox(
    val id: String,
    val name: String,
    val unreadEmails: Int,
)
