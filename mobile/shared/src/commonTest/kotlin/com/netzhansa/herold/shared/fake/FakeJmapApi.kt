package com.netzhansa.herold.shared.fake

import com.netzhansa.herold.shared.jmap.ChangesOutcome
import com.netzhansa.herold.shared.jmap.EmailWriteOutcome
import com.netzhansa.herold.shared.jmap.Envelope
import com.netzhansa.herold.shared.jmap.DownloadedBlob
import com.netzhansa.herold.shared.jmap.PushSubscriptionCreate
import com.netzhansa.herold.shared.jmap.GetResult
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.jmap.JmapSession
import com.netzhansa.herold.shared.jmap.PushSetOutcome
import com.netzhansa.herold.shared.jmap.SetOutcome
import com.netzhansa.herold.shared.jmap.SubmissionOutcome
import com.netzhansa.herold.shared.jmap.UploadedBlob
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.jmap.WireIdentity
import com.netzhansa.herold.shared.jmap.RuleSetOutcome
import com.netzhansa.herold.shared.jmap.WireLlmInspect
import com.netzhansa.herold.shared.jmap.WireLlmTransparency
import com.netzhansa.herold.shared.jmap.WireMailbox
import com.netzhansa.herold.shared.jmap.WireManagedRule
import com.netzhansa.herold.shared.jmap.WireSeenAddress
import com.netzhansa.herold.shared.jmap.WireSnippet
import com.netzhansa.herold.shared.jmap.WireThread
import kotlinx.serialization.json.JsonObject

/**
 * A scripted [JmapApi]: every response is canned, every call is recorded.
 * The sync engine and the action layer are exercised through it with no
 * network, so the reconciliation rules are deterministic tests.
 */
