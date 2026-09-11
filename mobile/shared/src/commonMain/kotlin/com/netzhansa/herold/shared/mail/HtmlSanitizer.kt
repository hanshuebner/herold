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

    private val activeElements = listOf("script", "iframe", "object", "embed", "applet", "form", "meta", "link")

    private val eventHandler = Regex("""\son[a-zA-Z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)""", RegexOption.IGNORE_CASE)
    private val javascriptUrl = Regex("""(href|src|action)\s*=\s*("|')\s*javascript:[^"']*("|')""", RegexOption.IGNORE_CASE)
    private val imgTag = Regex("""<img\b[^>]*>""", RegexOption.IGNORE_CASE)
    private val srcAttribute = Regex("""\bsrc\s*=\s*("([^"]*)"|'([^']*)')""", RegexOption.IGNORE_CASE)

    /**
     * @param html the message's HTML body
     * @param loadRemoteImages whether `http(s)` images may load; false leaves
     *   a placeholder so opening a message does not report back to the sender
     * @return the sanitised document plus whether it held back a remote image
     */
    fun sanitize(html: String, loadRemoteImages: Boolean = false): SanitizedHtml {
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
        return SanitizedHtml(html = out, blockedRemoteImages = blockedRemoteImages)
    }

    /** Wraps a plain-text body so the same renderer shows it. */
    fun fromPlainText(text: String): String =
        "<pre style=\"white-space:pre-wrap;word-wrap:break-word;font-family:sans-serif\">" +
            text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;") +
            "</pre>"

    /** Adds the viewport and colour rules the reading pane renders with. */
    fun document(body: String, darkTheme: Boolean): String {
        val background = if (darkTheme) "#1b1b1b" else "#ffffff"
        val foreground = if (darkTheme) "#e6e6e6" else "#1b1b1b"
        return """
            <!DOCTYPE html>
            <html><head>
            <meta name="viewport" content="width=device-width, initial-scale=1">
            <style>
              body { margin: 0; padding: 12px; background: $background; color: $foreground;
                     font-family: sans-serif; font-size: 15px; line-height: 1.45;
                     overflow-wrap: break-word; }
              img { max-width: 100%; height: auto; }
              table { max-width: 100%; }
              a { color: #4c8dff; }
            </style>
            </head><body>$body</body></html>
        """.trimIndent()
    }
}

/** A sanitised body and what it held back. */
data class SanitizedHtml(
    val html: String,
    val blockedRemoteImages: Boolean,
)
