package com.netzhansa.herold.android.media

/**
 * What a received attachment is called on disk and what type it is
 * handed to another app as (issue #500, REQ-AND-SYS-31/36).
 *
 * A sender picks both the filename and the content type, and neither can
 * be trusted: a name can carry path separators or nothing at all, and a
 * type is routinely `application/octet-stream` for a part whose extension
 * says exactly what it is. The decisions are taken on the two strings
 * alone, separate from [AttachmentFiles] which writes the bytes and
 * builds the intents, so a host-JVM check covers them.
 */
object AttachmentNames {

    /** The name a part with no usable one of its own is written under. */
    const val FALLBACK = "attachment"

    /** A type that says nothing about the bytes it labels. */
    private val OPAQUE_TYPES = setOf("", "application/octet-stream", "binary/octet-stream")

    /**
     * Extension to content type for the parts a mail client meets, so a
     * `report.pdf` sent as `application/octet-stream` still reaches a PDF
     * viewer. The platform's own MIME table covers more, and
     * [AttachmentFiles] consults it first; this map is what the decision
     * can be tested against.
     */
    private val TYPE_BY_EXTENSION = mapOf(
        "pdf" to "application/pdf",
        "png" to "image/png",
        "jpg" to "image/jpeg",
        "jpeg" to "image/jpeg",
        "gif" to "image/gif",
        "webp" to "image/webp",
        "heic" to "image/heic",
        "svg" to "image/svg+xml",
        "txt" to "text/plain",
        "csv" to "text/csv",
        "html" to "text/html",
        "ics" to "text/calendar",
        "vcf" to "text/vcard",
        "eml" to "message/rfc822",
        "zip" to "application/zip",
        "gz" to "application/gzip",
        "doc" to "application/msword",
        "docx" to "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        "xls" to "application/vnd.ms-excel",
        "xlsx" to "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        "ppt" to "application/vnd.ms-powerpoint",
        "pptx" to "application/vnd.openxmlformats-officedocument.presentationml.presentation",
        "odt" to "application/vnd.oasis.opendocument.text",
        "ods" to "application/vnd.oasis.opendocument.spreadsheet",
        "mp3" to "audio/mpeg",
        "m4a" to "audio/mp4",
        "ogg" to "audio/ogg",
        "wav" to "audio/wav",
        "mp4" to "video/mp4",
        "webm" to "video/webm",
    )

    /**
     * [name] reduced to a single filename: one path segment, of the
     * characters a file system and a content URI both carry, bounded in
     * length, never empty and never a relative traversal.
     */
    fun fileName(name: String): String {
        val segment = name.replace('\\', '/').substringAfterLast('/').trim()
        val cleaned = segment.map { char ->
            when {
                char.isLetterOrDigit() -> char
                char in " .-_()[]+@" -> char
                else -> '_'
            }
        }.joinToString("").trim(' ', '.')
        val bounded = if (cleaned.length > MAX_NAME) {
            val extension = cleaned.substringAfterLast('.', "")
            val stem = cleaned.removeSuffix(".$extension").take(MAX_NAME - extension.length - 1)
            if (extension.isEmpty()) cleaned.take(MAX_NAME) else "$stem.$extension"
        } else {
            cleaned
        }
        return bounded.ifBlank { FALLBACK }
    }

    /** The extension of [fileName], lower-cased, empty when it has none. */
    fun extension(fileName: String): String =
        fileName.substringAfterLast('.', "").lowercase().takeIf { it.isNotEmpty() && it.length <= 8 }.orEmpty()

    /**
     * The type a viewer app is offered the part as: the sender's own,
     * unless it says nothing, in which case the filename's extension
     * decides. [fromExtension] lets the caller consult the platform's
     * MIME table first; the built-in table answers what it does not.
     */
    fun openType(
        declaredType: String,
        fileName: String,
        fromExtension: (String) -> String? = { null },
    ): String {
        val declared = declaredType.substringBefore(';').trim().lowercase()
        if (declared !in OPAQUE_TYPES && declared.contains('/')) return declared
        val extension = extension(fileName)
        if (extension.isEmpty()) return OPAQUE_FALLBACK
        return fromExtension(extension)?.takeIf { it.isNotBlank() }
            ?: TYPE_BY_EXTENSION[extension]
            ?: OPAQUE_FALLBACK
    }

    /**
     * The name a Save is offered under, with the extension the [type]
     * implies where the sender left the filename without one, so the
     * document the user picks a folder for opens again afterwards.
     */
    fun saveName(name: String, type: String): String {
        val cleaned = fileName(name)
        if (extension(cleaned).isNotEmpty()) return cleaned
        val extension = TYPE_BY_EXTENSION.entries.firstOrNull {
            it.value == type.substringBefore(';').trim().lowercase()
        }?.key ?: return cleaned
        return "$cleaned.$extension"
    }

    /** What a part of unknown bytes is offered as. */
    private const val OPAQUE_FALLBACK = "application/octet-stream"

    /** Long enough for any real attachment, short of a filesystem's limit. */
    private const val MAX_NAME = 96
}
