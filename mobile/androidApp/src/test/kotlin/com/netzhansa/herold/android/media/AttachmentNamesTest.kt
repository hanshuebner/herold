package com.netzhansa.herold.android.media

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * What the reading pane calls a received part and hands it out as
 * (issue #500). The sender chooses both strings, so every case here is
 * one a message can actually carry.
 */
class AttachmentNamesTest {

    @Test
    fun anOrdinaryNameIsKept() {
        assertEquals("report.pdf", AttachmentNames.fileName("report.pdf"))
        assertEquals("Q3 report (final).pdf", AttachmentNames.fileName("Q3 report (final).pdf"))
    }

    @Test
    fun aNameCarryingAPathKeepsOnlyItsLastSegment() {
        assertEquals("secrets.txt", AttachmentNames.fileName("../../data/secrets.txt"))
        assertEquals("payload.bin", AttachmentNames.fileName("C:\\windows\\system32\\payload.bin"))
        assertTrue('/' !in AttachmentNames.fileName("a/b/c"))
    }

    @Test
    fun aNameThatReducesToNothingFallsBackToAConstant() {
        assertEquals(AttachmentNames.FALLBACK, AttachmentNames.fileName(""))
        assertEquals(AttachmentNames.FALLBACK, AttachmentNames.fileName("   "))
        assertEquals(AttachmentNames.FALLBACK, AttachmentNames.fileName(".."))
        assertEquals(AttachmentNames.FALLBACK, AttachmentNames.fileName("/"))
    }

    @Test
    fun aVeryLongNameIsBoundedAndKeepsItsExtension() {
        val bounded = AttachmentNames.fileName("x".repeat(400) + ".pdf")
        assertTrue(bounded.length <= 96, "saw ${bounded.length} characters")
        assertTrue(bounded.endsWith(".pdf"), "saw $bounded")
    }

    @Test
    fun theSendersTypeDecidesWhenItSaysSomething() {
        assertEquals("application/pdf", AttachmentNames.openType("application/pdf", "report.pdf"))
        assertEquals("image/png", AttachmentNames.openType("image/png", "shot.png"))
        // A charset parameter is not part of what an intent is typed with.
        assertEquals("text/plain", AttachmentNames.openType("text/plain; charset=utf-8", "note.txt"))
    }

    @Test
    fun anOpaqueTypeIsResolvedFromTheExtension() {
        assertEquals(
            "application/pdf",
            AttachmentNames.openType("application/octet-stream", "invoice.PDF"),
        )
        assertEquals("image/jpeg", AttachmentNames.openType("", "holiday.jpeg"))
        // The platform's own MIME table answers first where it knows.
        assertEquals(
            "application/vnd.platform",
            AttachmentNames.openType("application/octet-stream", "thing.xyz") { "application/vnd.platform" },
        )
    }

    @Test
    fun anUnknownExtensionStaysOpaque() {
        assertEquals(
            "application/octet-stream",
            AttachmentNames.openType("application/octet-stream", "blob.qqq"),
        )
        assertEquals("application/octet-stream", AttachmentNames.openType("", "noextension"))
    }

    @Test
    fun aSaveNameGainsTheExtensionItsTypeImplies() {
        assertEquals("report.pdf", AttachmentNames.saveName("report", "application/pdf"))
        assertEquals("report.pdf", AttachmentNames.saveName("report.pdf", "application/pdf"))
        // Nothing to add for a type with no extension of its own.
        assertEquals("thing", AttachmentNames.saveName("thing", "application/x-unknown"))
    }
}
