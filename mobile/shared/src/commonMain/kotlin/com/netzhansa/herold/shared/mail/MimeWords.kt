package com.netzhansa.herold.shared.mail

/**
 * RFC 2047 encoded-word decoding, for header text that reaches the client
 * undecoded (issue #348). herold decodes the sender before it puts it in a
 * push payload (issue #347), so this is what keeps a payload from an older
 * server rendering as `=?UTF-8?B?SGFucyBIw7xibmVy?=` rather than a name.
 *
 * Adjacent encoded words are joined without the whitespace between them,
 * as RFC 2047 section 6.2 requires; a word whose charset or encoding this
 * does not know is left exactly as it arrived, so nothing is lost to a
 * failed decode.
 */
object MimeWords {

    private val encodedWord = Regex("""=\?([^?\s]+)\?([BbQq])\?([^?\s]*)\?=""")

    /** [text] with every encoded word it contains replaced by its text. */
    fun decode(text: String): String {
        if (!text.contains("=?")) return text
        val out = StringBuilder()
        var cursor = 0
        var previousWasWord = false
        for (match in encodedWord.findAll(text)) {
            val gap = text.substring(cursor, match.range.first)
            cursor = match.range.last + 1
            val decoded = decodeWord(
                charset = match.groupValues[1],
                encoding = match.groupValues[2],
                payload = match.groupValues[3],
            )
            if (decoded == null) {
                out.append(gap).append(match.value)
                previousWasWord = false
                continue
            }
            // Whitespace separating two encoded words is layout of the
            // header, not part of the text they carry.
            if (!(previousWasWord && gap.isBlank())) out.append(gap)
            out.append(decoded)
            previousWasWord = true
        }
        out.append(text.substring(cursor))
        return out.toString()
    }

    /** One encoded word's text, or null when it cannot be decoded. */
    private fun decodeWord(charset: String, encoding: String, payload: String): String? {
        // RFC 2231 section 5 allows a language tag on the charset token.
        val name = charset.substringBefore('*').lowercase()
        val bytes = when (encoding.lowercase()) {
            "b" -> decodeBase64(payload)
            "q" -> decodeQuoted(payload)
            else -> null
        } ?: return null
        return decodeBytes(bytes, name)
    }

    /** The "Q" encoding: `_` is a space and `=XX` is one byte. */
    private fun decodeQuoted(payload: String): ByteArray? {
        val out = mutableListOf<Byte>()
        var index = 0
        while (index < payload.length) {
            when (val ch = payload[index]) {
                '_' -> {
                    out.add(' '.code.toByte())
                    index++
                }

                '=' -> {
                    if (index + 2 >= payload.length) return null
                    val value = payload.substring(index + 1, index + 3).toIntOrNull(16) ?: return null
                    out.add(value.toByte())
                    index += 3
                }

                else -> {
                    if (ch.code > 0xFF) return null
                    out.add(ch.code.toByte())
                    index++
                }
            }
        }
        return out.toByteArray()
    }

    private const val BASE64_ALPHABET =
        "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

    private fun decodeBase64(payload: String): ByteArray? {
        val out = mutableListOf<Byte>()
        var accumulator = 0
        var bits = 0
        for (ch in payload) {
            if (ch == '=' || ch.isWhitespace()) continue
            val value = BASE64_ALPHABET.indexOf(ch)
            if (value < 0) return null
            accumulator = (accumulator shl 6) or value
            bits += 6
            if (bits >= 8) {
                bits -= 8
                out.add(((accumulator shr bits) and 0xFF).toByte())
            }
        }
        return out.toByteArray()
    }

    /**
     * Bytes as text. UTF-8 is the common case; the single-byte charsets
     * that still turn up in mail map byte-for-byte, with the printable
     * characters Windows-1252 puts in the C1 range spelled out.
     */
    private fun decodeBytes(bytes: ByteArray, charset: String): String = when (charset) {
        "utf-8", "utf8" -> bytes.decodeToString()
        "iso-8859-1", "iso8859-1", "latin1", "latin-1", "us-ascii", "ascii" -> singleByte(bytes, null)
        "windows-1252", "cp1252" -> singleByte(bytes, CP1252_C1)
        else -> bytes.decodeToString()
    }

    private fun singleByte(bytes: ByteArray, c1: String?): String = buildString(bytes.size) {
        bytes.forEach { byte ->
            val code = byte.toInt() and 0xFF
            if (c1 != null && code in 0x80..0x9F) append(c1[code - 0x80]) else append(code.toChar())
        }
    }

    /**
     * Windows-1252's 0x80..0x9F, which ISO-8859-1 leaves as control codes.
     * Spelled as escapes so this file stays plain ASCII; the five
     * unassigned positions map to the replacement character.
     */
    private const val CP1252_C1 =
        "\u20AC\uFFFD\u201A\u0192\u201E\u2026\u2020\u2021" +
            "\u02C6\u2030\u0160\u2039\u0152\uFFFD\u017D\uFFFD" +
            "\uFFFD\u2018\u2019\u201C\u201D\u2022\u2013\u2014" +
            "\u02DC\u2122\u0161\u203A\u0153\uFFFD\u017E\u0178"
}
