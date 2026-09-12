package com.netzhansa.herold.shared.mail

import com.netzhansa.herold.shared.domain.Email
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The unsubscribe-header rules the suite's `list-headers.test.ts` pins,
 * asserted against the Kotlin mirror so both clients pick the same
 * mechanism for the same message (suite REQ-UNS-01..23).
 */
class ListHeadersTest {

    @Test
    fun everyBracketedUrlIsTaggedWithItsScheme() {
        val urls = ListHeaders.parseAngleBracketUrls(
            "<https://list.example/unsub?id=7>, <mailto:unsub@list.example?subject=go>",
        )
        assertEquals(2, urls.size)
        assertEquals(ListHeaders.Scheme.HTTPS, urls[0].scheme)
        assertEquals("https://list.example/unsub?id=7", urls[0].url)
        assertEquals(ListHeaders.Scheme.MAILTO, urls[1].scheme)
    }

    @Test
    fun unrecognisedTokensAreDropped() {
        assertEquals(emptyList(), ListHeaders.parseAngleBracketUrls("<NO>"))
        assertEquals(emptyList(), ListHeaders.parseAngleBracketUrls(null))
        assertEquals(emptyList(), ListHeaders.parseAngleBracketUrls("no brackets here"))
    }

    @Test
    fun oneClickMarkerIsMatchedWhateverTheCasing() {
        assertTrue(ListHeaders.hasOneClickPost("List-Unsubscribe=One-Click"))
        assertTrue(ListHeaders.hasOneClickPost("list-unsubscribe=one-click"))
        assertTrue(ListHeaders.hasOneClickPost("List-Unsubscribe = One-Click"))
        assertTrue(!ListHeaders.hasOneClickPost("List-Unsubscribe=Two-Click"))
        assertTrue(!ListHeaders.hasOneClickPost(null))
    }

    @Test
    fun oneClickWinsWhenTheMarkerAccompaniesAnHttpsUrl() {
        val mechanism = ListHeaders.chooseMechanism(
            "<mailto:unsub@list.example>, <https://list.example/unsub>",
            "List-Unsubscribe=One-Click",
        )
        assertEquals(ListHeaders.Mechanism.OneClick("https://list.example/unsub"), mechanism)
    }

    @Test
    fun httpsWithoutTheMarkerOpensRatherThanPosts() {
        val mechanism = ListHeaders.chooseMechanism("<https://list.example/unsub>", null)
        assertEquals(ListHeaders.Mechanism.Https("https://list.example/unsub"), mechanism)
    }

    @Test
    fun mailtoIsUsedWhenNoHttpsIsAdvertised() {
        val mechanism = ListHeaders.chooseMechanism("<mailto:unsub@list.example>", null)
        assertEquals(ListHeaders.Mechanism.Mailto("mailto:unsub@list.example"), mechanism)
    }

    @Test
    fun cleartextIsSurfacedAsHttpOnlyRatherThanOpened() {
        val mechanism = ListHeaders.chooseMechanism("<http://list.example/unsub>", "List-Unsubscribe=One-Click")
        assertEquals(ListHeaders.Mechanism.HttpOnly("http://list.example/unsub"), mechanism)
    }

    @Test
    fun anAbsentHeaderOffersNothing() {
        assertNull(ListHeaders.chooseMechanism(null, "List-Unsubscribe=One-Click"))
        assertNull(ListHeaders.chooseMechanism("", null))
    }

    @Test
    fun mailtoFieldsArePercentDecoded() {
        val fields = ListHeaders.parseMailto(
            "mailto:unsub%2Blist@list.example?subject=Unsubscribe%20me&body=please+stop",
        )
        assertEquals("unsub+list@list.example", fields.to)
        assertEquals("Unsubscribe me", fields.subject)
        assertEquals("please stop", fields.body)
    }

    @Test
    fun malformedEscapesAreCarriedThroughRatherThanThrowing() {
        val fields = ListHeaders.parseMailto("mailto:unsub@list.example?subject=100%")
        assertEquals("unsub@list.example", fields.to)
        assertEquals("100%", fields.subject)
    }

    @Test
    fun theOfferComesFromTheNewestMessageThatAdvertisesOne() {
        val offer = UnsubscribeOffer.of(
            listOf(
                email("e1", unsubscribe = "<https://list.example/a>"),
                email("e2", unsubscribe = "<https://list.example/b>"),
                email("e3", unsubscribe = null),
            ),
        )
        assertEquals("e2", offer?.email?.id)
        assertEquals(ListHeaders.Mechanism.Https("https://list.example/b"), offer?.mechanism)
    }

    @Test
    fun aThreadWithNoHeaderOffersNothing() {
        assertNull(UnsubscribeOffer.of(listOf(email("e1", unsubscribe = null))))
    }

    private fun email(id: String, unsubscribe: String?, post: String? = null) = Email(
        accountId = "acct-a",
        id = id,
        threadId = "t-1",
        fromEmail = "list@list.example",
        listUnsubscribe = unsubscribe,
        listUnsubscribePost = post,
    )
}
