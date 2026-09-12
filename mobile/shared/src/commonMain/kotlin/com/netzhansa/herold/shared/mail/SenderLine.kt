package com.netzhansa.herold.shared.mail

import com.netzhansa.herold.shared.compose.RecipientParser
import com.netzhansa.herold.shared.domain.MailAddress

/**
 * The sender a notification names, from the two shapes a push payload's
 * sender arrives in (issue #348).
 *
 * herold sends the decoded display name in `from` and the bare address in
 * `fromAddress` (issue #347). A payload from a server without that change
 * carries the raw `From` header in `from` alone - possibly with RFC 2047
 * encoded words and an address in angle brackets - so the header shape is
 * read here as well and the encoded words are decoded on the client.
 */
object SenderLine {

    fun parse(from: String, fromAddress: String? = null): MailAddress {
        val decoded = MimeWords.decode(from).trim().trim('"').trim()
        val address = fromAddress?.trim().orEmpty()

        if (decoded.contains('<')) {
            val parsed = RecipientParser.parseOne(decoded)
            return MailAddress(
                name = parsed?.name?.takeIf { it.isNotBlank() },
                email = address.ifBlank { parsed?.email.orEmpty() },
            )
        }
        if (address.isNotEmpty()) {
            return MailAddress(
                name = decoded.takeIf { it.isNotBlank() && !it.equals(address, ignoreCase = true) },
                email = address,
            )
        }
        // A bare header: an address on its own, or a name with none.
        val looksLikeAddress = decoded.contains('@') && !decoded.contains(' ')
        return if (looksLikeAddress) {
            MailAddress(name = null, email = decoded)
        } else {
            MailAddress(name = decoded.takeIf { it.isNotBlank() }, email = "")
        }
    }
}
