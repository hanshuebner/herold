package com.netzhansa.herold.shared.search

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.WireAddress
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.jmap.WireSnippet
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonPrimitive
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** Search: the filter's shape, the snippets, and the offline fallback. */
class MailSearchTest {

    private val accounts = listOf(
        Account(id = "a2", name = "alice@example.local", isPrimary = true),
        Account(id = "a5", name = "Classic Computing", isPrimary = false, sortOrder = 1),
    )

    private val mailboxes = listOf(
        Mailbox("a2", "7", "INBOX", MailboxRoles.INBOX),
        Mailbox("a2", "10", "Trash", MailboxRoles.TRASH),
        Mailbox("a2", "11", "Junk", MailboxRoles.JUNK),
        Mailbox("a5", "26", "INBOX", MailboxRoles.INBOX),
    )

    private fun search(api: FakeJmapApi, store: FakeLocalStore) = MailSearch(api, store)

    @Test
    fun theFilterIsFlatWithTheExclusionAsASiblingKey() {
        val filter = MailSearch(FakeJmapApi(), FakeLocalStore()).filter("report", mailboxes, "a2")
        // Flat: text and inMailboxOtherThan side by side, no operator wrapper.
        // The server's fast-query gate only recognises this shape (REQ-SRC-06).
        assertEquals(setOf("text", "inMailboxOtherThan"), filter.keys)
        assertEquals("report", filter["text"]?.jsonPrimitive?.content)
        assertContentEquals(
            listOf("10", "11"),
            filter["inMailboxOtherThan"]!!.jsonArray.map { it.jsonPrimitive.content },
        )
    }

    @Test
    fun anAccountWithoutTrashOrJunkGetsNoExclusion() {
        val filter = MailSearch(FakeJmapApi(), FakeLocalStore()).filter("report", mailboxes, "a5")
        assertEquals(setOf("text"), filter.keys)
    }

    @Test
    fun onlineSearchQueriesEveryAccountAndKeepsTheSnippets() = runTest {
        val api = FakeJmapApi()
        api.queryIds = mapOf("a2" to listOf("1"), "a5" to listOf("9"))
        api.emails = mapOf(
            "1" to WireEmail(
                id = "1", threadId = "t1", subject = "Quarterly report",
                from = listOf(WireAddress("Bob", "bob@example.local")), receivedAt = "2026-09-11T10:00:00Z",
            ),
            "9" to WireEmail(
                id = "9", threadId = "t9", subject = "Board report",
                from = listOf(WireAddress("Member", "member@classic-computing.example")),
                receivedAt = "2026-09-10T10:00:00Z",
            ),
        )
        api.snippets = listOf(
            WireSnippet("1", subject = "Quarterly <mark>report</mark>", preview = "the <mark>report</mark> is in"),
            WireSnippet("9", subject = "Board <mark>report</mark>", preview = null),
        )
        val results = search(api, FakeLocalStore()).search("report", accounts, mailboxes)

        assertEquals(SearchScope.SERVER, results.scope)
        assertEquals(listOf("Quarterly report", "Board report"), results.hits.map { it.row.subject })
        assertEquals("Quarterly <mark>report</mark>", results.hits[0].subjectSnippet)
        assertEquals(2, api.queryCalls.size)
        assertEquals(listOf("a2", "a5"), api.queryCalls.map { it.first })
    }

    @Test
    fun scopingToOneAccountQueriesOnlyThatAccount() = runTest {
        val api = FakeJmapApi()
        api.queryIds = mapOf("a2" to emptyList(), "a5" to emptyList())
        search(api, FakeLocalStore()).search("report", accounts, mailboxes, accountScope = "a5")
        assertEquals(listOf("a5"), api.queryCalls.map { it.first })
    }

    @Test
    fun withoutAConnectionTheLocalStoreAnswersAndSaysSo() = runTest {
        val api = FakeJmapApi()
        api.composeFailure = RuntimeException("connection refused")
        val store = FakeLocalStore()
        store.upsertEmails(
            listOf(
                Email(
                    accountId = "a2", id = "1", threadId = "t1", subject = "Quarterly report",
                    fromName = "Bob", fromEmail = "bob@example.local", receivedAt = 200,
                ),
                Email(
                    accountId = "a2", id = "2", threadId = "t2", subject = "Lunch",
                    fromName = "Carol", fromEmail = "carol@example.com", preview = "about the report", receivedAt = 100,
                ),
                Email(
                    accountId = "a2", id = "3", threadId = "t3", subject = "Unrelated",
                    fromName = "Dan", fromEmail = "dan@example.com", receivedAt = 50,
                ),
            ),
        )
        val results = search(api, store).search("report", accounts, mailboxes)

        assertEquals(SearchScope.CACHED, results.scope)
        assertEquals(listOf("Quarterly report", "Lunch"), results.hits.map { it.row.subject })
        assertTrue(results.message!!.startsWith("Cached results only"))
        assertNull(results.hits[0].subjectSnippet)
    }

    @Test
    fun snippetHighlightsSplitIntoMarkedAndPlainRuns() {
        assertEquals(
            listOf("Quarterly " to false, "report" to true, " 2026" to false),
            splitSnippet("Quarterly <mark>report</mark> 2026"),
        )
        assertEquals(listOf("a & b" to false), splitSnippet("a &amp; b"))
    }
}
