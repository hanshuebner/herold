package com.netzhansa.herold.android.media

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.webkit.MimeTypeMap
import androidx.activity.result.contract.ActivityResultContract
import androidx.core.content.FileProvider
import java.io.File

/**
 * How a received attachment leaves the app (issue #500, REQ-AND-SYS-31/36).
 *
 * Two destinations, both taking the bytes the bearer-authenticated blob
 * download already produced: another app, through a `FileProvider` URI on
 * an `ACTION_VIEW` intent, and a location the user picks, through the
 * Storage Access Framework's `ACTION_CREATE_DOCUMENT`. The bytes are
 * written to the app's own cache, which the provider is the only way into,
 * so the file is readable by the app the user opened it with and by
 * nothing else on the device.
 */
object AttachmentFiles {

    /** The cache subtree `file_paths.xml` exposes through the provider. */
    const val CACHE_DIR = "attachments"

    /** The provider's authority for this build's package (`.debug` included). */
    fun authority(context: Context): String = "${context.packageName}.fileprovider"

    /**
     * [bytes] written under [name] in the provider's cache subtree, as the
     * content URI another app reads them through. [key] separates one
     * part's directory from another's, so two attachments of the same
     * name in one thread do not overwrite each other.
     */
    fun cache(context: Context, key: String, name: String, bytes: ByteArray): Uri {
        val root = File(context.cacheDir, CACHE_DIR)
        prune(root)
        val directory = File(root, digest(key)).apply { mkdirs() }
        val file = File(directory, AttachmentNames.fileName(name))
        file.writeBytes(bytes)
        return FileProvider.getUriForFile(context, authority(context), file)
    }

    /**
     * The type [uri] is offered to another app as: the sender's, or the
     * filename's extension resolved through the platform's MIME table
     * when the sender's says nothing.
     */
    fun openType(declaredType: String, name: String): String =
        AttachmentNames.openType(declaredType, name) { extension ->
            MimeTypeMap.getSingleton().getMimeTypeFromExtension(extension)
        }

    /**
     * The intent that hands [uri] to whatever app handles [type]. The
     * read grant travels with the intent, so the provider needs no
     * permission of its own and the grant dies with the task.
     */
    fun viewIntent(uri: Uri, type: String): Intent =
        Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, type)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)

    /**
     * Opens [uri] in another app. False when the device has nothing that
     * handles [type], which the caller says out loud rather than
     * swallowing.
     */
    fun open(context: Context, uri: Uri, type: String): Boolean = try {
        context.startActivity(viewIntent(uri, type))
        true
    } catch (missing: ActivityNotFoundException) {
        false
    }

    /** The SAF intent that asks the user where [name] is written. */
    fun createDocumentIntent(name: String, type: String): Intent =
        Intent(Intent.ACTION_CREATE_DOCUMENT)
            .addCategory(Intent.CATEGORY_OPENABLE)
            .setType(type)
            .putExtra(Intent.EXTRA_TITLE, name)

    /** Writes [bytes] to the document the user picked. */
    fun write(context: Context, target: Uri, bytes: ByteArray): Boolean =
        runCatching {
            context.contentResolver.openOutputStream(target, "wt")?.use { it.write(bytes) } ?: return false
            true
        }.getOrDefault(false)

    /**
     * Keeps the cache subtree from growing without bound: a part opened
     * a day ago is gone, and the app's own cache directory is clearable
     * by the system in any case.
     */
    private fun prune(root: File) {
        val cutoff = System.currentTimeMillis() - CACHE_TTL_MS
        root.listFiles()?.forEach { entry ->
            if (entry.lastModified() < cutoff) entry.deleteRecursively()
        }
    }

    /** A directory name derived from the blob id, free of its characters. */
    private fun digest(key: String): String {
        var hash = 0xcbf29ce484222325uL
        key.encodeToByteArray().forEach { byte ->
            hash = hash xor (byte.toInt() and 0xFF).toULong()
            hash *= 0x100000001b3uL
        }
        return hash.toString(16)
    }

    private const val CACHE_TTL_MS = 24 * 60 * 60 * 1000L
}

/** What a Save asks the Storage Access Framework for. */
data class SaveRequest(val name: String, val type: String)

/**
 * `ACTION_CREATE_DOCUMENT` with the name and type decided per attachment.
 * The platform's own `CreateDocument` contract fixes the type when the
 * launcher is remembered, which one reading pane serving parts of every
 * type cannot do.
 */
class CreateAttachmentDocument : ActivityResultContract<SaveRequest, Uri?>() {
    override fun createIntent(context: Context, input: SaveRequest): Intent =
        AttachmentFiles.createDocumentIntent(input.name, input.type)

    override fun parseResult(resultCode: Int, intent: Intent?): Uri? =
        if (resultCode == Activity.RESULT_OK) intent?.data else null
}
