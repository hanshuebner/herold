package com.netzhansa.herold.shared.jmap

import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * What an attachment is fetched from (issue #500). Every byte the reading
 * pane hands to a viewer app comes through this URL, so the encoding of a
 * filename a sender chose is checked here rather than on a device.
 */
class BlobUrlsTest {

    @Test
    fun theSessionTemplateIsUsedWhenTheServerPublishesOne() {
        assertEquals(
            "https://mail.example/jmap/download/a1/G1/text%2Fplain/note.txt",
            BlobUrls.download(
                template = BlobUrls.downloadTemplate(
                    "https://mail.example/jmap/download/{accountId}/{blobId}/{type}/{name}",
                    "https://other.example",
                ),
                accountId = "a1",
                blobId = "G1",
                type = "text/plain",
                name = "note.txt",
            ),
        )
    }

    @Test
    fun aServerThatPublishesNoTemplateIsAddressedAtTheDefaultPath() {
        assertEquals(
            "https://mail.example/jmap/download/a1/G1/application%2Fpdf/report.pdf",
            BlobUrls.download(
                template = BlobUrls.downloadTemplate("", "https://mail.example/"),
                accountId = "a1",
                blobId = "G1",
                type = "application/pdf",
                name = "report.pdf",
            ),
        )
    }

    @Test
    fun aFilenameWithSpacesAndSlashesStaysInOneSegment() {
        val url = BlobUrls.download(
            template = BlobUrls.downloadTemplate("", "https://mail.example"),
            accountId = "a1",
            blobId = "Gb-1/2",
            type = "application/pdf",
            name = "Q3 report (final)/v2.pdf",
        )
        assertEquals(
            "https://mail.example/jmap/download/a1/Gb-1%2F2/application%2Fpdf/" +
                "Q3%20report%20%28final%29%2Fv2.pdf",
            url,
        )
        // Beyond the template's own four separators, the path carries no
        // segment the sender's filename invented.
        assertEquals(6, url.removePrefix("https://mail.example").count { it == '/' })
    }

    @Test
    fun aNonAsciiFilenameGoesOutAsItsUtf8Bytes() {
        assertEquals(
            "https://mail.example/jmap/download/a1/G1/application%2Fpdf/Geb%C3%BChren.pdf",
            BlobUrls.download(
                template = BlobUrls.downloadTemplate("", "https://mail.example"),
                accountId = "a1",
                blobId = "G1",
                type = "application/pdf",
                name = "Gebühren.pdf",
            ),
        )
    }

    @Test
    fun theUploadTemplateTakesTheSamePath() {
        assertEquals(
            "https://mail.example/jmap/upload/a%201",
            BlobUrls.upload(BlobUrls.uploadTemplate("", "https://mail.example"), "a 1"),
        )
    }
}
