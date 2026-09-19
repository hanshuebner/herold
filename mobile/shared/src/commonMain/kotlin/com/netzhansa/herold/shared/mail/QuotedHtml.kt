package com.netzhansa.herold.shared.mail

/**
 * Folds the quoted history of a reply behind a control the reader
 * opens, on the contract the Suite renders with
 * (`web/apps/suite/src/lib/mail/sanitize.ts` `collapseQuotedRegions`,
 * issue #432).
 *
 * The rules, all of them the Suite's:
 *
 *  - At most one region folds, and only when it trails: everything
 *    after it to the end of the body is quoted, inert or a signature
 *    block. A bottom-posted or interleaved reply keeps its quotes
 *    expanded, since the context is what the fresh text answers.
 *  - A citation-introducer line ahead of the quote ("On ... wrote:",
 *    "Am ... schrieb ...:") folds with it; ordinary prose ahead of the
 *    quote does not.
 *  - Fresh text some clients nest as the leading children of the
 *    quote element itself is lifted out ahead of the fold.
 *
 * The fold is a `<details>` element, which opens and closes on a tap
 * with no script, so it works in the reading pane's WebView with
 * JavaScript off.
 */
internal object QuotedHtml {

    const val DETAILS_CLASS = "herold-quoted"
    const val SHOW_CLASS = "herold-quoted-show"
    const val HIDE_CLASS = "herold-quoted-hide"
    const val SHOW_LABEL = "Show trimmed content"
    const val HIDE_LABEL = "Hide trimmed content"

    private val quoteClass = Regex("gmail_quote|yahoo_quoted|moz-cite-prefix", RegexOption.IGNORE_CASE)

    /** [inner] behind the same control, for the plain-text path. */
    fun foldedRegion(inner: String): String {
        val details = details()
        details.append(HtmlText(inner))
        return details.render()
    }

    /** The body with its trailing quoted region folded, or as it was. */
    fun collapse(html: String): String {
        val root = HtmlDom.parse(html)
        if (!collapseIn(root)) return html
        return HtmlDom.render(root)
    }

    private fun collapseIn(root: HtmlElement): Boolean {
        val candidate = firstQuotedRegion(root) ?: return false
        splitLeadingFreshContent(candidate)
        val parent = candidate.parent ?: return false
        val at = parent.indexOf(candidate)
        if (at < 0) return false

        // Only a trailing region folds: walk the siblings that would be
        // absorbed with it. A signature delimiter ends the walk - a
        // signature runs to the end of the message and folds away with
        // the quote - and anything else with text of its own is fresh
        // content the reader wrote below the quote.
        var end = at + 1
        var inSignature = false
        while (end < parent.children.size) {
            val node = parent.children[end]
            if (!inSignature) {
                if (isSignatureDelimiter(node)) {
                    inSignature = true
                } else if (!isQuoteOrEmpty(node)) {
                    return false
                }
            }
            end++
        }

        // A citation-introducer line ahead of the quote folds with it.
        // Only the nearest non-empty sibling is examined: clients write
        // a newline between the introducer and the quote, and ordinary
        // prose there is the sender's own text.
        var start = at
        var back = at - 1
        while (back >= 0 && isQuoteOrEmpty(parent.children[back])) back--
        if (back >= 0 && isAttribution(parent.children[back])) start = back

        val folded = parent.children.subList(start, end).toList()
        val details = details()
        parent.insertBefore(details, parent.children[start])
        folded.forEach { details.append(it) }
        return true
    }

    /** The fold and its chip, which carries both of its labels. */
    private fun details(): HtmlElement {
        val details = HtmlElement("details", "<details class=\"$DETAILS_CLASS\">")
        details.endTag = "</details>"
        val summary = HtmlElement("summary", "<summary>")
        summary.endTag = "</summary>"
        summary.append(label(SHOW_CLASS, SHOW_LABEL))
        summary.append(label(HIDE_CLASS, HIDE_LABEL))
        details.append(summary)
        return details
    }

    private fun label(styleClass: String, text: String): HtmlElement {
        val span = HtmlElement("span", "<span class=\"$styleClass\">")
        span.endTag = "</span>"
        span.append(HtmlText(text))
        return span
    }

    /** The first quoted region in document order. */
    private fun firstQuotedRegion(root: HtmlElement): HtmlElement? {
        root.children.forEach { child ->
            if (child is HtmlElement) {
                if (isQuoteElement(child)) return child
                firstQuotedRegion(child)?.let { return it }
            }
        }
        return null
    }

    private fun isQuoteElement(element: HtmlElement): Boolean =
        element.name == "blockquote" ||
            (element.name == "div" && quoteClass.containsMatchIn(element.classes))

    /**
     * Lifts fresh text out of the quote element it was written into.
     * Some clients put a top-posted reply among the quote's own leading
     * children rather than ahead of it; without this the whole element,
     * the reply included, folds as one block.
     */
    private fun splitLeadingFreshContent(candidate: HtmlElement) {
        val parent = candidate.parent ?: return
        val boundary = candidate.children.indexOfFirst { isQuoteStart(it) }
        if (boundary <= 0) return
        val leading = candidate.children.subList(0, boundary).toList()
        if (leading.none { !isQuoteOrEmpty(it) }) return
        leading.forEach { parent.insertBefore(it, candidate) }
    }

    /** True for a node that itself marks where quoted material begins. */
    private fun isQuoteStart(node: HtmlNode): Boolean {
        if (node is HtmlElement && isQuoteElement(node)) return true
        if (isAttribution(node)) return true
        return isQuoteWrapper(node)
    }

    /**
     * True for a plain element that bundles an attribution line or a
     * quote as its own first content, which is how Thunderbird and
     * Apple Mail wrap the two together.
     */
    private fun isQuoteWrapper(node: HtmlNode): Boolean {
        if (node !is HtmlElement) return false
        val first = firstMeaningfulChild(node) ?: return false
        if (first is HtmlElement && isQuoteElement(first)) return true
        return isAttribution(first)
    }

    private fun firstMeaningfulChild(element: HtmlElement): HtmlNode? =
        element.children.firstOrNull { child ->
            when (child) {
                is HtmlText -> child.raw.isNotBlank()
                is HtmlElement -> child.name != "br" && child.name != "hr"
            }
        }

    /**
     * True for the nodes that fold alongside the quote: whitespace,
     * separators, further quoted blocks and elements with no text of
     * their own.
     */
    private fun isQuoteOrEmpty(node: HtmlNode): Boolean = when (node) {
        is HtmlText -> node.raw.isBlank() || node.text().isBlank()
        is HtmlElement -> when {
            node.name == "br" || node.name == "hr" -> true
            isQuoteElement(node) -> true
            else -> node.text().isBlank()
        }
    }

    private fun isSignatureDelimiter(node: HtmlNode): Boolean =
        QuotedText.isSignatureDelimiter(node.text().trim())

    private fun isAttribution(node: HtmlNode): Boolean =
        QuotedText.isAttributionLine(node.text().trim())
}
