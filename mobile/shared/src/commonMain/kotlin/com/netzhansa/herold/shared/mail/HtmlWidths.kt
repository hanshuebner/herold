package com.netzhansa.herold.shared.mail

/**
 * Makes a document written for a desktop reading pane lay out in a
 * phone's card (issue #430).
 *
 * A mail template states its geometry twice: as presentational
 * `width`/`height` attributes and as inline pixel lengths. Both survive
 * every stylesheet the renderer adds - a presentational attribute is an
 * author rule the reader's sheet can outrank, but an inline `width:
 * 575px` outranks everything short of `!important`, and a table whose
 * columns are told to be 555px wide is 555px wide. So the widths are
 * rewritten in the markup: anything wider than the card yields, and
 * everything that already fits is left exactly as the sender wrote it.
 *
 * What is left over after that is content that genuinely cannot narrow -
 * a row that refuses to wrap. [minimumWidthCssPx] estimates how wide
 * such a document still needs to be, which is what decides whether the
 * message is better read as its plain-text alternative
 * ([BodyPreference]).
 */
object HtmlWidths {

    /**
     * Rewrites every declared width wider than [contentWidthCssPx] so the
     * box it sizes reflows to the card.
     *
     * - a `width` attribute wider than the card is dropped, so the box
     *   takes the width it is given;
     * - a fixed `height` on a box whose width just changed is dropped
     *   with it, since the reflowed content is taller than the sender's
     *   single desktop line;
     * - an inline `width` becomes `auto`, an inline `max-width` becomes
     *   `100%`, and an inline `min-width` wider than the card - the one
     *   declaration that would force an overflow on its own - is
     *   removed.
     *
     * Percentages, and any length that already fits, are left alone: a
     * two-column 160px layout reads the way it was written.
     */
    fun neutralise(html: String, contentWidthCssPx: Int): String = startTag.replace(html) { match ->
        val name = match.groupValues[1]
        var attributes = match.groupValues[2]
        var widthYielded = false
        attributes = widthAttribute.replace(attributes) { attribute ->
            val declared = pixels(attributeValue(attribute))
            if (declared != null && declared > contentWidthCssPx) {
                widthYielded = true
                ""
            } else {
                attribute.value
            }
        }
        if (widthYielded) {
            attributes = heightAttribute.replace(attributes) { attribute ->
                if (pixels(attributeValue(attribute)) != null) "" else attribute.value
            }
        }
        attributes = styleAttribute.replace(attributes) { attribute ->
            val rewritten = rewriteStyle(attributeValue(attribute), contentWidthCssPx)
            if (rewritten.isEmpty()) "" else " style=\"${rewritten.replace("\"", "&quot;")}\""
        }
        "<$name$attributes>"
    }

    /**
     * How wide the document still needs to be after [neutralise], in CSS
     * pixels, or 0 when nothing in it resists reflow.
     *
     * Text reflows, images are capped, and declared widths have yielded,
     * so what remains is the content that says outright that it does not
     * wrap: a `nowrap` cell, an inline `white-space: nowrap` or `pre`.
     * Its width is estimated from its text at the pane's own type size,
     * which is enough to tell a phrase that fits from a forty-column
     * table row that cannot.
     */
    fun minimumWidthCssPx(html: String): Int {
        var widest = 0
        startTag.findAll(html).forEach { match ->
            val name = match.groupValues[1].lowercase()
            val attributes = match.groupValues[2]
            if (name != "nobr" && !holdsNowrap(attributes)) return@forEach
            val text = plainTextOf(html, match.range.last + 1, name)
            widest = maxOf(widest, (text.length * AVERAGE_CHAR_PX).toInt())
        }
        return widest
    }

    /** The average advance of the reading pane's 15px sans-serif text. */
    private const val AVERAGE_CHAR_PX = 7.0

    private val startTag = Regex("""<([a-zA-Z][a-zA-Z0-9]*)((?:"[^"]*"|'[^']*'|[^>"'])*)>""")
    private val widthAttribute =
        Regex("""\swidth\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)""", RegexOption.IGNORE_CASE)
    private val heightAttribute =
        Regex("""\sheight\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)""", RegexOption.IGNORE_CASE)
    private val styleAttribute =
        Regex("""\sstyle\s*=\s*("[^"]*"|'[^']*')""", RegexOption.IGNORE_CASE)
    private val nowrapAttribute = Regex("""\snowrap(\s|=|/|$)""", RegexOption.IGNORE_CASE)
    private val length = Regex("""^(\d+(?:\.\d+)?)\s*(px|pt)?$""", RegexOption.IGNORE_CASE)
    private val markup = Regex("""<[^>]*>""")

    /** The value of a matched attribute, with its quotes taken off. */
    private fun attributeValue(match: MatchResult): String =
        match.groupValues[1].trim().removeSurrounding("\"").removeSurrounding("'").trim()

    /** A length in CSS pixels, or null when it is a percentage or junk. */
    private fun pixels(value: String): Int? {
        val parsed = length.matchEntire(value.trim()) ?: return null
        val number = parsed.groupValues[1].toDoubleOrNull() ?: return null
        val unit = parsed.groupValues[2].lowercase()
        return if (unit == "pt") (number * 4 / 3).toInt() else number.toInt()
    }

    private fun rewriteStyle(style: String, contentWidthCssPx: Int): String =
        style.split(';').mapNotNull { declaration ->
            if (declaration.isBlank()) return@mapNotNull null
            val property = declaration.substringBefore(':').trim().lowercase()
            val value = declaration.substringAfter(':', "").trim()
            val declared = pixels(value)
            if (declared == null || declared <= contentWidthCssPx) return@mapNotNull declaration.trim()
            when (property) {
                "width" -> "width:auto"
                "max-width" -> "max-width:100%"
                "min-width" -> null
                else -> declaration.trim()
            }
        }.joinToString(";")

    private fun holdsNowrap(attributes: String): Boolean {
        if (nowrapAttribute.containsMatchIn(attributes)) return true
        val style = styleAttribute.find(attributes)?.let { attributeValue(it) } ?: return false
        return style.split(';').any {
            it.substringBefore(':').trim().lowercase() == "white-space" &&
                it.substringAfter(':', "").trim().lowercase() in setOf("nowrap", "pre")
        }
    }

    /** The text an element holds, from [from] to the first `</name>`. */
    private fun plainTextOf(html: String, from: Int, name: String): String {
        val end = html.indexOf("</$name", from, ignoreCase = true).let { if (it < 0) html.length else it }
        return markup.replace(html.substring(from, end), " ").replace(Regex("""\s+"""), " ").trim()
    }
}
