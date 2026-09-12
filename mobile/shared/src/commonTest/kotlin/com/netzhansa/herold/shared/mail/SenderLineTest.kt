package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * The sender a push payload names, in both the shape herold sends now and
 * the raw-header shape an older server sends (issues #347, #348).
 */
class SenderLineTest {

    @Test
    fun theDecodedNameAndAddressTheServerSendsAreTakenAsTheyAre() {
        val sender = SenderLine.parse("Hans H\u00FCbner", "hans@example.local")

        assertEquals("Hans H\u00FCbner", sender.name)
        assertEquals("hans@example.local", sender.email)
    }

    @Test
    fun aRawHeaderIsSplitAndItsEncodedWordsDecoded() {
        val sender = SenderLine.parse("=?utf-8?B?SGFucyBIw7xibmVy?= <hans@example.local>")

        assertEquals("Hans H\u00FCbner", sender.name)
        assertEquals("hans@example.local", sender.email)
    }

    @Test
    fun aQuotedDisplayNameLosesItsQuotes() {
        val sender = SenderLine.parse("\"Example, Bob\" <bob@example.local>")

        assertEquals("Example, Bob", sender.name)
        assertEquals("bob@example.local", sender.email)
    }

    @Test
    fun aBareAddressIsTheAddressWithNoName() {
        val sender = SenderLine.parse("bob@example.local")

        assertNull(sender.name)
        assertEquals("bob@example.local", sender.email)
        assertEquals("bob@example.local", sender.display)
    }

    @Test
    fun theAddressFieldWinsOverTheOneInTheHeaderText() {
        // The server resolved the sender; the header text is only there
        // for the name.
        val sender = SenderLine.parse("Bob Example <old@example.local>", "bob@example.local")

        assertEquals("Bob Example", sender.name)
        assertEquals("bob@example.local", sender.email)
    }

    @Test
    fun anEmptySenderYieldsNothingToRender() {
        val sender = SenderLine.parse("")

        assertNull(sender.name)
        assertEquals("", sender.email)
    }

    @Test
    fun theFallbackAvatarIsStablePerAddressAndInitialledFromTheName() {
        assertEquals(
            SenderAvatar.colourFor("bob@example.local"),
            SenderAvatar.colourFor("Bob@Example.Local "),
        )
        assertEquals("BE", SenderAvatar.initialsFor("Bob Example", "bob@example.local"))
        assertEquals("B", SenderAvatar.initialsFor(null, "bob@example.local"))
        assertEquals("?", SenderAvatar.initialsFor(null, ""))
    }
}
