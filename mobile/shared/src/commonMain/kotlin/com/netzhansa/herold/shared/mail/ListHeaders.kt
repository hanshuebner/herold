package com.netzhansa.herold.shared.mail

/**
 * Parsing of the RFC 2369 `List-*` header family and the RFC 8058
 * one-click unsubscribe extension (suite `14-unsubscribe.md`
 * REQ-UNS-01..23). It mirrors the suite's `lib/mail/list-headers.ts` so
 * the two clients choose the same mechanism for the same message.
 *
 * Nothing here touches the network: these are string-to-shape transforms,
 * exercised in `commonTest`.
 */
object ListHeaders {
    /** One URL taken out of an angle-bracketed `List-*` header value. */
    data class ListUrl(val scheme: Scheme, val url: String)

    enum class Scheme { HTTPS, HTTP, MAILTO }

    /**
     * Every `<...>` bracketed URL of a raw header value, tagged with its
     * scheme. A value with an unrecognised scheme (a bare `NO`, a non-URL
     * token) is dropped, and a caller treats an empty result the same as
     * an absent header.
     */
    fun parseAngleBracketUrls(raw: String?): List<ListUrl> {
        if (raw.isNullOrBlank()) return emptyList()
        return BRACKETED.findAll(raw).mapNotNull { match ->
            val url = match.groupValues[1].trim()
            when {
                url.startsWith("https:", ignoreCase = true) -> ListUrl(Scheme.HTTPS, url)
                url.startsWith("http:", ignoreCase = true) -> ListUrl(Scheme.HTTP, url)
                url.startsWith("mailto:", ignoreCase = true) -> ListUrl(Scheme.MAILTO, url)
                else -> null
            }
        }.toList()
    }

    /**
     * REQ-UNS-02: true when `List-Unsubscribe-Post` carries the RFC 8058
     * one-click marker. Matched case-insensitively, since senders vary the
     * casing of the one defined value.
     */
    fun hasOneClickPost(raw: String?): Boolean {
        if (raw.isNullOrBlank()) return false
        return ONE_CLICK.containsMatchIn(raw)
    }

    /** The mechanism the Unsubscribe affordance runs. */
    sealed interface Mechanism {
        val url: String

        /** RFC 8058: an empty POST, no confirmation (REQ-UNS-20/30). */
        data class OneClick(override val url: String) : Mechanism

        /** A plain HTTPS URL, opened in the browser (REQ-UNS-21). */
        data class Https(override val url: String) : Mechanism

        /** A `mailto:`, which opens a prefilled compose (REQ-UNS-22). */
        data class Mailto(override val url: String) : Mechanism

        /**
         * Only a cleartext `http:` URL was advertised. The affordance is
         * still shown - the header names a mechanism (REQ-UNS-03) - and
         * acting on it surfaces the refusal rather than opening it
         * (REQ-UNS-04).
         */
        data class HttpOnly(override val url: String) : Mechanism
    }

    /**
     * The mechanism to offer for a message, given its two raw header
     * values. Priority: one-click, plain HTTPS, `mailto:`, cleartext.
     * Null when `List-Unsubscribe` names no recognised URL at all, which
     * is REQ-UNS-03's "no affordance, no body-text fallback".
     */
    fun chooseMechanism(listUnsubscribe: String?, listUnsubscribePost: String?): Mechanism? {
        val urls = parseAngleBracketUrls(listUnsubscribe)
        if (urls.isEmpty()) return null
        val https = urls.firstOrNull { it.scheme == Scheme.HTTPS }
        if (https != null) {
            return if (hasOneClickPost(listUnsubscribePost)) {
                Mechanism.OneClick(https.url)
            } else {
                Mechanism.Https(https.url)
            }
        }
        urls.firstOrNull { it.scheme == Scheme.MAILTO }?.let { return Mechanism.Mailto(it.url) }
        urls.firstOrNull { it.scheme == Scheme.HTTP }?.let { return Mechanism.HttpOnly(it.url) }
        return null
    }

    /** The fields a `mailto:` URI prefills in the composer (REQ-UNS-22). */
    data class MailtoFields(val to: String, val subject: String, val body: String)

    /**
     * Parses a `mailto:` URI into compose fields. Tolerant of malformed
     * percent-encoding: an undecodable segment is carried through as it
     * stands rather than failing the unsubscribe.
     */
    fun parseMailto(uri: String): MailtoFields {
        val stripped = uri.removePrefix("mailto:").removePrefix("MAILTO:")
        val queryIndex = stripped.indexOf('?')
        val addressPart = if (queryIndex == -1) stripped else stripped.substring(0, queryIndex)
        val queryPart = if (queryIndex == -1) "" else stripped.substring(queryIndex + 1)
        val params = queryPart.split('&').mapNotNull { pair ->
            if (pair.isBlank()) return@mapNotNull null
            val eq = pair.indexOf('=')
            if (eq == -1) return@mapNotNull null
            decode(pair.substring(0, eq)).lowercase() to decode(pair.substring(eq + 1))
        }.toMap()
        return MailtoFields(
            to = decode(addressPart),
            subject = params["subject"].orEmpty(),
            body = params["body"].orEmpty(),
        )
    }

    /**
     * Percent-decoding with `+` as a space, as a `mailto:` query carries
     * it. An incomplete escape is kept literally.
     */
    private fun decode(value: String): String {
        if (!value.contains('%') && !value.contains('+')) return value
        val bytes = mutableListOf<Byte>()
        var index = 0
        while (index < value.length) {
            val char = value[index]
            when {
                char == '+' -> {
                    bytes.add(' '.code.toByte())
                    index++
                }

                char == '%' && index + 2 < value.length + 1 -> {
                    val hex = value.drop(index + 1).take(2)
                    val parsed = hex.toIntOrNull(16)
                    if (hex.length == 2 && parsed != null) {
                        bytes.add(parsed.toByte())
                        index += 3
                    } else {
                        bytes.addAll(char.toString().encodeToByteArray().toList())
                        index++
                    }
                }

                else -> {
                    bytes.addAll(char.toString().encodeToByteArray().toList())
                    index++
                }
            }
        }
        return bytes.toByteArray().decodeToString()
    }

    private val BRACKETED = Regex("<([^>]+)>")
    private val ONE_CLICK = Regex("list-unsubscribe\\s*=\\s*one-click", RegexOption.IGNORE_CASE)
}