class FakeJmapApi(
    var session: JmapSession = JmapSession(),
) : JmapApi {

    var mailboxes: List<WireMailbox> = emptyList()
    var mailboxState: String = "mailbox-1"
    var mailboxChanges: ChangesOutcome = ChangesOutcome.Changed("mailbox-1", emptyList(), emptyList(), emptyList(), false)

    var inboxIds: List<String> = emptyList()
    var emails: Map<String, WireEmail> = emptyMap()
    var emailState: String = "email-1"
    var emailChanges: ChangesOutcome = ChangesOutcome.Changed("email-1", emptyList(), emptyList(), emptyList(), false)

    var threads: List<WireThread> = emptyList()
    var threadState: String = "thread-1"
    var threadChanges: ChangesOutcome = ChangesOutcome.Changed("thread-1", emptyList(), emptyList(), emptyList(), false)

    var identities: List<WireIdentity> = emptyList()
    var identityState: String = "identity-1"
    var identityChanges: ChangesOutcome = ChangesOutcome.Changed("identity-1", emptyList(), emptyList(), emptyList(), false)

    var categories: List<String> = emptyList()

    var rules: List<WireManagedRule> = emptyList()
    var ruleState: String = "rule-1"
    var ruleChanges: ChangesOutcome = ChangesOutcome.Changed("rule-1", emptyList(), emptyList(), emptyList(), false)

    /** What a `ManagedRule/set` answers; refusals go in its `errors`. */
    var ruleSetOutcome: RuleSetOutcome = RuleSetOutcome()

    var transparency: WireLlmTransparency? = null
    var inspect: List<WireLlmInspect> = emptyList()

    val ruleSetCalls = mutableListOf<Triple<Map<String, JsonObject>, Map<String, JsonObject>, List<String>>>()
    val threadMuteCalls = mutableListOf<Pair<String, Boolean>>()
    val blockedSenderCalls = mutableListOf<String>()
    var ruleGetCalls = 0

    /** When set, every `Email/set` throws it - the offline and rejection paths. */
    var setFailure: Throwable? = null

    /** When set, every read throws it - the failed-pass path. */
    var readFailure: Throwable? = null

    /** Ids the server refuses to update, with the reason the UI shows. */
    var setRejections: Map<String, String> = emptyMap()

    val emailGetCalls = mutableListOf<List<String>>()
    val emailSetCalls = mutableListOf<Map<String, JsonObject>>()
    var mailboxGetCalls = 0
    var inboxQueryCalls = 0

    override suspend fun session(): JmapSession = session

    override suspend fun managedRuleGet(accountId: String, ids: List<String>?): GetResult<WireManagedRule> {
        readFailure?.let { throw it }
        ruleGetCalls++
        val list = if (ids == null) rules else rules.filter { it.id in ids }
        return GetResult(ruleState, list, notFound = ids.orEmpty().filter { id -> rules.none { it.id == id } })
    }

    override suspend fun managedRuleChanges(accountId: String, sinceState: String): ChangesOutcome = ruleChanges

    override suspend fun managedRuleSet(
        accountId: String,
        create: Map<String, JsonObject>,
        update: Map<String, JsonObject>,
        destroy: List<String>,
    ): RuleSetOutcome {
        setFailure?.let { throw it }
        ruleSetCalls.add(Triple(create, update, destroy))
        return ruleSetOutcome
    }

    override suspend fun threadMute(accountId: String, threadId: String, muted: Boolean) {
        setFailure?.let { throw it }
        threadMuteCalls.add(threadId to muted)
    }

    override suspend fun blockedSenderSet(accountId: String, address: String) {
        setFailure?.let { throw it }
        blockedSenderCalls.add(address)
    }

    override suspend fun llmTransparency(accountId: String): WireLlmTransparency? = transparency

    override suspend fun llmInspect(accountId: String, ids: List<String>): List<WireLlmInspect> =
        inspect.filter { it.id in ids }

    override suspend fun mailboxGet(accountId: String, ids: List<String>?): GetResult<WireMailbox> {
        readFailure?.let { throw it }
        mailboxGetCalls++
        val list = if (ids == null) mailboxes else mailboxes.filter { it.id in ids }
        return GetResult(mailboxState, list)
    }

    override suspend fun mailboxChanges(accountId: String, sinceState: String): ChangesOutcome = mailboxChanges

    override suspend fun emailQueryInbox(accountId: String, mailboxId: String, limit: Int): List<String> {
        inboxQueryCalls++
        return inboxIds
    }

    override suspend fun emailGet(accountId: String, ids: List<String>, withBody: Boolean): GetResult<WireEmail> {
        emailGetCalls.add(ids)
        val list = ids.mapNotNull { emails[it] }
        return GetResult(emailState, list, notFound = ids.filter { it !in emails })
    }

    override suspend fun emailChanges(accountId: String, sinceState: String): ChangesOutcome = emailChanges

    override suspend fun threadGet(accountId: String, ids: List<String>): GetResult<WireThread> =
        GetResult(threadState, threads.filter { it.id in ids })

    override suspend fun threadChanges(accountId: String, sinceState: String): ChangesOutcome = threadChanges

    override suspend fun identityGet(accountId: String): GetResult<WireIdentity> =
        GetResult(identityState, identities)

    override suspend fun identityChanges(accountId: String, sinceState: String): ChangesOutcome = identityChanges

    override suspend fun emailSet(accountId: String, update: Map<String, JsonObject>): SetOutcome {
        emailSetCalls.add(update)
        setFailure?.let { throw it }
        val rejected = update.keys.filter { it in setRejections }
        return SetOutcome(
            newState = emailState,
            updated = update.keys - rejected.toSet(),
            notUpdated = rejected.associateWith { setRejections.getValue(it) },
        )
    }

    /** Every `PushSubscription/set` the client made, in order. */
    val pushSetCalls = mutableListOf<Pair<PushSubscriptionCreate?, List<String>>>()

    /** Every `PushSubscription/set { update }` patch, by subscription id. */
    val pushUpdateCalls = mutableListOf<Map<String, JsonObject>>()

    /** The verification code the next create is answered with. */
    var pushVerificationCode: String? = null

    /** The id the next create is answered with. */
    var pushSubscriptionId: String = "1"

    /** When set, the create comes back rejected with this description. */
    var pushRejection: String? = null

    /** When set, every `PushSubscription/set` throws it. */
    var pushFailure: Throwable? = null

    override suspend fun pushSubscriptionSet(
        create: PushSubscriptionCreate?,
        update: Map<String, JsonObject>,
        destroy: List<String>,
    ): PushSetOutcome {
        pushSetCalls.add(create to destroy)
        if (update.isNotEmpty()) pushUpdateCalls.add(update)
        pushFailure?.let { throw it }
        pushRejection?.let {
            return PushSetOutcome(
                createdId = null,
                verificationCode = null,
                notCreated = it,
                destroyed = destroy,
            )
        }
        return PushSetOutcome(
            createdId = if (create == null) null else pushSubscriptionId,
            verificationCode = if (create == null) null else pushVerificationCode,
            notCreated = null,
            destroyed = destroy,
        )
    }

    override suspend fun derivedCategories(accountId: String): List<String> = categories

    // ---- compose and search -------------------------------------------

    /** Ids the next `Email/query` returns, by account. */
    var queryIds: Map<String, List<String>> = emptyMap()

    /** Snippets the next `SearchSnippet/get` returns. */
    var snippets: List<WireSnippet> = emptyList()

    var seenAddresses: List<WireSeenAddress> = emptyList()

    /** When set, every compose or search call throws it. */
    var composeFailure: Throwable? = null

    val queryCalls = mutableListOf<Pair<String, JsonObject>>()
    val snippetCalls = mutableListOf<Pair<JsonObject, List<String>>>()
    val uploads = mutableListOf<Triple<String, String, Int>>()
    val emailCreates = mutableListOf<Pair<String, JsonObject>>()
    val emailReplaces = mutableListOf<Triple<String, String, JsonObject>>()
    val emailDestroys = mutableListOf<List<String>>()
    val sendCalls = mutableListOf<SendCall>()

    /** One recorded [sendEmail], so a test can assert the whole batch's shape. */
    data class SendCall(
        val accountId: String,
        val email: JsonObject,
        val draftId: String?,
        val identityId: String,
        val envelope: Envelope,
        val onSuccessUpdate: JsonObject,
        val parentId: String?,
        val parentKeyword: String?,
    )

    var uploadBlobId: String = "blob-1"
    var createdEmailId: String = "draft-1"
    var sentEmailId: String = "sent-1"
    var emailCreateError: String? = null
    var submissionError: String? = null

    override suspend fun emailQuery(
        accountId: String,
        filter: JsonObject,
        limit: Int,
        collapseThreads: Boolean,
    ): List<String> {
        composeFailure?.let { throw it }
        queryCalls.add(accountId to filter)
        return queryIds[accountId] ?: emptyList()
    }

    override suspend fun searchSnippets(
        accountId: String,
        filter: JsonObject,
        emailIds: List<String>,
    ): List<WireSnippet> {
        composeFailure?.let { throw it }
        snippetCalls.add(filter to emailIds)
        return snippets.filter { it.emailId in emailIds }
    }

    override suspend fun seenAddresses(accountId: String): List<WireSeenAddress> {
        composeFailure?.let { throw it }
        return seenAddresses
    }

    override suspend fun uploadBlob(
        accountId: String,
        bytes: ByteArray,
        type: String,
        filename: String?,
    ): UploadedBlob {
        composeFailure?.let { throw it }
        uploads.add(Triple(accountId, type, bytes.size))
        return UploadedBlob(accountId, uploadBlobId, type, bytes.size.toLong())
    }

    override suspend fun emailCreate(accountId: String, email: JsonObject): EmailWriteOutcome {
        composeFailure?.let { throw it }
        emailCreates.add(accountId to email)
        return EmailWriteOutcome(
            id = if (emailCreateError == null) createdEmailId else null,
            newState = emailState,
            error = emailCreateError,
        )
    }

    override suspend fun emailReplace(accountId: String, id: String, email: JsonObject): EmailWriteOutcome {
        composeFailure?.let { throw it }
        emailReplaces.add(Triple(accountId, id, email))
        return EmailWriteOutcome(id = id, newState = emailState, error = emailCreateError)
    }

    override suspend fun emailDestroy(accountId: String, ids: List<String>) {
        composeFailure?.let { throw it }
        emailDestroys.add(ids)
    }

    override suspend fun sendEmail(
        accountId: String,
        email: JsonObject,
        draftId: String?,
        identityId: String,
        envelope: Envelope,
        onSuccessUpdate: JsonObject,
        parentId: String?,
        parentKeyword: String?,
    ): SubmissionOutcome {
        composeFailure?.let { throw it }
        sendCalls.add(
            SendCall(accountId, email, draftId, identityId, envelope, onSuccessUpdate, parentId, parentKeyword),
        )
        return SubmissionOutcome(
            submissionId = if (submissionError == null) "sub-1" else null,
            emailId = if (submissionError == null) (draftId ?: sentEmailId) else null,
            error = submissionError,
        )
    }

    override suspend fun downloadBlob(
        accountId: String,
        blobId: String,
        type: String,
        name: String,
    ): DownloadedBlob = throw JmapException("no blob $blobId in the fake", status = 404)

    /** Avatar blob ids by address, for the sender-picture lookup. */
    var principalAvatars: Map<String, String> = emptyMap()

    override suspend fun principalAvatarBlobId(accountId: String, email: String): String? =
        principalAvatars[email.lowercase()]
}
