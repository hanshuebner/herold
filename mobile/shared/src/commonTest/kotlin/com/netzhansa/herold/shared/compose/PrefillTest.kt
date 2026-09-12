package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.links.ComposePrefill
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class PrefillTest {

    private fun blank() = ComposeState(mode = ComposeMode.NEW, accountId = "a1", identity = null)

    @Test
    fun sharedTextBecomesTheBodyAndSubject() {
        val state = blank().withPrefill(
            ComposePrefill(subject = "A photo", body = "Look at this"),
        )
        assertEquals("A photo", state.subject)
        assertTrue(state.bodyHtml.contains("Look at this"))
    }

    @Test
    fun mailtoAddressesBecomeRecipients() {
        val state = blank().withPrefill(
            ComposePrefill(to = listOf("alice@example.local"), cc = listOf("Bob <bob@example.local>")),
        )
        assertEquals(listOf("alice@example.local"), state.to.map { it.email })
        assertEquals(listOf("bob@example.local"), state.cc.map { it.email })
        assertTrue(state.showCc)
    }

    @Test
    fun sharedTextKeepsAnExistingQuoteBelowIt() {
        val quoted = blank().copy(bodyHtml = "<blockquote>old</blockquote>")
        val state = quoted.withPrefill(ComposePrefill(body = "new"))
        assertTrue(state.bodyHtml.indexOf("new") < state.bodyHtml.indexOf("old"))
    }

    @Test
    fun anEmptyPrefillChangesNothing() {
        val state = blank().copy(subject = "kept")
        assertEquals(state, state.withPrefill(ComposePrefill()))
    }
}
