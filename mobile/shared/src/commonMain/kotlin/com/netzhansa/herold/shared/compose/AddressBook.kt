package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.jmap.JmapApi
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** A completion offered under a recipient field. */
data class AddressSuggestion(
    val address: MailAddress,
    val lastUsedAt: String?,
)

/**
 * Recipient autocomplete, from the principal's seen-address history
 * (`SeenAddress`, suite REQ-MAIL-11e..m). The list is loaded once per
 * session on the first completion and ranked the way the suite's
 * recipient field ranks it: addresses whose name or local part starts
 * with the typed text first, then substring matches, most recently used
 * first within each tier.
 */
class AddressBook(private val api: JmapApi) {

    private val mutex = Mutex()
    private var loaded: List<AddressSuggestion>? = null

    /** Drops the cache so the next completion re-reads the history. */
    suspend fun invalidate() = mutex.withLock { loaded = null }

    suspend fun suggestions(accountId: String, query: String, limit: Int = DEFAULT_LIMIT): List<AddressSuggestion> {
        val all = entries(accountId)
        val needle = query.trim().lowercase()
        if (needle.isEmpty()) return all.take(limit)
        val prefix = mutableListOf<AddressSuggestion>()
        val contains = mutableListOf<AddressSuggestion>()
        all.forEach { entry ->
            val email = entry.address.email.lowercase()
            val name = entry.address.name.orEmpty().lowercase()
            when {
                email.startsWith(needle) || name.startsWith(needle) ||
                    email.substringAfter('@', "").startsWith(needle) -> prefix.add(entry)

                email.contains(needle) || name.contains(needle) -> contains.add(entry)
            }
        }
        return (prefix + contains).take(limit)
    }

    private suspend fun entries(accountId: String): List<AddressSuggestion> {
        loaded?.let { return it }
        return mutex.withLock {
            loaded ?: runCatching { api.seenAddresses(accountId) }
                .getOrDefault(emptyList())
                .sortedByDescending { it.lastUsedAt.orEmpty() }
                .map {
                    AddressSuggestion(
                        address = MailAddress(it.displayName.takeIf(String::isNotBlank), it.email),
                        lastUsedAt = it.lastUsedAt,
                    )
                }
                .also { loaded = it }
        }
    }

    companion object {
        const val DEFAULT_LIMIT = 8
    }
}

/**
 * Parses what the user typed into a recipient field. Accepts
 * "Name <addr>", a bare address, and comma or semicolon separated lists,
 * mirroring the suite's `recipient-parse.ts`.
 */
object RecipientParser {

    fun parse(raw: String): List<MailAddress> =
        raw.split(',', ';', '\n')
            .mapNotNull { parseOne(it) }

    fun parseOne(raw: String): MailAddress? {
        val value = raw.trim().trim(',', ';')
        if (value.isEmpty()) return null
        val angle = Regex("""^(.*)<([^>]+)>\s*$""").find(value)
        if (angle != null) {
            val name = angle.groupValues[1].trim().trim('"').takeIf { it.isNotBlank() }
            return MailAddress(name, angle.groupValues[2].trim())
        }
        return MailAddress(null, value)
    }

    /** True when [value] looks like an address the server will accept. */
    fun isValid(value: String): Boolean {
        val at = value.indexOf('@')
        return at > 0 && at < value.length - 1 && !value.contains(' ')
    }
}
