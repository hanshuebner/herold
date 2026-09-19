package com.netzhansa.herold.shared.mail

/**
 * A minimal HTML tree for the passes that need structure rather than
 * pattern matching: the quoted-history fold walks siblings, moves nodes
 * and wraps a run of them in a new element (issue #432).
 *
 * Every node keeps the source text it was parsed from, so serialising a
 * tree nobody touched returns the input byte for byte: an implicitly
 * closed element carries an empty end tag, an end tag with no open
 * element stays raw text, and unknown markup passes through untouched.
 * The tree is therefore safe to build for any body, however malformed,
 * and the fold either finds its shape or leaves the document as it was.
 */
internal sealed class HtmlNode {
    var parent: HtmlElement? = null

    abstract fun render(out: StringBuilder)

    /** The visible text of this node and its descendants, entities decoded. */
    abstract fun text(out: StringBuilder)

    fun render(): String = StringBuilder().also { render(it) }.toString()

    fun text(): String = StringBuilder().also { text(it) }.toString()
}

/** Text, a comment or a doctype: anything outside a tag, kept verbatim. */
internal class HtmlText(val raw: String, private val visible: Boolean = true) : HtmlNode() {
    override fun render(out: StringBuilder) {
        out.append(raw)
    }

    override fun text(out: StringBuilder) {
        if (visible) out.append(decodeEntities(raw))
    }
}

/** An element, with the source of its own start and end tags. */
internal class HtmlElement(
    val name: String,
    private val startTag: String,
    private val selfClosing: Boolean = false,
) : HtmlNode() {
    val children = mutableListOf<HtmlNode>()

    /** The source of the end tag, empty when the element was never closed. */
    var endTag: String = ""

    val classes: String by lazy { attribute("class").orEmpty() }

    fun attribute(name: String): String? {
        val match = Regex(
            """\s$name\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))""",
            RegexOption.IGNORE_CASE,
        ).find(startTag) ?: return null
        return match.groupValues.drop(2).firstOrNull { it.isNotEmpty() }
    }

    override fun render(out: StringBuilder) {
        out.append(startTag)
        children.forEach { it.render(out) }
        out.append(endTag)
    }

    override fun text(out: StringBuilder) {
        if (selfClosing) return
        children.forEach { it.text(out) }
    }

    fun append(node: HtmlNode) {
        node.parent?.children?.remove(node)
        node.parent = this
        children.add(node)
    }

    fun insertBefore(node: HtmlNode, reference: HtmlNode) {
        val at = children.indexOf(reference)
        require(at >= 0) { "reference node is not a child" }
        node.parent?.children?.remove(node)
        node.parent = this
        children.add(at, node)
    }

    fun indexOf(node: HtmlNode): Int = children.indexOf(node)
}

/** The parsed document: a root element holding the body's top-level nodes. */
internal object HtmlDom {

    private val voidElements = setOf(
        "area", "base", "br", "col", "embed", "hr", "img", "input",
        "link", "meta", "param", "source", "track", "wbr",
    )

    /** Elements whose content is text, never markup. */
    private val rawTextElements = setOf("style", "script", "textarea", "title")

    /** Start tags that close an open `<p>`. */
    private val closesParagraph = setOf(
        "address", "article", "aside", "blockquote", "details", "div", "dl",
        "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3",
        "h4", "h5", "h6", "header", "hr", "main", "menu", "nav", "ol", "p",
        "pre", "section", "table", "ul",
    )

    /** Elements an equally named or sibling start tag closes implicitly. */
    private val implicitlyClosedBy = mapOf(
        "li" to setOf("li"),
        "dt" to setOf("dt", "dd"),
        "dd" to setOf("dt", "dd"),
        "option" to setOf("option"),
        "td" to setOf("td", "th", "tr"),
        "th" to setOf("td", "th", "tr"),
        "tr" to setOf("tr"),
        "thead" to setOf("tbody", "tfoot"),
        "tbody" to setOf("tbody", "tfoot"),
    )

    private val tagStart = Regex("""<(/?)([a-zA-Z][a-zA-Z0-9:-]*)""")

