package com.netzhansa.herold.shared.links

/**
 * The link surface the client is addressed through (REQ-AND-SYS-10/11):
 * the internal `herold://` scheme that notifications, shortcuts, the
 * widget and the tile carry, the Suite's https thread URLs that arrive as
 * App Links, and `mailto:` URIs from any other app.
 *
 * The parsing lives in the shared module because it is pure text work the
 * iOS client reuses for its universal links; the platform glue that turns
 * an `Intent` into one of these destinations is the Android app's.
 */
sealed interface AppDestination {

    /** The message list. */
    data object Inbox : AppDestination

    /** A conversation. [accountId] is null when the link did not name one. */
    data class Thread(val accountId: String?, val threadId: String) : AppDestination

    /** The composer, opened on [prefill]. */
    data class Compose(val prefill: ComposePrefill = ComposePrefill()) : AppDestination

    /** The composer answering a message (the shade's Reply action). */
    data class Reply(val accountId: String, val emailId: String) : AppDestination

    /** Settings, at [section] when the link named one. */
    data class Settings(val section: String? = null) : AppDestination
}

/**
 * What a link, a share or a `mailto:` hands the composer. Addresses are
 * kept as text and parsed into recipients by the composer, which already
 * owns that grammar.
 */
data class ComposePrefill(
    val to: List<String> = emptyList(),
    val cc: List<String> = emptyList(),
    val bcc: List<String> = emptyList(),
    val subject: String = "",
    val body: String = "",
) {
    val isEmpty: Boolean
        get() = to.isEmpty() && cc.isEmpty() && bcc.isEmpty() && subject.isBlank() && body.isBlank()
}

/** The internal scheme every in-app link is minted under. */
const val HEROLD_SCHEME = "herold"

object AppLinks {

    /** `herold://thread/<accountId>/<threadId>` - notifications and the widget. */
    fun threadUri(accountId: String, threadId: String): String =
        "$HEROLD_SCHEME://thread/$accountId/$threadId"

    /** `herold://reply/<accountId>/<emailId>` - the shade's Reply action. */
    fun replyUri(accountId: String, emailId: String): String =
        "$HEROLD_SCHEME://reply/$accountId/$emailId"

    /** `herold://compose` - the widget, the tile and the launcher shortcut. */
    fun composeUri(): String = "$HEROLD_SCHEME://compose"

    fun settingsUri(section: String? = null): String =
        if (section == null) "$HEROLD_SCHEME://settings" else "$HEROLD_SCHEME://settings/$section"

    /**
     * The Suite's URL for a conversation, which is what a share of a
     * message carries (REQ-AND-SYS-02). The Suite routes on the fragment
     * (`web/apps/suite/src/lib/router/router.svelte.ts`), so the path is
     * the deployment origin's root and the route follows the `#`.
     */
    fun suiteThreadUrl(baseUrl: String, threadId: String): String =
        "${baseUrl.trimEnd('/')}/#/mail/thread/$threadId"

    /**
     * Resolves any URI the app is opened with. Returns null when the URI
     * names nothing the client handles, so the caller can ignore it
     * rather than land the user somewhere arbitrary.
     */
    fun parse(uri: String): AppDestination? {
        val parsed = ParsedUri.of(uri) ?: return null
        return when (parsed.scheme) {
            HEROLD_SCHEME -> internal(parsed)
            "mailto" -> AppDestination.Compose(Mailto.parse(uri) ?: ComposePrefill())
            "http", "https" -> suite(parsed)
            else -> null
        }
    }

    private fun internal(uri: ParsedUri): AppDestination? {
        // `herold://thread/a/b` puts "thread" in the authority; a
        // three-slash form puts it in the path. Both are accepted.
        val segments = buildList {
            uri.authority?.takeIf { it.isNotBlank() }?.let { add(it) }
            addAll(uri.pathSegments)
        }
        val head = segments.firstOrNull() ?: return AppDestination.Inbox
        val rest = segments.drop(1)
        return when (head) {
            "thread" -> when (rest.size) {
                1 -> AppDestination.Thread(null, rest[0])
                2 -> AppDestination.Thread(rest[0], rest[1])
                else -> null
            }

            "reply" -> if (rest.size == 2) AppDestination.Reply(rest[0], rest[1]) else null

            "compose" -> AppDestination.Compose(prefillFromQuery(uri.query))

            "settings" -> AppDestination.Settings(rest.firstOrNull())

            "inbox" -> AppDestination.Inbox

            else -> null
        }
    }

