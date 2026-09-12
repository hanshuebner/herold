package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.links.ComposePrefill

/**
 * Folds what a share, a `mailto:` link or a deep link handed over into an
 * open compose (REQ-AND-SYS-01/03/10). The shared text becomes the body
 * above whatever the compose already held, so a forward's quote stays
 * below it, and the addresses go through the composer's own recipient
 * grammar rather than a second parser.
 */
fun ComposeState.withPrefill(prefill: ComposePrefill): ComposeState {
    if (prefill.isEmpty) return this
    val addedCc = prefill.cc.flatMap { RecipientParser.parse(it) }
    val addedBcc = prefill.bcc.flatMap { RecipientParser.parse(it) }
    return copy(
        to = to + prefill.to.flatMap { RecipientParser.parse(it) },
        cc = cc + addedCc,
        bcc = bcc + addedBcc,
        subject = if (prefill.subject.isBlank()) subject else prefill.subject,
        bodyHtml = if (prefill.body.isBlank()) bodyHtml else HtmlText.toHtml(prefill.body) + bodyHtml,
        showCc = showCc || cc.isNotEmpty() || addedCc.isNotEmpty(),
    )
}
