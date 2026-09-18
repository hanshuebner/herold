package com.netzhansa.herold.shared.store

import android.content.Context
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File

/**
 * The blob cache's bytes in the app's cache directory (issue #420). The
 * directory is the system's to reclaim under storage pressure; a row
 * whose file is gone is dropped as it is read and the blob is downloaded
 * again, so losing the directory costs a download and nothing else.
 *
 * Attachment bytes a queued send still needs are not here - those live in
 * the outbox spool under `filesDir`, outside the cache budget
 * (`FileBlobSpool`, REQ-AND-SYNC-22).
 */
class FileBlobFileStore(
    context: Context,
    private val dispatcher: CoroutineDispatcher = Dispatchers.IO,
) : BlobFileStore {

    private val root: File = File(context.applicationContext.cacheDir, DIRECTORY)

    override suspend fun write(path: String, bytes: ByteArray): Boolean = withContext(dispatcher) {
        val file = resolve(path) ?: return@withContext false
        runCatching {
            file.parentFile?.mkdirs()
            // The bytes land under a staging name and are renamed into
            // place, so a kill in the middle of a write leaves no short
            // file for a later read to hand to the screen.
            val staging = File(file.parentFile, file.name + ".part")
            staging.writeBytes(bytes)
            staging.renameTo(file)
        }.getOrDefault(false)
    }

    override suspend fun read(path: String): ByteArray? = withContext(dispatcher) {
        val file = resolve(path) ?: return@withContext null
        if (!file.isFile) return@withContext null
        runCatching { file.readBytes() }.getOrNull()
    }

    override suspend fun delete(path: String) {
        withContext(dispatcher) { resolve(path)?.delete() }
    }

    override suspend fun deleteAll() {
        withContext(dispatcher) { root.deleteRecursively() }
    }

    /** The file [path] names, as long as it stays inside the cache root. */
    private fun resolve(path: String): File? {
        val file = File(root, path)
        val rootPath = runCatching { root.canonicalPath }.getOrNull() ?: return null
        val candidate = runCatching { file.canonicalPath }.getOrNull() ?: return null
        return if (candidate == rootPath || candidate.startsWith(rootPath + File.separator)) file else null
    }

    private companion object {
        const val DIRECTORY = "blobs"
    }
}
