package com.netzhansa.herold.shared.mail

/**
 * Where a plain-text body's own text ends and the quoted history
 * begins, on the contract the Suite renders with
 * (`web/apps/suite/src/lib/mail/quoted.ts`, issue #432).
 *
 * [head] is always visible, [collapsed] is the single trailing citation
 * the reader opens. Rendering head then collapsed keeps the source
 * order.
 */
internal data class BodySplit(val head: String, val collapsed: String)

internal object QuotedText {

    /**
     * Splits a plain-text body.
     *
     * At most one citation folds: the single contiguous trailing quoted
     * region, an optional attribution line ahead of it, and a signature
     * block behind it. Anything else after the quoted region - genuine
     * text the sender wrote below it - leaves the whole body visible,
     * so a bottom-posted or interleaved reply reads in order.
     */
    fun split(body: String): BodySplit {
        if (body.isEmpty()) return BodySplit("", "")
        val lines = body.split(Regex("\r?\n"))

        val signatureAt = lines.indexOfFirst { isSignatureDelimiter(it) }
        val beforeSignature = if (signatureAt >= 0) lines.subList(0, signatureAt) else lines

        val citationAt = trailingCitationStart(beforeSignature)
        if (citationAt < 0) return BodySplit(body, "")

        val head = beforeSignature.subList(0, citationAt).dropLastWhile { it.isBlank() }
        val collapsed = lines.subList(citationAt, lines.size).dropLastWhile { it.isBlank() }
        return BodySplit(head.joinToString("\n"), collapsed.joinToString("\n"))
    }

    /** True for an auto-generated citation-introducer line. */
    fun isAttributionLine(line: String): Boolean =
        leadAttribution.matches(line) || nameFirstSchrieb.matches(line)

    /** True for the RFC 3676 signature delimiter and its common variant. */
    fun isSignatureDelimiter(line: String): Boolean = signatureDelimiter.matches(line.trim())

    /**
     * The line the trailing citation starts at, or -1 when the last
     * non-blank line is not quoted.
     */
    private fun trailingCitationStart(lines: List<String>): Int {
        var end = lines.size - 1
        while (end >= 0 && lines[end].isBlank()) end--
        if (end < 0 || !isQuotedLine(lines[end])) return -1

        var start = end
        var i = end - 1
        while (i >= 0) {
            val line = lines[i]
            if (line.isBlank()) {
                i--
                continue
            }
            if (isQuotedLine(line)) {
                start = i
                i--
                continue
            }
            if (isAttributionLine(line.trim())) start = i
            break
        }
        return start
    }

    private fun isQuotedLine(line: String): Boolean = quotedLine.containsMatchIn(line.trim())

    // English "On <date>, <sender> wrote:", German "Am <date> schrieb
    // <sender>:", Spanish and French in the same shape - the lines the
    // clients herold's correspondents use write above a quote.
    private val leadAttribution = Regex(
        """(On\b[\s\S]+\bwrote\s*:|Am\b[\s\S]+schrieb\b[\s\S]*:|El\b[\s\S]+escribi[oó]\s*:|Le\b[\s\S]+a\s+[ée]crit\s*:)\s*""",
        RegexOption.IGNORE_CASE,
    )

    // Thunderbird's German locale puts the name first: "<Name> schrieb
    // am <dd.mm.yy> um <hh:mm>:". Anchored on the literal date and time
    // so prose that happens to carry the same words is left alone.
    private val nameFirstSchrieb = Regex(
        """.+\bschrieb am\s+\d{1,2}\.\d{1,2}\.\d{2,4}\s+um\s+\d{1,2}:\d{2}\s*:\s*""",
        RegexOption.IGNORE_CASE,
    )

    private val signatureDelimiter = Regex("""-- ?""")

    private val quotedLine = Regex("""^>+(\s|$)""")
}
