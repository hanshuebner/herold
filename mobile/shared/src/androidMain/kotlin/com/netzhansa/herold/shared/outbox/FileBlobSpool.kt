package com.netzhansa.herold.shared.outbox

import android.content.Context
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File

/**
 * Attachment bytes in app-private storage, where a queued send can still
 * read them (REQ-AND-SYNC-21). The system picker's grant on the chosen URI
 * is gone long before an entry queued offline drains, so the compose
 * copies the file here as it is picked and the entry refers to the copy.
 *
 * The directory lives under the app's `filesDir`, so it is private to the
 * app and removed with it, and it is outside the blob cache's budget:
 * nothing evicts a file a pending send still needs (REQ-AND-SYNC-22).
 */
class FileBlobSpool(
    context: Context,
    private val dispatcher: CoroutineDispatcher = Dispatchers.IO,
) : BlobSpool {

    private val dir: File = File(context.filesDir, DIRECTORY).apply { mkdirs() }

    override suspend fun put(bytes: ByteArray, name: String): String = withContext(dispatcher) {
        val file = File(dir, "${System.currentTimeMillis()}-${counter()}-${name.sanitised()}")
        file.writeBytes(bytes)
        file.name
    }

    override suspend fun read(handle: String): ByteArray? = withContext(dispatcher) {
        val file = File(dir, File(handle).name)
        if (file.isFile) file.readBytes() else null
    }

    override suspend fun remove(handle: String) {
        withContext(dispatcher) { File(dir, File(handle).name).delete() }
    }

    private fun counter(): Int = (++sequence)

    /** Keeps a picked file's name from escaping the spool directory. */
    private fun String.sanitised(): String =
        filter { it.isLetterOrDigit() || it == '.' || it == '-' || it == '_' }.takeLast(64).ifBlank { "file" }

    private var sequence = 0

    private companion object {
        const val DIRECTORY = "outbox"
    }
}
