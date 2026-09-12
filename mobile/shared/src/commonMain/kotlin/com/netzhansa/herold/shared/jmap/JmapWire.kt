package com.netzhansa.herold.shared.jmap

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.jsonPrimitive

/**
 * Wire shapes of the JMAP objects the client reads (RFC 8620/8621 plus
 * herold's `snoozedUntil` property). The sync engine maps these onto the
 * domain models the local store holds; nothing above the sync engine sees
 * a wire type.
 */

@Serializable
data class JmapAccount(
    val name: String = "",
    val isPersonal: Boolean = true,
    val isReadOnly: Boolean = false,
    val accountCapabilities: Map<String, JsonElement> = emptyMap(),
)

@Serializable
data class JmapSession(
    val capabilities: Map<String, JsonElement> = emptyMap(),
    val accounts: Map<String, JmapAccount> = emptyMap(),
    val primaryAccounts: Map<String, String> = emptyMap(),
    val username: String = "",
    val apiUrl: String = "",
    val downloadUrl: String = "",
    val uploadUrl: String = "",
    val eventSourceUrl: String = "",
    val state: String = "",
) {
    val mailAccountId: String? get() = primaryAccounts[Capability.MAIL]

    fun hasCapability(uri: String): Boolean = capabilities.containsKey(uri)

    /**
     * The largest blob the server accepts in one upload
     * (`urn:ietf:params:jmap:core/maxSizeUpload`, suite REQ-ATT-04).
     */
    val maxSizeUpload: Long?
        get() = (capabilities[Capability.CORE] as? JsonObject)
            ?.get("maxSizeUpload")?.jsonPrimitive?.longOrNull

    /**
     * Every account carrying the mail capability, primary first, then
     * sub-accounts in descriptor order (suite REQ-MAIL-SUB-01/02).
     */
    fun mailAccountIds(): List<String> {
        val primary = mailAccountId
        val withMail = accounts.filterValues { it.accountCapabilities.containsKey(Capability.MAIL) }.keys
        val ordered = LinkedHashSet<String>()
        if (primary != null && withMail.contains(primary)) ordered.add(primary)
        ordered.addAll(withMail)
        if (ordered.isEmpty() && primary != null) ordered.add(primary)
        return ordered.toList()
    }
}

@Serializable
data class WireMailbox(
    val id: String,
    val name: String = "",
    val role: String? = null,
    val parentId: String? = null,
    val sortOrder: Long = 0,
    val totalEmails: Long = 0,
    val unreadEmails: Long = 0,
)

@Serializable
data class WireAddress(
    val name: String? = null,
    val email: String = "",
)

@Serializable
data class WireBodyPart(
    val partId: String? = null,
    val blobId: String? = null,
    val size: Long = 0,
    val type: String = "",
    val charset: String? = null,
    val disposition: String? = null,
    val name: String? = null,
    val cid: String? = null,
)

@Serializable
data class WireBodyValue(
    val value: String = "",
    val isEncodingProblem: Boolean = false,
    val isTruncated: Boolean = false,
)

@Serializable
data class WireEmail(
    val id: String,
    val blobId: String? = null,
    val threadId: String = "",
    val mailboxIds: Map<String, Boolean> = emptyMap(),
    val keywords: Map<String, Boolean> = emptyMap(),
    val from: List<WireAddress>? = null,
    val to: List<WireAddress>? = null,
    val cc: List<WireAddress>? = null,
    val replyTo: List<WireAddress>? = null,
    val messageId: List<String>? = null,
    val inReplyTo: List<String>? = null,
    val references: List<String>? = null,
    val sentAt: String? = null,
    /**
     * The delivery address herold injected at fan-out (server REQ-FLOW-34).
     * It is what tells a reply which of the principal's identities the
     * message reached, for Bcc and list mail that name none of them in
     * To/Cc (suite REQ-MAIL-12a step 3).
     */
    @SerialName("header:X-Herold-Recipient:asText") val deliveredTo: String? = null,
    val subject: String? = null,
    val receivedAt: String? = null,
    val size: Long = 0,
    val preview: String? = null,
    val hasAttachment: Boolean = false,
    val snoozedUntil: String? = null,
    val htmlBody: List<WireBodyPart>? = null,
    val textBody: List<WireBodyPart>? = null,
    val attachments: List<WireBodyPart>? = null,
    val bodyValues: Map<String, WireBodyValue>? = null,
)

@Serializable
data class WireThread(
    val id: String,
    val emailIds: List<String> = emptyList(),
)

@Serializable
data class WireIdentity(
    val id: String,
    val name: String = "",
    val email: String = "",
    val mayDelete: Boolean = false,
    /** The account's default sending address (suite REQ-MAIL-12). */
    val isDefault: Boolean = false,
)

/** Result of a `Foo/get` call. */
data class GetResult<T>(
    val state: String,
    val list: List<T>,
    val notFound: List<String> = emptyList(),
)

/**
 * Result of a `Foo/changes` call. `CannotCalculate` is the
 * `cannotCalculateChanges` method error, which makes the sync engine drop
 * that type's rows and refetch (REQ-AND-SYNC-10).
 */
sealed interface ChangesOutcome {
    data class Changed(
        val newState: String,
        val created: List<String>,
        val updated: List<String>,
        val destroyed: List<String>,
        val hasMoreChanges: Boolean,
    ) : ChangesOutcome

    data object CannotCalculate : ChangesOutcome
}

/** Result of an `Email/set` call: which ids the server accepted and why the rest failed. */
data class SetOutcome(
    val newState: String?,
    val updated: Set<String>,
    val notUpdated: Map<String, String>,
) {
    val isCompleteSuccess: Boolean get() = notUpdated.isEmpty()
}

/** A blob fetched from `/jmap/download/...`. */
data class DownloadedBlob(
    val contentType: String,
    val bytes: ByteArray,
) {
    override fun equals(other: Any?): Boolean =
        other is DownloadedBlob && contentType == other.contentType && bytes.contentEquals(other.bytes)

    override fun hashCode(): Int = 31 * contentType.hashCode() + bytes.contentHashCode()
}

/** A `StateChange` event off the EventSource connection (RFC 8620 section 7.1). */
@Serializable
data class StateChangeEvent(
    @SerialName("@type") val type: String = "StateChange",
    val changed: Map<String, Map<String, String>> = emptyMap(),
)

/** One entry of the principal's seen-address history (suite REQ-MAIL-11e). */
@Serializable
data class WireSeenAddress(
    val id: String,
    val email: String = "",
    val displayName: String = "",
    val lastUsedAt: String? = null,
    val sendCount: Long = 0,
    val receivedCount: Long = 0,
)

/** A `SearchSnippet` (RFC 8621 section 6.1); matches come back in `<mark>`. */
@Serializable
data class WireSnippet(
    val emailId: String,
    val subject: String? = null,
    val preview: String? = null,
)

/** The `POST /jmap/upload/<accountId>` response (RFC 8620 section 6.1). */
@Serializable
data class UploadedBlob(
    val accountId: String = "",
    val blobId: String,
    val type: String = "application/octet-stream",
    val size: Long = 0,
)

/** Result of an `Email/set` that creates or updates one message. */
data class EmailWriteOutcome(
    val id: String?,
    val newState: String?,
    val error: String?,
) {
    val isSuccess: Boolean get() = error == null
}

/** Result of an `EmailSubmission/set` create. */
data class SubmissionOutcome(
    val submissionId: String?,
    val emailId: String?,
    val error: String?,
) {
    val isSuccess: Boolean get() = error == null
}
