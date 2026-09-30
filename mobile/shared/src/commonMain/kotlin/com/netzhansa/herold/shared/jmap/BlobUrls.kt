package com.netzhansa.herold.shared.jmap

/**
 * The blob endpoints' URLs, built from the session object's templates
 * (RFC 8620 section 2, `downloadUrl` / `uploadUrl`).
 *
 * The construction is a pure function of the template and the four
 * variables, so a host-JVM check covers what a filename with a space, a
 * slash or a non-ASCII character does to the path an attachment is
 * fetched from (issue #500). The request itself stays in [JmapClient],
 * which carries the bearer token.
 */
object BlobUrls {

    /**
     * The download template a server that publishes none is addressed
     * with; herold serves this path (`docs/design/android/notes/server-contract.md`).
     */
    const val DEFAULT_DOWNLOAD_PATH = "/jmap/download/{accountId}/{blobId}/{type}/{name}"

    /** The upload template for a server that publishes none. */
    const val DEFAULT_UPLOAD_PATH = "/jmap/upload/{accountId}"

    /** The session's template, or the default path under [baseUrl]. */
    fun downloadTemplate(sessionTemplate: String, baseUrl: String): String =
        sessionTemplate.ifBlank { baseUrl.trimEnd('/') + DEFAULT_DOWNLOAD_PATH }

    /** The session's upload template, or the default path under [baseUrl]. */
    fun uploadTemplate(sessionTemplate: String, baseUrl: String): String =
        sessionTemplate.ifBlank { baseUrl.trimEnd('/') + DEFAULT_UPLOAD_PATH }

    /**
     * [template] with its four variables filled in. Each value is
     * percent-encoded, so a blob id, a content type's slash and a
     * filename's spaces stay inside their own path segment.
     */
    fun download(
        template: String,
        accountId: String,
        blobId: String,
        type: String,
        name: String,
    ): String = template
        .replace("{accountId}", accountId.urlEncode())
        .replace("{blobId}", blobId.urlEncode())
        .replace("{type}", type.urlEncode())
        .replace("{name}", name.urlEncode())

    /** [template] with its account variable filled in. */
    fun upload(template: String, accountId: String): String =
        template.replace("{accountId}", accountId.urlEncode())
}

/**
 * One path segment's worth of percent-encoding: the unreserved set of
 * RFC 3986 section 2.3 passes through, everything else goes out as the
 * UTF-8 bytes it encodes to.
 */
internal fun String.urlEncode(): String = buildString {
    this@urlEncode.encodeToByteArray().forEach { byte ->
        val value = byte.toInt() and 0xFF
        val char = value.toChar()
        if ((char.code < 0x80 && char.isLetterOrDigit()) || char in "-_.~") {
            append(char)
        } else {
            append('%')
            append(value.toString(16).uppercase().padStart(2, '0'))
        }
    }
}
