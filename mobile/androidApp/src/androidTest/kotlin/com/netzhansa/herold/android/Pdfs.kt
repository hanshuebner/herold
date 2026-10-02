package com.netzhansa.herold.android

/**
 * A small but structurally real PDF, so a viewer app could open what the
 * attachment checks hand it (issues #500, #503).
 */
fun acceptancePdf(): ByteArray {
    val objects = listOf(
        "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
        "2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
        "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
            "/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>\nendobj\n",
        "4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
        "5 0 obj\n<< /Length 62 >>\nstream\nBT /F1 18 Tf 20 100 Td " +
            "(herold acceptance) Tj ET\nendstream\nendobj\n",
    )
    val header = "%PDF-1.4\n"
    val offsets = mutableListOf<Int>()
    val body = buildString {
        append(header)
        objects.forEach {
            offsets += length
            append(it)
        }
    }
    val xrefAt = body.length
    val xref = buildString {
        append("xref\n0 ${objects.size + 1}\n0000000000 65535 f \n")
        offsets.forEach { append(it.toString().padStart(10, '0') + " 00000 n \n") }
        append("trailer\n<< /Size ${objects.size + 1} /Root 1 0 R >>\nstartxref\n$xrefAt\n%%EOF\n")
    }
    return (body + xref).encodeToByteArray()
}
