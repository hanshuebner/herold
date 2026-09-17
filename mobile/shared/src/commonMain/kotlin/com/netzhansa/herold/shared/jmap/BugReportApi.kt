package com.netzhansa.herold.shared.jmap

/**
 * One file of a bug report bundle as the request carries it: the drop
 * filename, its media type, and its bytes (`report.json`, `report.md`,
 * `logs.txt`, `screenshot-N.png`, `private.json`).
 */
data class BugReportPart(val name: String, val type: String, val bytes: ByteArray) {
    override fun equals(other: Any?): Boolean =
        other is BugReportPart && name == other.name && type == other.type && bytes.contentEquals(other.bytes)

    override fun hashCode(): Int = (name.hashCode() * 31 + type.hashCode()) * 31 + bytes.contentHashCode()
}

/**
 * The account server's bug-reports surface (`POST /api/v1/bug-reports`,
 * REQ-ADM-320, issue #416). The in-app reporter's bundle goes up as one
 * multipart request on the account's bearer token, and the server keeps
 * it for `herold bug-fetch` to collect.
 *
 * It is its own interface rather than part of [JmapApi] because it is a
 * REST endpoint of the same server rather than a JMAP method, and the
 * only thing that calls it is the outbox drain.
 */
interface BugReportApi {
    /**
     * Posts [parts] as one report and returns the id the server minted
     * for it. Throws [JmapException] carrying the status the server
     * answered with, so the drain classifies a refusal, a busy server
     * and a dead wire the way it does for a send.
     */
    suspend fun postBugReport(parts: List<BugReportPart>): String
}
