package com.netzhansa.herold.shared.outbox

/**
 * Where the bytes of an attachment wait while its send is queued. A file
 * chosen through the system picker is readable only while the grant lasts,
 * so the compose copies it here the moment the user picks it and the
 * outbox entry refers to the copy (REQ-AND-SYNC-21).
 *
 * The Android actual writes into app-private storage; tests use
 * [InMemoryBlobSpool].
 */
interface BlobSpool {
    /** Copies [bytes] in and returns the handle the entry carries. */
    suspend fun put(bytes: ByteArray, name: String): String

    suspend fun read(handle: String): ByteArray?

    suspend fun remove(handle: String)
}

/** A spool that keeps everything in memory, for host-JVM tests. */
class InMemoryBlobSpool : BlobSpool {
    private val files = mutableMapOf<String, ByteArray>()
    private var counter = 0

    override suspend fun put(bytes: ByteArray, name: String): String {
        val handle = "spool-${++counter}-$name"
        files[handle] = bytes
        return handle
    }

    override suspend fun read(handle: String): ByteArray? = files[handle]

    override suspend fun remove(handle: String) {
        files.remove(handle)
    }
}
