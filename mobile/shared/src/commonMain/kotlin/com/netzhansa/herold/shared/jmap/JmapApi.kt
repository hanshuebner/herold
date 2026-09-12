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

    /**
     * `Email/query` with an arbitrary filter (search, REQ-SRC-02/06). Ids
     * come back newest first; [collapseThreads] returns one message per
     * conversation so results render as threads (REQ-SRC-03).
     */
    suspend fun emailQuery(
        accountId: String,
        filter: JsonObject,
        limit: Int,
        collapseThreads: Boolean = true,
    ): List<String>

    /**
     * `SearchSnippet/get` (RFC 8621 section 6.1) for the visible result
     * rows, passing the active filter so the server highlights the matched
     * terms in `<mark>` (REQ-SRC-30).
     */
    suspend fun searchSnippets(
        accountId: String,
        filter: JsonObject,
        emailIds: List<String>,
    ): List<WireSnippet>

    /** `SeenAddress/get`: the address history the recipient fields complete from. */
    suspend fun seenAddresses(accountId: String): List<WireSeenAddress>

    /** `POST` to the session's `uploadUrl` (RFC 8620 section 6.1). */
    suspend fun uploadBlob(
        accountId: String,
        bytes: ByteArray,
        type: String,
        filename: String? = null,
    ): UploadedBlob

    /** `Email/set { create }` for a draft; returns the id the server assigned. */
    suspend fun emailCreate(accountId: String, email: JsonObject): EmailWriteOutcome

    /** `Email/set { update }` for one message with a full property set (draft autosave). */
    suspend fun emailReplace(accountId: String, id: String, email: JsonObject): EmailWriteOutcome

    suspend fun emailDestroy(accountId: String, ids: List<String>)

    /**
     * The send batch: `Email/set` writing the draft, then
     * `EmailSubmission/set { create }` referencing it with
     * `onSuccessUpdateEmail` moving the message out of Drafts into Sent and
     * clearing `$draft` (suite `19-drafts.md`, RFC 8621 section 7.5).
     *
     * @param draftId the id of an already-saved draft, or null to create one
     *   in the same batch
     * @param parentKeyword `$answered` / `$forwarded` to set on [parentId]
     */
    suspend fun sendEmail(
        accountId: String,
        email: JsonObject,
        draftId: String?,
        identityId: String,
        envelope: Envelope,
        onSuccessUpdate: JsonObject,
        parentId: String? = null,
        parentKeyword: String? = null,
    ): SubmissionOutcome

    /** `Email/set` with a patch per email id (star, read, archive, label, snooze). */
    suspend fun emailSet(accountId: String, update: Map<String, JsonObject>): SetOutcome

    /**
     * The account's category names from the `CategorySettings` singleton
     * (`https://netzhansa.com/jmap/categorise`), empty when the capability is
     * not advertised.
     */
    suspend fun derivedCategories(accountId: String): List<String>

    /**
     * `PushSubscription/set` (RFC 8620 section 7.2 with herold's `kind`
     * extension). One call carries the create, the patches and the
     * destroys, so a token rotation registers the new subscription and
     * drops the stale one in a single round trip (REQ-AND-PUSH-02).
     */
    suspend fun pushSubscriptionSet(
        create: FcmSubscriptionCreate? = null,
        update: Map<String, JsonObject> = emptyMap(),
        destroy: List<String> = emptyList(),
    ): PushSetOutcome

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

/** The SMTP envelope an `EmailSubmission` carries (RFC 8621 section 7). */
data class Envelope(
    val mailFrom: String,
    val rcptTo: List<String>,
)
