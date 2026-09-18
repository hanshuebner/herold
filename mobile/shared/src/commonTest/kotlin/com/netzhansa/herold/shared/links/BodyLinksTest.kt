package com.netzhansa.herold.shared.links

import com.netzhansa.herold.shared.mail.HtmlSanitizer
import kotlin.test.Test
import kotlin.test.assertEquals

class BodyLinksTest {

    @Test
    fun httpsGoesToTheBrowser() {
        assertEquals(
            BodyLinkAction.OpenExternally("https://vendor.example/offer?id=7"),
            BodyLinks.route("https://vendor.example/offer?id=7"),
        )
    }

    @Test
    fun cleartextHttpGoesToTheBrowserToo() {
        assertEquals(
            BodyLinkAction.OpenExternally("http://vendor.example/"),
            BodyLinks.route("http://vendor.example/"),
        )
    }

    @Test
    fun surroundingWhitespaceIsTrimmed() {
        assertEquals(
            BodyLinkAction.OpenExternally("https://vendor.example/"),
            BodyLinks.route("  https://vendor.example/\n"),
        )
    }

    @Test
    fun mailtoOpensTheComposerOnAddressAndSubject() {
        assertEquals(
            BodyLinkAction.Compose(
                ComposePrefill(to = listOf("sales@vendor.example"), subject = "Your offer"),
            ),
            BodyLinks.route("mailto:sales@vendor.example?subject=Your%20offer"),
        )
    }

    @Test
    fun bareMailtoOpensAnEmptyComposer() {
        assertEquals(
            BodyLinkAction.Compose(ComposePrefill()),
            BodyLinks.route("mailto:"),
        )
    }

    @Test
    fun telGoesToTheSystem() {
        assertEquals(BodyLinkAction.HandOff("tel:+49301234567"), BodyLinks.route("tel:+49301234567"))
    }

    @Test
    fun smsGoesToTheSystem() {
        assertEquals(BodyLinkAction.HandOff("sms:+49301234567"), BodyLinks.route("sms:+49301234567"))
    }

    @Test
    fun scriptAndContentSchemesAreIgnored() {
        listOf(
            "javascript:alert(1)",
            "data:text/html,<b>hi</b>",
            "file:///sdcard/secret.txt",
            "content://com.netzhansa.herold.android.files/token",
            "intent://scan/#Intent;scheme=zxing;end",
            "about:blank",
        ).forEach { url ->
            assertEquals(BodyLinkAction.Ignore, BodyLinks.route(url), url)
        }
    }

    @Test
    fun anchorsAndEmptyTargetsAreIgnored() {
        assertEquals(BodyLinkAction.Ignore, BodyLinks.route("#section-2"))
        assertEquals(BodyLinkAction.Ignore, BodyLinks.route(""))
        assertEquals(BodyLinkAction.Ignore, BodyLinks.route("/relative/path"))
    }

    @Test
    fun theInlineImageSchemeIsIgnored() {
        assertEquals(
            BodyLinkAction.Ignore,
            BodyLinks.route("${HtmlSanitizer.INLINE_SCHEME}cid-42"),
        )
    }
}
