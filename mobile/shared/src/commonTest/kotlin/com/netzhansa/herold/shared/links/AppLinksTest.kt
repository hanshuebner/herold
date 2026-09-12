package com.netzhansa.herold.shared.links

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class AppLinksTest {

    @Test
    fun internalThreadLinkCarriesAccountAndThread() {
        assertEquals(
            AppDestination.Thread("acct-1", "T42"),
            AppLinks.parse(AppLinks.threadUri("acct-1", "T42")),
        )
    }

    @Test
    fun internalReplyLinkNamesTheMessage() {
        assertEquals(
            AppDestination.Reply("acct-1", "M7"),
            AppLinks.parse(AppLinks.replyUri("acct-1", "M7")),
        )
    }

    @Test
    fun composeLinkCarriesItsPrefill() {
        val destination = AppLinks.parse("herold://compose?to=bob%40example.local&subject=Hi%20there")
        assertEquals(
            AppDestination.Compose(
                ComposePrefill(to = listOf("bob@example.local"), subject = "Hi there"),
            ),
            destination,
        )
    }

    @Test
    fun bareComposeLinkOpensAnEmptyCompose() {
        val destination = AppLinks.parse(AppLinks.composeUri())
        assertTrue(destination is AppDestination.Compose && destination.prefill.isEmpty)
    }

    @Test
    fun settingsLinkKeepsItsSection() {
        assertEquals(AppDestination.Settings("notifications"), AppLinks.parse("herold://settings/notifications"))
        assertEquals(AppDestination.Settings(null), AppLinks.parse(AppLinks.settingsUri()))
    }

    @Test
    fun suiteThreadUrlResolvesToTheThread() {
        val url = AppLinks.suiteThreadUrl("https://mail.netzhansa.com", "T9")
        assertEquals("https://mail.netzhansa.com/#/mail/thread/T9", url)
        assertEquals(AppDestination.Thread(null, "T9"), AppLinks.parse(url))
    }

    @Test
    fun otherSuiteUrlsLandOnTheMessageList() {
        assertEquals(AppDestination.Inbox, AppLinks.parse("https://mail.netzhansa.com/#/mail/inbox"))
        assertEquals(AppDestination.Inbox, AppLinks.parse("https://mail.netzhansa.com/"))
        assertEquals(AppDestination.Settings("appearance"), AppLinks.parse("https://mail.netzhansa.com/#/settings/appearance"))
    }

    @Test
    fun unknownSchemeIsNotRouted() {
        assertNull(AppLinks.parse("content://media/external/images/1"))
        assertNull(AppLinks.parse("herold://nonsense"))
    }

    @Test
    fun mailtoPrefillsRecipientSubjectAndBody() {
        val prefill = Mailto.parse("mailto:alice@example.local?subject=Lunch&body=At%20noon%3F&cc=bob@example.local")
        assertEquals(listOf("alice@example.local"), prefill?.to)
        assertEquals(listOf("bob@example.local"), prefill?.cc)
        assertEquals("Lunch", prefill?.subject)
        assertEquals("At noon?", prefill?.body)
    }

    @Test
    fun mailtoTakesSeveralAddressesAndDoesNotEatAPlus() {
        val prefill = Mailto.parse("mailto:a%2Btag@example.local,b@example.local?to=c@example.local")
        assertEquals(listOf("a+tag@example.local", "b@example.local", "c@example.local"), prefill?.to)
    }

    @Test
    fun mailtoWithNoAddressStillOpensCompose() {
        val destination = AppLinks.parse("mailto:?subject=Hello")
        assertEquals(AppDestination.Compose(ComposePrefill(subject = "Hello")), destination)
    }
}