    /**
     * A Suite URL. The thread route lives in the fragment, so a verified
     * App Link for the origin's root resolves here; anything else on the
     * origin lands on the message list rather than nowhere.
     */
    private fun suite(uri: ParsedUri): AppDestination {
        val route = uri.fragment.orEmpty().trim('/').split('/').filter { it.isNotBlank() }
        if (route.size >= 3 && route[0] == "mail" && route[1] == "thread") {
            return AppDestination.Thread(null, route[2])
        }
        if (route.firstOrNull() == "settings") return AppDestination.Settings(route.getOrNull(1))
        return AppDestination.Inbox
    }

    private fun prefillFromQuery(query: String?): ComposePrefill {
        val params = queryParameters(query)
        return ComposePrefill(
            to = addresses(params["to"]),
            cc = addresses(params["cc"]),
            bcc = addresses(params["bcc"]),
            subject = params["subject"].orEmpty(),
            body = params["body"].orEmpty(),
        )
    }
}

/**
 * `mailto:` per RFC 6068 (REQ-AND-SYS-03): addresses before the `?`,
 * `to`, `cc`, `bcc`, `subject` and `body` after it. Percent-escapes are
 * decoded; a `+` stays a `+`, because it is a legal character in an
 * address and RFC 6068 does not give it the form-encoding meaning.
 */
object Mailto {
    fun parse(uri: String): ComposePrefill? {
        if (!uri.startsWith("mailto:", ignoreCase = true)) return null
        val rest = uri.substring("mailto:".length)
        val head = rest.substringBefore('?')
        val params = queryParameters(rest.substringAfter('?', ""))
        val to = addresses(head) + addresses(params["to"])
        return ComposePrefill(
            to = to.distinct(),
            cc = addresses(params["cc"]),
            bcc = addresses(params["bcc"]),
            subject = params["subject"].orEmpty(),
            body = params["body"].orEmpty(),
        )
    }
}

/** Splits a comma-separated address list, decoding each entry. */
internal fun addresses(raw: String?): List<String> =
    raw.orEmpty().split(',').map { percentDecode(it).trim() }.filter { it.isNotEmpty() }

/** Query parameters, last value wins, keys lowercased. */
internal fun queryParameters(query: String?): Map<String, String> {
    if (query.isNullOrBlank()) return emptyMap()
    val out = LinkedHashMap<String, String>()
    query.split('&').forEach { pair ->
        if (pair.isBlank()) return@forEach
        val key = percentDecode(pair.substringBefore('=')).lowercase()
        val value = percentDecode(pair.substringAfter('=', ""))
        if (key.isNotBlank()) out[key] = value
    }
    return out
}

/** Decodes `%XX` escapes as UTF-8; a stray `%` is left as it stands. */
internal fun percentDecode(text: String): String {
    if (!text.contains('%')) return text
    val bytes = ArrayList<Byte>(text.length)
    var index = 0
    while (index < text.length) {
        val ch = text[index]
        if (ch == '%' && index + 2 < text.length) {
            val hex = text.substring(index + 1, index + 3)
            val value = hex.toIntOrNull(16)
            if (value != null) {
                bytes.add(value.toByte())
                index += 3
                continue
            }
        }
        ch.toString().encodeToByteArray().forEach { bytes.add(it) }
        index += 1
    }
    return bytes.toByteArray().decodeToString()
}

/**
 * The little of a URI the routing needs, parsed without a platform URI
 * class so the shared module stays free of `java.net`.
 */
internal data class ParsedUri(
    val scheme: String,
    val authority: String?,
    val pathSegments: List<String>,
    val query: String?,
    val fragment: String?,
) {
    companion object {
        fun of(uri: String): ParsedUri? {
            val separator = uri.indexOf(':')
            if (separator <= 0) return null
            val scheme = uri.substring(0, separator).lowercase()
            var rest = uri.substring(separator + 1)
            val fragment = if (rest.contains('#')) rest.substringAfter('#') else null
            rest = rest.substringBefore('#')
            val query = if (rest.contains('?')) rest.substringAfter('?') else null
            rest = rest.substringBefore('?')
            var authority: String? = null
            if (rest.startsWith("//")) {
                val afterSlashes = rest.substring(2)
                authority = afterSlashes.substringBefore('/')
                rest = afterSlashes.substringAfter('/', "")
            }
            val segments = rest.split('/').filter { it.isNotBlank() }.map { percentDecode(it) }
            return ParsedUri(scheme, authority, segments, query, fragment)
        }
    }
}
