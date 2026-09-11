package com.netzhansa.herold.shared.fake

import com.netzhansa.herold.shared.jmap.ChangesOutcome
import com.netzhansa.herold.shared.jmap.DownloadedBlob
import com.netzhansa.herold.shared.jmap.GetResult
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.jmap.JmapSession
import com.netzhansa.herold.shared.jmap.SetOutcome
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.jmap.WireIdentity
import com.netzhansa.herold.shared.jmap.WireMailbox
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

    override suspend fun derivedCategories(accountId: String): List<String> = categories

    override suspend fun downloadBlob(
        accountId: String,
        blobId: String,
        type: String,
        name: String,
    ): DownloadedBlob = throw JmapException("no blob $blobId in the fake", status = 404)
}
