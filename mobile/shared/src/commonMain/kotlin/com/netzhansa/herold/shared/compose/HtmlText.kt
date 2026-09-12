package com.netzhansa.herold.shared.compose

/**
 * The HTML <-> plain-text projections compose needs: the `text/plain`
 * alternative sent alongside the rich body, and the paragraph markup a
 * quoted plain-text original is wrapped in.
 *
 * The suite does this with the browser's DOM parser
 * (`web/apps/suite/src/lib/compose/compose.svelte.ts`, `htmlToPlainText`);
 * the shared core has no DOM, so the same block-level rules are applied by
 * a small tag scanner. The rules are the ones that matter to a reader of
 * the plain part: block elements end a line, list items get "- ", quoted
 * blocks get "> ", and a link keeps its target.
 */
object HtmlText {

    private val blockTags = setOf(
        "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "pre", "hr", "table", "tr",
    )

    /**
     * The `text/plain` alternative of an HTML body (suite REQ-MAIL-06).
     * Block structure survives as line breaks; `<blockquote>` content is
     * prefixed with "> " so a quoted reply still reads as one.
     */
    fun toPlainText(html: String): String {
        val out = StringBuilder()
        var quoteDepth = 0
        var atLineStart = true
        var pendingHref: String? = null
        var index = 0

        fun appendText(text: String) {
            if (text.isEmpty()) return
            if (atLineStart && quoteDepth > 0) out.append("> ".repeat(quoteDepth))
            out.append(text)
            atLineStart = false
        }

        fun newline() {
            if (!atLineStart) {
                out.append('\n')
                atLineStart = true
            }
        }

        while (index < html.length) {
            if (html[index] == '<') {
                val close = html.indexOf('>', index)
                if (close < 0) break
                val tag = html.substring(index + 1, close)
                val closing = tag.startsWith("/")
                val name = tag.trimStart('/').takeWhile { !it.isWhitespace() && it != '/' }.lowercase()
                when {
                    name == "br" -> {
                        if (atLineStart && quoteDepth > 0) out.append("> ".repeat(quoteDepth))
                        out.append('\n')
                        atLineStart = true
                    }

                    name == "li" && !closing -> appendText("- ")
                    name == "li" && closing -> newline()
                    name == "blockquote" && !closing -> {
                        newline()
                        quoteDepth++
                    }

                    name == "blockquote" && closing -> {
                        newline()
                        if (quoteDepth > 0) quoteDepth--
                    }

                    name == "a" && !closing -> pendingHref = attributeOf(tag, "href")
                    name == "img" -> attributeOf(tag, "alt")?.takeIf { it.isNotBlank() }
                        ?.let { appendText("[$it]") }

                    name in blockTags -> newline()
                }
                index = close + 1
                continue
            }
            val nextTag = html.indexOf('<', index).let { if (it < 0) html.length else it }
            val text = decodeEntities(html.substring(index, nextTag))
                .replace('\n', ' ')
                .replace('\r', ' ')
                .replace(Regex(" {2,}"), " ")
            if (text.isNotBlank()) {
                appendText(if (atLineStart) text.trimStart() else text)
                pendingHref?.let { href ->
                    if (!text.contains(href)) appendText(" ($href)")
                    pendingHref = null
                }
            }
            index = nextTag
        }
        return out.toString()
            .replace(Regex("[ \t]+\n"), "\n")
            .replace(Regex("\n{3,}"), "\n\n")
            .trim()
    }

    /** Wraps plain text as paragraph markup, for quoting a text-only original. */
    fun toHtml(text: String): String {
        if (text.isBlank()) return ""
        return text.split(Regex("\n{2,}")).joinToString("") { paragraph ->
            "<p>" + escape(paragraph).replace("\n", "<br>") + "</p>"
        }
    }

    /** HTML-escapes a value for insertion as element text or an attribute. */
    fun escape(value: String): String = value
        .replace("&", "&amp;")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
        .replace("\"", "&quot;")

    private fun attributeOf(tag: String, name: String): String? {
        val match = Regex("""\b$name\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))""", RegexOption.IGNORE_CASE)
            .find(tag) ?: return null
        return match.groupValues.drop(2).firstOrNull { it.isNotEmpty() }
    }

    private fun decodeEntities(value: String): String = value
        .replace("&nbsp;", " ")
        .replace("&lt;", "<")
        .replace("&gt;", ">")
        .replace("&quot;", "\"")
        .replace("&#39;", "'")
        .replace("&amp;", "&")
}
