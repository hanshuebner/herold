package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertContains
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class HtmlSanitizerTest {

    @Test
    fun stripsScriptsIframesAndEventHandlers() {
        val result = HtmlSanitizer.sanitize(
            """<p onclick="steal()">hi</p><script>evil()</script><iframe src="http://x"></iframe>""",
        )

        assertFalse(result.html.contains("script", ignoreCase = true))
        assertFalse(result.html.contains("iframe", ignoreCase = true))
        assertFalse(result.html.contains("onclick", ignoreCase = true))
        assertContains(result.html, "hi")
    }

    @Test
    fun neutralisesJavascriptTargets() {
        val result = HtmlSanitizer.sanitize("""<a href="javascript:alert(1)">x</a>""")

        assertFalse(result.html.contains("javascript:", ignoreCase = true))
    }

    @Test
    fun rewritesInlineImagesOntoTheSchemeTheReadingPaneResolves() {
        val result = HtmlSanitizer.sanitize("""<img src="cid:logo@example" alt="logo">""")

        assertContains(result.html, "${HtmlSanitizer.INLINE_SCHEME}logo@example")
        assertFalse(result.blockedRemoteImages)
    }

    @Test
    fun holdsBackRemoteImagesUntilTheUserAsksForThem() {
        val blocked = HtmlSanitizer.sanitize("""<img src="https://tracker.example/pixel.gif">""")
        assertTrue(blocked.blockedRemoteImages)
        assertContains(blocked.html, "data-blocked-src=\"https://tracker.example/pixel.gif\"")

        val loaded = HtmlSanitizer.sanitize(
            """<img src="https://tracker.example/pixel.gif">""",
            loadRemoteImages = true,
        )
        assertFalse(loaded.blockedRemoteImages)
        assertContains(loaded.html, "src=\"https://tracker.example/pixel.gif\"")
    }

    @Test
    fun keepsDataUriImages() {
        val result = HtmlSanitizer.sanitize("""<img src="data:image/png;base64,AAAA">""")

        assertContains(result.html, "data:image/png;base64,AAAA")
        assertFalse(result.blockedRemoteImages)
    }

    /**
     * The fragment a quote embeds (issue #431): the reader's markup
     * without anything that acts, styles or wraps a document.
     */
    @Test
    fun aQuoteFragmentKeepsTheMarkupAndDropsTheDocument() {
        val fragment = HtmlSanitizer.quoteFragment(
            "<!DOCTYPE html><html><head><title>Newsletter</title>" +
                "<style>body { display: none }</style></head>" +
                "<body><h1>Heading</h1><table><tr><td>cell</td></tr></table>" +
                "<script>evil()</script></body></html>",
        )

        assertContains(fragment, "<h1>Heading</h1>")
        assertContains(fragment, "<td>cell</td>")
        assertFalse(fragment.contains("DOCTYPE", ignoreCase = true))
        assertFalse(fragment.contains("<html", ignoreCase = true))
        assertFalse(fragment.contains("<body", ignoreCase = true))
        assertFalse(fragment.contains("<style", ignoreCase = true))
        assertFalse(fragment.contains("Newsletter"))
        assertFalse(fragment.contains("script", ignoreCase = true))
    }

    /** A quoted image keeps the reference the original wrote. */
    @Test
    fun aQuoteFragmentLeavesImageSourcesAlone() {
        val fragment = HtmlSanitizer.quoteFragment(
            """<img src="cid:logo@example"><img src="https://tracker.example/pixel.gif">""",
        )

        assertContains(fragment, "src=\"cid:logo@example\"")
        assertContains(fragment, "src=\"https://tracker.example/pixel.gif\"")
    }

    /** A body on its way out carries the URL the block held back. */
    @Test
    fun aBlockedRemoteImageGoesOutWithItsUrl() {
        val blocked = HtmlSanitizer.sanitize("""<img src="https://tracker.example/pixel.gif" width="4">""").html
        val restored = HtmlSanitizer.restoreBlockedImages(blocked)

        assertContains(restored, "src=\"https://tracker.example/pixel.gif\"")
        assertContains(restored, "width=\"4\"")
        assertFalse(restored.contains("data-blocked-src"))
        // An image that was never blocked is left as it is.
        assertContains(HtmlSanitizer.restoreBlockedImages("""<img src="cid:logo">"""), "src=\"cid:logo\"")
    }

    @Test
    fun theDocumentLaysABodyOutToTheCardsWidth() {
        val document = HtmlSanitizer.document("<p>Body</p>", darkTheme = false)

        // The viewport the WebView's wide-viewport setting reads.
        assertContains(document, "width=device-width, initial-scale=1")
        // Every box is capped at the width it was given, and what cannot
        // be narrowed scrolls inside the body's own box (issue #430).
        assertContains(document, "body * { max-width: 100%; }")
        assertContains(document, ".${HtmlSanitizer.BODY_CLASS} { overflow-x: auto; }")
        assertContains(document, "<div class=\"${HtmlSanitizer.BODY_CLASS}\"><p>Body</p></div>")
    }

    @Test
    fun plainTextBodiesAreEscapedNotInterpreted() {
        val html = HtmlSanitizer.fromPlainText("<b>not bold</b> & co")

        assertContains(html, "&lt;b&gt;not bold&lt;/b&gt; &amp; co")
    }
}
