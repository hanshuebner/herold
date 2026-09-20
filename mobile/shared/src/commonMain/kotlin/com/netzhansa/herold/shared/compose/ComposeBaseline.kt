package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.MailAddress

/**
 * The composer as it was seeded, which is what a close compares the
 * composer against to decide whether there is a draft to keep
 * (issue #371).
 *
 * A reply opens carrying the address it answers, a `Re:` subject and
 * the quoted original; a forward opens carrying the quote and the
 * parent's files; a reopened draft opens carrying everything that was
 * written into it before. Absolute emptiness is therefore false of all
 * three from the first frame, and closing on that saves a draft for a
 * conversation the reader only looked at. What decides is whether the
 * reader changed anything since the composer opened.
 *
 * The body is held as its text with runs of whitespace collapsed: the
 * editor republishes the document it was seeded with in its own markup
 * as it loads, and the same words in different markup are the same
 * reply.
 */
data class ComposeBaseline(
    val to: List<String> = emptyList(),
    val cc: List<String> = emptyList(),
    val bcc: List<String> = emptyList(),
    val subject: String = "",
    val attachments: List<String> = emptyList(),
    val bodyText: String = "",
) {

    /**
     * The same baseline with [bodyHtml] as the body it was seeded with,
     * for the editor's first publish: the document coming back in the
     * editor's own markup is what the composer opened on, not an edit.
     */
    fun withBody(bodyHtml: String): ComposeBaseline = copy(bodyText = textOf(bodyHtml))

    companion object {

        /**
         * A composer seeded with nothing, so everything in it is the
         * reader's: a new message, and a compose handed back from a
         * send taken back inside its undo window.
         */
        val EMPTY = ComposeBaseline()

        /** What [state] holds, as the comparison reads it. */
        fun of(state: ComposeState): ComposeBaseline = ComposeBaseline(
            to = emails(state.to),
            cc = emails(state.cc),
            bcc = emails(state.bcc),
            subject = state.subject.trim(),
            attachments = state.attachments.map { it.key },
            bodyText = textOf(state.bodyHtml),
        )

        private fun emails(addresses: List<MailAddress>): List<String> =
            addresses.map { it.email.trim().lowercase() }.filter { it.isNotEmpty() }

        private fun textOf(bodyHtml: String): String =
            HtmlText.toPlainText(bodyHtml)
                // A no-break space reads as a space, and an editor
                // writes one where the markup it was handed had none.
                .replace('\u00A0', ' ')
                .split(WHITESPACE)
                .filter { it.isNotEmpty() }
                .joinToString(" ")

        private val WHITESPACE = Regex("\\s+")
    }
}

/**
 * True when the reader changed something since the composer was seeded,
 * which is what makes the compose worth keeping as a draft (issue #371).
 */
fun ComposeState.changedSince(baseline: ComposeBaseline): Boolean =
    ComposeBaseline.of(this) != baseline
