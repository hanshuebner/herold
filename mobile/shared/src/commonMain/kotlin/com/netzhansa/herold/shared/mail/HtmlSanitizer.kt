package com.netzhansa.herold.shared.mail

/**
 * Prepares a message's HTML body for the reading pane's WebView.
 *
 * The WebView runs with JavaScript off, so this is the second line of
 * defence rather than the only one: active elements and event handlers are
 * removed, `javascript:` targets are neutralised, inline `cid:` images are
 * rewritten onto a scheme the WebView resolves out of the blob cache, and
 * remote images are held back until the user asks for them (suite
 * `09-ui-layout.md` reading pane, `REQ-SEC-07`).
 */
object HtmlSanitizer {

    /** Scheme the reading pane intercepts to serve an inline image from the blob cache. */
    const val INLINE_SCHEME = "https://inline.herold.invalid/"

    /** The box the body renders in, which is as wide as the card. */
    const val BODY_CLASS = "herold-body"

    private val activeElements = listOf("script", "iframe", "object", "embed", "applet", "form", "meta", "link")

    private val eventHandler = Regex("""\son[a-zA-Z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)""", RegexOption.IGNORE_CASE)
    private val javascriptUrl = Regex("""(href|src|action)\s*=\s*("|')\s*javascript:[^"']*("|')""", RegexOption.IGNORE_CASE)
    private val imgTag = Regex("""<img\b[^>]*>""", RegexOption.IGNORE_CASE)
    private val srcAttribute = Regex("""\bsrc\s*=\s*("([^"]*)"|'([^']*)')""", RegexOption.IGNORE_CASE)

    /**
     * @param html the message's HTML body
     * @param loadRemoteImages whether `http(s)` images may load; false leaves
     *   a placeholder so opening a message does not report back to the sender
     * @param collapseQuotes whether a trailing quoted history folds behind a
     *   control the reader opens; the reading pane asks for it, the composer's
     *   editable body does not (issue #432)
     * @return the sanitised document plus whether it held back a remote image
     */
    fun sanitize(
        html: String,
        loadRemoteImages: Boolean = false,
        collapseQuotes: Boolean = false,
    ): SanitizedHtml {
        var out = html
        activeElements.forEach { tag ->
            out = Regex("""<$tag\b[\s\S]*?</$tag\s*>""", RegexOption.IGNORE_CASE).replace(out, "")
            out = Regex("""<$tag\b[^>]*/?>""", RegexOption.IGNORE_CASE).replace(out, "")
        }
        out = eventHandler.replace(out, "")
        out = javascriptUrl.replace(out) { match -> "${match.groupValues[1]}=\"#\"" }

        var blockedRemoteImages = false
        out = imgTag.replace(out) { match ->
            val tag = match.value
            val src = srcAttribute.find(tag)?.let { it.groupValues[2].ifEmpty { it.groupValues[3] } }.orEmpty()
            when {
                src.startsWith("cid:", ignoreCase = true) -> {
                    val cid = src.removePrefix("cid:").removePrefix("CID:").trim('<', '>')
                    srcAttribute.replace(tag) { "src=\"$INLINE_SCHEME$cid\"" }
                }

                src.startsWith("data:", ignoreCase = true) -> tag

                src.startsWith("http://", ignoreCase = true) || src.startsWith("https://", ignoreCase = true) -> {
                    if (loadRemoteImages) {
                        tag
                    } else {
                        blockedRemoteImages = true
                        srcAttribute.replace(tag) { "src=\"\" data-blocked-src=\"$src\"" }
                    }
                }

                else -> tag
            }
        }
        if (collapseQuotes) out = QuotedHtml.collapse(out)
        return SanitizedHtml(html = out, blockedRemoteImages = blockedRemoteImages)
    }

    /**
     * Wraps a plain-text body so the same renderer shows it.
     *
     * With [collapseQuotes] the trailing citation - the run of
     * `>`-prefixed lines and the attribution line above it - folds
     * behind the same control the HTML path uses (issue #432). The
     * composer asks for the plain wrapping, since what it wraps is a
     * body about to be sent.
     */
    fun fromPlainText(text: String, collapseQuotes: Boolean = false): String {
        if (!collapseQuotes) return preformatted(text)
        val split = QuotedText.split(text)
        if (split.collapsed.isEmpty()) return preformatted(text)
        val head = if (split.head.isBlank()) "" else preformatted(split.head)
        return head + QuotedHtml.foldedRegion(preformatted(split.collapsed))
    }

    private fun preformatted(text: String): String =
        "<pre style=\"white-space:pre-wrap;word-wrap:break-word;font-family:sans-serif\">" +
            text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;") +
            "</pre>"

    /** Adds the viewport and colour rules the reading pane renders with. */
    fun document(body: String, darkTheme: Boolean): String {
        val background = if (darkTheme) "#1b1b1b" else "#ffffff"
        val foreground = if (darkTheme) "#e6e6e6" else "#1b1b1b"
        val muted = if (darkTheme) "#a6a6a6" else "#525252"
        val rule = if (darkTheme) "#4a4a4a" else "#c6c6c6"
        val chip = if (darkTheme) "#393939" else "#f0f0f0"
        val chipOpen = if (darkTheme) "#525252" else "#e0e0e0"
        return """
            <!DOCTYPE html>
            <html><head>
            <meta name="viewport" content="width=device-width, initial-scale=1">
            <style>
              body { margin: 0; padding: 12px; background: $background; color: $foreground;
                     font-family: sans-serif; font-size: 15px; line-height: 1.45;
                     overflow-wrap: break-word; }
              img { max-width: 100%; height: auto; }
              /* A document written for a desktop pane declares pixel widths
                 a phone does not have. Capping every box at the width it was
                 given reflows the document to the card at its own text size
                 (issue #430). */
              body * { max-width: 100%; }
              /* What no reflow can narrow - a row that refuses to wrap, a
                 fixed table - keeps its width and scrolls sideways in this
                 box, which is as wide as the card. The conversation around
                 it only ever scrolls up and down. */
              .$BODY_CLASS { overflow-x: auto; }
              table { border-collapse: collapse; }
              a { color: #4c8dff; }
              blockquote { border-left: 3px solid $rule; margin: 0 0 0 8px;
                           padding: 0 0 0 12px; color: $muted; }
              /* The quoted history's fold (issue #432). <details> opens and
                 closes on a tap with no script, which is what the reading
                 pane's WebView allows; the two labels swap on the open
                 state, so the chip reads as a control either way. */
              details.${QuotedHtml.DETAILS_CLASS} { margin: 8px 0; }
              details.${QuotedHtml.DETAILS_CLASS} > summary {
                     cursor: pointer; list-style: none; display: inline-block;
                     padding: 6px 14px; margin-bottom: 8px; background: $chip;
                     color: $muted; border-radius: 16px; font-size: 14px; }
              details.${QuotedHtml.DETAILS_CLASS} > summary::-webkit-details-marker { display: none; }
              details.${QuotedHtml.DETAILS_CLASS}[open] > summary { background: $chipOpen; }
              .${QuotedHtml.HIDE_CLASS} { display: none; }
              details.${QuotedHtml.DETAILS_CLASS}[open] .${QuotedHtml.SHOW_CLASS} { display: none; }
              details.${QuotedHtml.DETAILS_CLASS}[open] .${QuotedHtml.HIDE_CLASS} { display: inline; }
            </style>
            </head><body><div class="$BODY_CLASS">$body</div></body></html>
        """.trimIndent()
    }
}

/** A sanitised body and what it held back. */
data class SanitizedHtml(
    val html: String,
    val blockedRemoteImages: Boolean,
)
