package com.netzhansa.herold.shared.jmap

import kotlinx.serialization.json.JsonObject

/**
 * The server surface the sync engine and the action layer depend on. It is
 * an interface so the reconciliation logic is unit-testable against canned
 * JMAP responses with no network (docs/design/android/implementation-plan.md,
 * "Verification model").
 */
interface JmapApi {
    suspend fun session(): JmapSession

    suspend fun mailboxGet(accountId: String, ids: List<String>? = null): GetResult<WireMailbox>

    suspend fun mailboxChanges(accountId: String, sinceState: String): ChangesOutcome

    /** Inbox ids newest first (`Email/query` with a receivedAt sort). */
    suspend fun emailQueryInbox(accountId: String, mailboxId: String, limit: Int): List<String>

    suspend fun emailGet(
        accountId: String,
        ids: List<String>,
        withBody: Boolean = false,
    ): GetResult<WireEmail>

    suspend fun emailChanges(accountId: String, sinceState: String): ChangesOutcome

    suspend fun threadGet(accountId: String, ids: List<String>): GetResult<WireThread>

    suspend fun threadChanges(accountId: String, sinceState: String): ChangesOutcome

    suspend fun identityGet(accountId: String): GetResult<WireIdentity>

    suspend fun identityChanges(accountId: String, sinceState: String): ChangesOutcome

    /** `Email/set` with a patch per email id (star, read, archive, label, snooze). */
    suspend fun emailSet(accountId: String, update: Map<String, JsonObject>): SetOutcome

    /**
     * The account's category names from the `CategorySettings` singleton
     * (`https://netzhansa.com/jmap/categorise`), empty when the capability is
     * not advertised.
     */
    suspend fun derivedCategories(accountId: String): List<String>

    suspend fun downloadBlob(
        accountId: String,
        blobId: String,
        type: String,
        name: String,
    ): DownloadedBlob
}

/**
 * A JMAP request or method failure. [status] carries the HTTP status when the
 * failure was request-level; a `401` signs the user out (REQ-AND-AUTH-04 with
 * no refresh, since device tokens do not expire).
 */
class JmapException(
    message: String,
    val status: Int? = null,
    val methodError: String? = null,
) : Exception(message) {
    val isUnauthorized: Boolean get() = status == 401
}