    fun parse(html: String): HtmlElement {
        val root = HtmlElement("#root", "")
        val open = mutableListOf(root)
        var i = 0
        val pending = StringBuilder()

        fun flushText() {
            if (pending.isNotEmpty()) {
                open.last().append(HtmlText(pending.toString()))
                pending.clear()
            }
        }

        while (i < html.length) {
            val lt = html.indexOf('<', i)
            if (lt < 0) {
                pending.append(html, i, html.length)
                break
            }
            pending.append(html, i, lt)

            // A comment, a doctype or a CDATA block is kept verbatim and
            // contributes no visible text.
            if (html.startsWith("<!--", lt)) {
                val end = html.indexOf("-->", lt + 4)
                val stop = if (end < 0) html.length else end + 3
                flushText()
                open.last().append(HtmlText(html.substring(lt, stop), visible = false))
                i = stop
                continue
            }
            if (html.startsWith("<!", lt) || html.startsWith("<?", lt)) {
                val end = html.indexOf('>', lt)
                val stop = if (end < 0) html.length else end + 1
                flushText()
                open.last().append(HtmlText(html.substring(lt, stop), visible = false))
                i = stop
                continue
            }

            val match = tagStart.matchAt(html, lt)
            if (match == null) {
                // A stray "<" that starts no tag is content.
                pending.append('<')
                i = lt + 1
                continue
            }
            val end = endOfTag(html, lt)
            if (end < 0) {
                pending.append(html, lt, html.length)
                break
            }
            val source = html.substring(lt, end + 1)
            val name = match.groupValues[2].lowercase()
            val closing = match.groupValues[1] == "/"
            flushText()

            if (closing) {
                val at = open.indexOfLast { it.name == name }
                if (at <= 0) {
                    // No element to close: the tag is content.
                    open.last().append(HtmlText(source, visible = false))
                } else {
                    open[at].endTag = source
                    while (open.size > at) open.removeAt(open.size - 1)
                }
                i = end + 1
                continue
            }

            closeImplicitly(open, name)
            val selfClosing = name in voidElements || source.trimEnd().endsWith("/>")
            val element = HtmlElement(name, source, selfClosing)
            open.last().append(element)
            if (name in voidElements) {
                i = end + 1
                continue
            }
            if (name in rawTextElements) {
                val closeAt = indexOfClosing(html, name, end + 1)
                val stop = if (closeAt < 0) html.length else closeAt
                if (stop > end + 1) {
                    element.append(HtmlText(html.substring(end + 1, stop), visible = false))
                }
                if (closeAt >= 0) {
                    val closeEnd = html.indexOf('>', closeAt)
                    val after = if (closeEnd < 0) html.length else closeEnd + 1
                    element.endTag = html.substring(closeAt, after)
                    i = after
                } else {
                    i = html.length
                }
                continue
            }
            if (!source.trimEnd().endsWith("/>")) open.add(element)
            i = end + 1
        }
        flushText()
        return root
    }

    /** Serialises a tree; an untouched tree renders as its source. */
    fun render(root: HtmlElement): String =
        StringBuilder().also { out -> root.children.forEach { it.render(out) } }.toString()

    /**
     * The index of the tag's closing ">", skipping the ones inside a
     * quoted attribute value so `<a title="a > b">` is one tag.
     */
    private fun endOfTag(html: String, start: Int): Int {
        var i = start + 1
        var quote = ' '
        while (i < html.length) {
            val c = html[i]
            when {
                quote != ' ' -> if (c == quote) quote = ' '
                c == '"' || c == '\'' -> quote = c
                c == '>' -> return i
            }
            i++
        }
        return -1
    }

    private fun indexOfClosing(html: String, name: String, from: Int): Int {
        var i = from
        while (i < html.length) {
            val lt = html.indexOf("</", i)
            if (lt < 0) return -1
            val match = tagStart.matchAt(html, lt)
            if (match != null && match.groupValues[2].lowercase() == name) return lt
            i = lt + 2
        }
        return -1
    }

    private fun closeImplicitly(open: MutableList<HtmlElement>, starting: String) {
        while (open.size > 1) {
            val top = open.last().name
            val closes = when {
                top == "p" -> starting in closesParagraph
                else -> implicitlyClosedBy[top]?.contains(starting) == true
            }
            if (!closes) return
            open.removeAt(open.size - 1)
        }
    }
}

private val namedEntities = mapOf(
    "amp" to "&", "lt" to "<", "gt" to ">", "quot" to "\"", "apos" to "'",
    "nbsp" to " ", "ndash" to "-", "mdash" to "-", "hellip" to "...",
    "auml" to "a", "ouml" to "o", "uuml" to "u", "szlig" to "ss",
)

private val entity = Regex("""&(#[0-9]+|#[xX][0-9a-fA-F]+|[a-zA-Z][a-zA-Z0-9]*);""")

/** Decodes the entities an attribution line plausibly carries. */
internal fun decodeEntities(text: String): String =
    if (!text.contains('&')) {
        text
    } else {
        entity.replace(text) { match ->
            val body = match.groupValues[1]
            when {
                body.startsWith("#x", ignoreCase = true) ->
                    body.drop(2).toIntOrNull(16)?.let { charOf(it) } ?: match.value

                body.startsWith("#") ->
                    body.drop(1).toIntOrNull()?.let { charOf(it) } ?: match.value

                else -> namedEntities[body.lowercase()] ?: match.value
            }
        }
    }

private fun charOf(code: Int): String? =
    if (code in 1..0x10FFFF) {
        StringBuilder().appendCodePointCompat(code).toString()
    } else {
        null
    }

private fun StringBuilder.appendCodePointCompat(code: Int): StringBuilder {
    if (code <= 0xFFFF) {
        append(code.toChar())
    } else {
        val v = code - 0x10000
        append((0xD800 + (v shr 10)).toChar())
        append((0xDC00 + (v and 0x3FF)).toChar())
    }
    return this
}
