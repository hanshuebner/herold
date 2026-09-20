package com.netzhansa.herold.shared.mail

/** Which of a message's two bodies the reading pane shows. */
enum class BodyVariant { Html, Text }

/**
 * Which body a message is read as (issue #430).
 *
 * A `multipart/alternative` carries the same message twice, and the HTML
 * half is the one worth showing: it has the sender's images, links and
 * emphasis. It is worth showing as long as it fits the card. When the
 * HTML declares content that cannot narrow to a phone's width even after
 * [HtmlWidths.neutralise] has made its declared widths yield, the text
 * alternative is the readable one, and the HTML stays one tap away.
 */
object BodyPreference {

    /**
     * @param show the body the pane renders
     * @param offer the other body, when the reader is given a control to
     *   switch to it; null when the message reads as itself and there is
     *   nothing to offer
     */
    data class Choice(val show: BodyVariant, val offer: BodyVariant?)

    /**
     * @param hasHtml whether the message has an HTML body
     * @param text the plain-text alternative, if it carries one
     * @param minimumWidthCssPx how wide the HTML still needs to be
     *   ([HtmlWidths.minimumWidthCssPx])
     * @param contentWidthCssPx what the card gives the body
     * @param readerChose the variant the reader asked for, which beats
     *   every rule below
     */
    fun choose(
        hasHtml: Boolean,
        text: String?,
        minimumWidthCssPx: Int,
        contentWidthCssPx: Int,
        readerChose: BodyVariant? = null,
    ): Choice {
        val hasText = !text.isNullOrBlank()
        if (!hasHtml) return Choice(BodyVariant.Text, null)
        if (!hasText) return Choice(BodyVariant.Html, null)
        val fits = minimumWidthCssPx <= contentWidthCssPx
        if (fits && readerChose == null) return Choice(BodyVariant.Html, null)
        val show = readerChose ?: BodyVariant.Text
        return Choice(show, if (show == BodyVariant.Html) BodyVariant.Text else BodyVariant.Html)
    }
}
