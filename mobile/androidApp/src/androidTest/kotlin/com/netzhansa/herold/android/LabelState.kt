package com.netzhansa.herold.android

import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.jmap.WireMailbox
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * The account's labels as a class found them, and the restore that puts
 * them back (issue #414).
 *
 * Category state is account-wide and outlives the process: a label, its
 * disposition and its place in the five-pin budget are the server's, so
 * one class's provisioning is the next class's starting point. A class
 * that writes labels takes a snapshot in `@Before` and restores it in
 * `@After`, which is what lets the instrumented suite run in any order
 * and twice over.
 */
class LabelState private constructor(
    private val client: JmapClient,
    private val accountId: String,
    private val taken: List<WireMailbox>,
) {

    /**
     * Sets every label of the account to disposition `none`, so the
     * five-pin budget belongs to the caller whatever an earlier class
     * pinned.
     */
    suspend fun unpinAll(keep: Set<String> = emptySet()) {
        val folded = keep.map { it.lowercase() }.toSet()
        val pinned = labels().filter { it.disposition != "none" && it.name.lowercase() !in folded }
        if (pinned.isEmpty()) return
        val outcome = client.mailboxSet(
            accountId,
            update = pinned.associate { it.id to unset() },
        )
        check(outcome.errorMessages.isEmpty()) {
            "clearing the account's dispositions failed: ${outcome.errorMessages}"
        }
    }

    /**
     * Destroys the labels [names] names, so their categories reach the
     * client as the classifier's own rather than as a label's stated
     * lane.
     */
    suspend fun dropLabels(names: Set<String>) {
        val folded = names.map { it.lowercase() }.toSet()
        val doomed = labels().filter { it.name.lowercase() in folded }
        if (doomed.isEmpty()) return
        val outcome = client.mailboxSet(accountId, destroy = doomed.map { it.id })
        check(outcome.errorMessages.isEmpty()) {
            "dropping ${doomed.map { it.name }} failed: ${outcome.errorMessages}"
        }
    }

    /**
     * Puts the account back the way the snapshot found it: labels the
     * class created are destroyed, and the dispositions and priorities
     * of the ones that were already there are written back.
     *
     * The dispositions go back in two passes - everything to `none`
     * first, then the snapshot's own lanes in priority order - so
     * restoring five pinned labels does not meet the pinned limit
     * halfway through.
     */
    suspend fun restore() {
        val current = labels()
        val known = taken.associateBy { it.id }
        val created = current.filter { it.id !in known }
        if (created.isNotEmpty()) {
            val outcome = client.mailboxSet(accountId, destroy = created.map { it.id })
            check(outcome.errorMessages.isEmpty()) {
                "removing the labels this class created failed: ${outcome.errorMessages}"
            }
        }
        val survivors = current.filter { it.id in known }
        val strayed = survivors.filter {
            val was = known.getValue(it.id)
            it.disposition != was.disposition || it.priority != was.priority
        }
        if (strayed.isEmpty()) return
        val cleared = strayed.filter { it.disposition != "none" }
        if (cleared.isNotEmpty()) {
            client.mailboxSet(accountId, update = cleared.associate { it.id to unset() })
        }
        val restored = strayed.map { known.getValue(it.id) }
            .sortedBy { it.priority ?: Int.MAX_VALUE }
        restored.forEach { was ->
            val outcome = client.mailboxSet(
                accountId,
                update = mapOf(
                    was.id to buildJsonObject {
                        put("disposition", was.disposition)
                        if (was.priority == null) {
                            put("priority", JsonPrimitive(null as String?))
                        } else {
                            put("priority", was.priority)
                        }
                    },
                ),
            )
            check(outcome.errorMessages.isEmpty()) {
                "restoring ${was.name} to ${was.disposition} failed: ${outcome.errorMessages}"
            }
        }
    }

    private suspend fun labels(): List<WireMailbox> =
        client.mailboxGet(accountId, null).list.filter { it.role == null }

    private fun unset() = buildJsonObject {
        put("disposition", "none")
        put("priority", JsonPrimitive(null as String?))
    }

    companion object {
        /** The account's labels as they stand now. */
        suspend fun take(client: JmapClient, accountId: String): LabelState =
            LabelState(client, accountId, client.mailboxGet(accountId, null).list.filter { it.role == null })
    }
}
