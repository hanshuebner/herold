package com.netzhansa.herold.shared.search

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.inbox.InboxAssembler
import com.netzhansa.herold.shared.inbox.ThreadRow
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.store.LocalStore
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray

/** Where the rows on screen came from (REQ-AND-SYNC-13). */
enum class SearchScope {
    /** `Email/query` against the server's index: the whole mailbox. */
    SERVER,

    /** The locally synced set only, because the server was not reachable. */
    CACHED,
}

/** A search result row: the thread, plus the server's highlighted snippet when there is one. */
data class SearchHit(
    val row: ThreadRow,
    val subjectSnippet: String? = null,
    val previewSnippet: String? = null,
)

/** What a search produced. */
data class SearchResults(
    val query: String,
    val scope: SearchScope,
    val hits: List<SearchHit>,
    val message: String? = null,
)

/**
 * Search over the principal's mail.
 *
 * Online it is `Email/query` with the suite's filter shape - `text` plus
 * the Trash/Junk exclusion as a *sibling key* of one flat filter object,
 * which is what the server's fast-query gate recognises (suite REQ-SRC-02
 * and REQ-SRC-06, `web/apps/suite/src/lib/mail/store.svelte.ts`
 * `applyTrashJunkExclusion`) - with `SearchSnippet/get` for the
 * highlights (REQ-SRC-30).
 *
 * Offline it filters the local store on sender, subject and the cached
 * preview and says so, because the full-corpus index lives on the server
 * (REQ-AND-SYNC-13).
 */
class MailSearch(
    private val api: JmapApi,
    private val store: LocalStore,
    private val limit: Int = DEFAULT_LIMIT,
) {

    /**
     * The `Email/query` filter for [query]: one flat `FilterCondition`
     * whose keys are AND-ed by the server (RFC 8621 section 5.5).
     */
    fun filter(query: String, mailboxes: List<Mailbox>, accountId: String): JsonObject {
        val excluded = mailboxes
            .filter { it.accountId == accountId && (it.role == MailboxRoles.TRASH || it.role == MailboxRoles.JUNK) }
            .map { it.id }
        return buildJsonObject {
            put("text", query.trim())
            if (excluded.isNotEmpty()) {
                putJsonArray("inMailboxOtherThan") { excluded.forEach { add(it) } }
            }
        }
    }

    /**
     * Runs [query] against every account in [accountScope] (all of them
     * when it is null), falling back to the local store when the server
     * cannot be reached.
     */
    suspend fun search(
        query: String,
        accounts: List<Account>,
        mailboxes: List<Mailbox>,
        accountScope: String? = null,
    ): SearchResults {
        val trimmed = query.trim()
        if (trimmed.isBlank()) return SearchResults(trimmed, SearchScope.SERVER, emptyList())
        val targets = accounts.filter { accountScope == null || it.id == accountScope }

        val emails = mutableListOf<Email>()
        val snippets = mutableMapOf<String, com.netzhansa.herold.shared.jmap.WireSnippet>()
        try {
            targets.forEach { account ->
                val accountFilter = filter(trimmed, mailboxes, account.id)
                val ids = api.emailQuery(account.id, accountFilter, limit, collapseThreads = true)
                if (ids.isEmpty()) return@forEach
                val fetched = api.emailGet(account.id, ids)
                emails.addAll(fetched.list.map { it.toStoreRow(account.id) })
                runCatching { api.searchSnippets(account.id, accountFilter, ids) }
                    .getOrDefault(emptyList())
                    .forEach { snippets[account.id + ":" + it.emailId] = it }
            }
        } catch (t: Throwable) {
            return cached(trimmed, accounts, mailboxes, accountScope)
        }

        val rows = InboxAssembler.threadRows(emails, accounts, mailboxes, accountScope)
        return SearchResults(
            query = trimmed,
            scope = SearchScope.SERVER,
            hits = rows.map { row ->
                val snippet = snippets[row.accountId + ":" + row.latestEmailId]
                SearchHit(row, snippet?.subject, snippet?.preview)
            },
        )
    }

    /** The cached-only pass, also used directly when the device is known to be offline. */
    suspend fun cached(
        query: String,
        accounts: List<Account>,
        mailboxes: List<Mailbox>,
        accountScope: String? = null,
    ): SearchResults {
        val trimmed = query.trim()
        val matches = store.searchCached(trimmed, limit.toLong())
        val rows = InboxAssembler.threadRows(matches, accounts, mailboxes, accountScope)
        return SearchResults(
            query = trimmed,
            scope = SearchScope.CACHED,
            hits = rows.map { SearchHit(it) },
            message = "Cached results only - the full search needs a connection",
        )
    }

    companion object {
        const val DEFAULT_LIMIT = 50
    }
}

/** Splits a snippet on its `<mark>` runs so the UI can highlight them. */
fun splitSnippet(snippet: String): List<Pair<String, Boolean>> {
    val out = mutableListOf<Pair<String, Boolean>>()
    var index = 0
    while (index < snippet.length) {
        val open = snippet.indexOf("<mark>", index, ignoreCase = true)
        if (open < 0) {
            out.add(unescape(snippet.substring(index)) to false)
            break
        }
        if (open > index) out.add(unescape(snippet.substring(index, open)) to false)
        val close = snippet.indexOf("</mark>", open, ignoreCase = true)
        if (close < 0) {
            out.add(unescape(snippet.substring(open + 6)) to true)
            break
        }
        out.add(unescape(snippet.substring(open + 6, close)) to true)
        index = close + 7
    }
    return out.filter { it.first.isNotEmpty() }
}

private fun unescape(value: String): String = value
    .replace("&lt;", "<")
    .replace("&gt;", ">")
    .replace("&quot;", "\"")
    .replace("&#39;", "'")
    .replace("&amp;", "&")
