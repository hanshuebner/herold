package com.netzhansa.herold.shared.links

import com.netzhansa.herold.shared.mail.HtmlSanitizer

/**
 * What a tap on a link in a message body asks the shell to do
 * (REQ-AND-SYS-04, issue #425).
 */
sealed interface BodyLinkAction {

    /** A web page: the browser shows it, outside the message surface. */
    data class OpenExternally(val url: String) : BodyLinkAction

    /** A `mailto:` address: the composer opens on [prefill]. */
    data class Compose(val prefill: ComposePrefill) : BodyLinkAction

    /** A scheme another app owns (`tel:`, `sms:`, `geo:`): the system takes it. */
    data class HandOff(val uri: String) : BodyLinkAction

    /** Nothing happens and the message stays on screen. */
    data object Ignore : BodyLinkAction
}

/**
 * The scheme dispatch behind every navigation a message body attempts.
 *
 * The reading pane's WebView renders one sanitised document and never
 * navigates: each link is resolved here and acted on outside the WebView,
 * so a tap can neither replace the message with a fetched page nor blank
 * the surface (issue #425). The dispatch is pure text work, so it lives in
 * the shared module and the iOS client reuses it.
 */
object BodyLinks {

    /**
     * Schemes that stay inside the process. `javascript:` and `data:` are
     * script and content the sender controls, `file:` and `content:` are
     * the device's own storage, and `intent:` addresses arbitrary
     * components by name - handing any of them to the system would let a
     * message reach past the reading pane.
     */
    private val contained = setOf("javascript", "data", "about", "file", "content", "blob", "intent", "android-app")

    fun route(url: String): BodyLinkAction {
        val trimmed = url.trim()
        if (trimmed.isEmpty() || trimmed.startsWith("#")) return BodyLinkAction.Ignore
        if (trimmed.startsWith(HtmlSanitizer.INLINE_SCHEME)) return BodyLinkAction.Ignore
        val scheme = ParsedUri.of(trimmed)?.scheme ?: return BodyLinkAction.Ignore
        return when {
            scheme in contained -> BodyLinkAction.Ignore
            scheme == "http" || scheme == "https" -> BodyLinkAction.OpenExternally(trimmed)
            scheme == "mailto" -> BodyLinkAction.Compose(Mailto.parse(trimmed) ?: ComposePrefill())
            else -> BodyLinkAction.HandOff(trimmed)
        }
    }
}
