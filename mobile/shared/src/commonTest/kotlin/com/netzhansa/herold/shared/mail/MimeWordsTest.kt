package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * RFC 2047 decoding of header text (issue #348). The cases are the shapes
 * that reach a mail client in practice: both encodings, the charsets that
 * still turn up, split words, and text that must be left alone.
 */
class MimeWordsTest {

    @Test
    fun plainTextIsReturnedUntouched() {
        assertEquals("Lunch on Friday", MimeWords.decode("Lunch on Friday"))
        assertEquals("", MimeWords.decode(""))
    }

    @Test
    fun aBase64WordDecodesToItsUtf8Text() {
        assertEquals("Hans H\u00FCbner", MimeWords.decode("=?UTF-8?B?SGFucyBIw7xibmVy?="))
    }

    @Test
    fun aQuotedPrintableWordDecodesUnderscoresAsSpaces() {
        assertEquals("Hans H\u00FCbner", MimeWords.decode("=?utf-8?q?Hans_H=C3=BCbner?="))
    }

    @Test
    fun aWordKeepsTheTextAroundItAndTheAddressAfterIt() {
        assertEquals(
            "Re: Hans H\u00FCbner <hans@example.local>",
            MimeWords.decode("Re: =?utf-8?B?SGFucyBIw7xibmVy?= <hans@example.local>"),
        )
    }

    @Test
    fun adjacentWordsJoinWithoutTheWhitespaceBetweenThem() {
        // RFC 2047 section 6.2: the space separating two encoded words is
        // header layout, not text.
        assertEquals(
            "\u00DCber allen Gipfeln",
            MimeWords.decode("=?utf-8?q?=C3=9Cber_allen?= =?utf-8?q?_Gipfeln?="),
        )
    }

    @Test
    fun latin1AndWindows1252BytesDecodeToTheirCharacters() {
        assertEquals("G\u00E4rtner", MimeWords.decode("=?iso-8859-1?q?G=E4rtner?="))
        // 0x93/0x94 are curly quotes in Windows-1252 and control codes in
        // ISO-8859-1, which is why the charset is honoured.
        assertEquals("\u201Cquoted\u201D", MimeWords.decode("=?windows-1252?q?=93quoted=94?="))
    }

    @Test
    fun aLanguageTaggedCharsetIsAccepted() {
        assertEquals("Hello", MimeWords.decode("=?utf-8*en?q?Hello?="))
    }

    @Test
    fun anUndecodableWordIsLeftExactlyAsItArrived() {
        assertEquals("=?utf-8?x?Hello?=", MimeWords.decode("=?utf-8?x?Hello?="))
        assertEquals("=?utf-8?q?bro=ken?=", MimeWords.decode("=?utf-8?q?bro=ken?="))
        assertEquals("=?utf-8?b?!!!?=", MimeWords.decode("=?utf-8?b?!!!?="))
    }

    @Test
    fun anUnknownCharsetFallsBackToUtf8RatherThanDroppingTheText() {
        assertEquals("Hello", MimeWords.decode("=?x-unknown?b?SGVsbG8=?="))
    }
}
