package com.netzhansa.herold.shared.store

/**
 * Where the blob cache's bytes are held (issue #420). Android hands a
 * database row to the client through a 2 MiB cursor window, so the bytes
 * of a cached blob live in a file and the `blob_cache` row keeps the
 * metadata and that file's relative path.
 *
 * Paths are relative to the implementation's own directory and are what
 * the row stores; the store derives them from the account and the blob
 * id (`SqlDelightLocalStore.blobPath`).
 */
interface BlobFileStore {
    /**
     * Writes [bytes] under [path], creating what the path needs. Returns
     * false when the write did not complete, which leaves the cache
     * without a row for the blob and the next read downloading it again.
     */
    suspend fun write(path: String, bytes: ByteArray): Boolean

    /** The bytes held under [path], or null when the file is gone. */
    suspend fun read(path: String): ByteArray?

    /** Removes the file under [path]; a missing file is not an error. */
    suspend fun delete(path: String)

    /** Removes everything held, for a sign-out or a store reset. */
    suspend fun deleteAll()
}

/**
 * Bytes held in memory, for a store built without platform storage (a
 * host-JVM test). The app uses the file-backed implementation.
 */
class InMemoryBlobFileStore : BlobFileStore {
    private val files = mutableMapOf<String, ByteArray>()

    override suspend fun write(path: String, bytes: ByteArray): Boolean {
        files[path] = bytes
        return true
    }

    override suspend fun read(path: String): ByteArray? = files[path]

    override suspend fun delete(path: String) {
        files.remove(path)
    }

    override suspend fun deleteAll() {
        files.clear()
    }
}
