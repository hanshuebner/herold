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
 *    quote element itself is lifted out ahead of the fold. The
 *    attribution it ends at is read as one line across the nodes it is
 *    written over - Thunderbird spreads it over a text node, a
 *    `mailto:` link and the colon after it.
 *  - The fold begins at the citation. An element whose leading content
 *    is the sender's own text is left outside it, and the search
 *    carries on with the quoted material inside or after that element.
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

    // Thunderbird's class for the citation-prefix line. What it names is
    // the attribution, not a quote container, and a reply composed above
    // the citation lands in it alongside the attribution - so it only
    // starts a fold when the sender's own text does not precede the
    // attribution inside it (issue #432).
    private val citationPrefixClass = Regex("moz-cite-prefix", RegexOption.IGNORE_CASE)

    /** How long the text an attribution line is read from may run. */
    private const val ATTRIBUTION_MAX_CHARS = 512

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

    /**
     * Folds the first quoted region that begins at the citation.
     *
     * A region whose own leading content is text the sender wrote is
     * passed over rather than folded: the reply reads above the chip,
     * and the search continues with the quoted material inside or after
     * it, which is what the reader asked to have out of the way.
     */
    private fun collapseIn(root: HtmlElement): Boolean {
        val passedOver = mutableListOf<HtmlElement>()
        var atTheFirstRegion = true
        while (true) {
            val candidate = firstQuotedRegion(root, passedOver) ?: return false
            // Where the sender's own text can be is a question of
            // position, not of the tag or class a region starts with.
            // It is either outside the quote or among the leading
            // children of the region the body opens with, never both
            // and nowhere else: from the first quoted region on, the
            // document is the quoted message, and reading any of it as
            // the sender's would put the correspondent's words on
            // screen as though they had just been written.
            val mayHoldTheSendersText = atTheFirstRegion && nothingPrecedes(root, candidate)
            atTheFirstRegion = false
            if (!mayHoldTheSendersText) return fold(candidate)

            splitLeadingFreshContent(candidate)
            if (startsAtTheCitation(candidate)) return fold(candidate)
            passedOver.add(candidate)
        }
    }

    /** True when [candidate] is the first thing in the body that reads. */
    private fun nothingPrecedes(root: HtmlElement, candidate: HtmlElement): Boolean {
        val ahead = StringBuilder()
        var reached = false

        fun walk(node: HtmlNode) {
            if (reached) return
            if (node === candidate) {
                reached = true
                return
            }
            when (node) {
                is HtmlText -> node.text(ahead)
                is HtmlElement -> node.children.forEach { walk(it) }
            }
        }

        root.children.forEach { walk(it) }
        return ahead.toString().isBlank()
    }

    private fun fold(candidate: HtmlElement): Boolean {
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

        // The rest of the document after the element the citation sits
        // in counts too: a client that wraps the citation and its quote
        // in a container of their own leaves a bottom-posted reply
        // outside that container, where the sibling walk above never
        // reaches it. Those nodes stay where they are - only the
        // candidate's own siblings are absorbed - but text among them
        // means the quote is not what the message ends with.
        var wrapper: HtmlElement? = parent
        while (wrapper != null) {
            val above = wrapper.parent ?: break
            var next = above.indexOf(wrapper) + 1
            while (next < above.children.size) {
                val node = above.children[next]
                if (!inSignature) {
                    if (isSignatureDelimiter(node)) {
                        inSignature = true
                    } else if (!isQuoteOrEmpty(node)) {
                        return false
                    }
                }
                next++
            }
            wrapper = above
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

    /** The first quoted region in document order, [passedOver] aside. */
    private fun firstQuotedRegion(root: HtmlElement, passedOver: List<HtmlElement>): HtmlElement? {
        root.children.forEach { child ->
            if (child is HtmlElement) {
                if (isQuoteElement(child) && passedOver.none { it === child }) return child
                firstQuotedRegion(child, passedOver)?.let { return it }
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
        val marker = candidate.children.indexOfFirst { isQuoteStart(it) }
        // A marker at the head of the element means the quoted
        // material starts there and nothing of the sender's precedes
        // it. Reading the text again for an attribution would find the
        // one that belongs to a deeper quote and cut the element open
        // at it.
        if (marker == 0) return
        val boundary = if (marker > 0) marker else attributionStart(candidate.children)
        if (boundary <= 0) return
        val leading = candidate.children.subList(0, boundary).toList()
        if (leading.none { !isQuoteOrEmpty(it) }) return
        leading.forEach { parent.insertBefore(it, candidate) }
    }

    /**
     * Where an attribution line spread over several children begins, or
     * -1 when the run of children ends in none.
     *
     * Thunderbird writes the attribution as a text node, a `mailto:`
     * link carrying the address and the colon after it, all of them
     * children of the same element as the reply text above. Read child
     * by child none of them is an attribution line; read together they
     * are one. The last child the remaining text reads as an
     * attribution from is the one it starts at, which keeps the line
     * itself out of the sender's visible text even when that text
     * happens to open with the same words the attribution does.
     *
     * The scan walks back from the end while the text it has gathered
     * is still short enough to be one line, so a long body costs a
     * glance at its tail rather than a pass over every suffix.
     */
    private fun attributionStart(children: List<HtmlNode>): Int {
        val tail = StringBuilder()
        for (at in children.indices.reversed()) {
            tail.insert(0, children[at].text())
            if (tail.length > ATTRIBUTION_MAX_CHARS) return -1
            if (at == 0) return -1
            val text = tail.toString().trim()
            if (text.isNotBlank() && QuotedText.isAttributionLine(text)) return at
        }
        return -1
    }

    /**
     * True when [element] holds no text of the sender's ahead of the
     * quoted material it carries.
     *
     * A `blockquote` and a client's quote container hold the quoted
     * message by construction. Thunderbird's citation-prefix div does
     * not: it names the attribution line, and a reply typed above the
     * citation is written into it ahead of that line. Such a div starts
     * the fold only once the attribution is what it leads with.
     */
    private fun startsAtTheCitation(element: HtmlElement): Boolean {
        if (!citationPrefixClass.containsMatchIn(element.classes)) return true
        val lead = StringBuilder()
        for (child in element.children) {
            if (isQuoteStart(child)) break
            child.text(lead)
        }
        val text = lead.toString().trim()
        return text.isBlank() || QuotedText.isAttributionLine(text)
    }

    /** True for a node that itself marks where quoted material begins. */
    private fun isQuoteStart(node: HtmlNode): Boolean {
        if (node is HtmlElement && isQuoteElement(node) && startsAtTheCitation(node)) return true
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
        if (first is HtmlElement && isQuoteElement(first) && startsAtTheCitation(first)) return true
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
            isQuoteElement(node) -> startsAtTheCitation(node)
            else -> node.text().isBlank()
        }
    }

    private fun isSignatureDelimiter(node: HtmlNode): Boolean =
        QuotedText.isSignatureDelimiter(node.text().trim())

    private fun isAttribution(node: HtmlNode): Boolean =
        QuotedText.isAttributionLine(node.text().trim())
}
