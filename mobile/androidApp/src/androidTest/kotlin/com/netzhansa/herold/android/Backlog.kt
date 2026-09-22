package com.netzhansa.herold.android

import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject

/**
 * Puts an account further behind than one `Email/changes` answer
 * carries, which is the state the phone in issue #450 was in: the
 * client's stored state is older than [Backlog.MORE_THAN_ONE_ANSWER]
 * changes, so the server trims its answer and reports more to come.
 *
 * The changes are drafts written straight over JMAP from the test
 * process, so they are server changes of the same kind mail arriving
 * makes, and the app learns of them only through its own fold.
 */
object Backlog {

    /**
     * More changes than `JmapClient` asks for in one answer
     * (`maxChanges`), so the answer is trimmed.
     */
    const val MORE_THAN_ONE_ANSWER = 300

    /** How many creates go in one request, under the session's maxObjectsInSet. */
    private const val CHUNK = 50

    /** Writes [count] drafts into the account, in batches. */
    suspend fun raise(client: JmapClient, count: Int = MORE_THAN_ONE_ANSWER) {
        val accountId = client.session().mailAccountId ?: error("the session carries no mail account")
        val drafts = client.mailboxGet(accountId, null).list
            .firstOrNull { it.role == MailboxRoles.DRAFTS }
            ?: error("the account has no drafts mailbox")
        var written = 0
        while (written < count) {
            val batch = minOf(CHUNK, count - written)
            val args = buildJsonObject {
                put("accountId", accountId)
                putJsonObject("create") {
                    repeat(batch) { index ->
                        putJsonObject("b${written + index}") {
                            putJsonObject("mailboxIds") { put(drafts.id, true) }
                            putJsonObject("keywords") { put("\$draft", true) }
                            putJsonArray("from") {
                                add(buildJsonObject { put("email", DevInstance.email) })
                            }
                            put("subject", "backlog ${written + index}")
                            putJsonObject("bodyValues") {
                                putJsonObject("b") { put("value", "backlog ${written + index}") }
                            }
                            putJsonArray("textBody") {
                                add(
                                    buildJsonObject {
                                        put("partId", "b")
                                        put("type", "text/plain")
                                    },
                                )
                            }
                        }
                    }
                }
            }
            val response = client.batch(
                listOf(JmapClient.MethodCall("Email/set", args, "c0")),
                listOf(Capability.CORE, Capability.MAIL),
            ).single()
            val created = (response.args["created"] as? kotlinx.serialization.json.JsonObject)?.size ?: 0
            check(created == batch) { "Email/set created $created of $batch drafts: ${response.args}" }
            written += batch
        }
    }
}
