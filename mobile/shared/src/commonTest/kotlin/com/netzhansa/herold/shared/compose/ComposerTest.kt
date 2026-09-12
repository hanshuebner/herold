package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.Mailbox
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.jmap.JmapSession
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlin.test.Test
import kotlin.test.assertContains
import kotlin.test.assertEquals
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** Draft writing, sending and attachment handling against a scripted server. */
class ComposerTest {

    private val accounts = listOf(
        Account(id = "a2", name = "alice@example.local", isPrimary = true, sortOrder = 0),
        Account(id = "a5", name = "vorsitz@classic-computing.example", isPrimary = false, sortOrder = 1),
    )

    private val identities = listOf(
        Identity("a2", "default", "Alice", "alice@example.local"),
        Identity("a5", "default", "Vorsitz", "vorsitz@classic-computing.example"),
    )

    private val mailboxes = listOf(
        Mailbox("a2", "7", "INBOX", MailboxRoles.INBOX),
        Mailbox("a2", "9", "Drafts", MailboxRoles.DRAFTS),
        Mailbox("a2", "8", "Sent", MailboxRoles.SENT),
        Mailbox("a5", "28", "Drafts", MailboxRoles.DRAFTS),
        Mailbox("a5", "27", "Sent", MailboxRoles.SENT),
    )

    private val parent = Email(
        accountId = "a2",
        id = "11",
        threadId = "t11",
        fromName = "Bob",
        fromEmail = "bob@example.local",
        toAddresses = listOf(MailAddress(null, "alice@example.local"), MailAddress("Carol", "carol@example.com")),
        deliveredTo = "alice@example.local",
        subject = "Quarterly report",
        messageId = listOf("parent@example.local"),
        bodyText = "The original body.",
    )

    private fun composer(api: FakeJmapApi) = Composer(api, newCid = { "inline-1@herold.local" })

    private fun api(): FakeJmapApi = FakeJmapApi(
        session = JmapSession(
            capabilities = mapOf(
                "urn:ietf:params:jmap:core" to buildJsonObject { put("maxSizeUpload", 1024) },
            ),
        ),
    )

    @Test
    fun aReplyOpensOnTheParentsAccountWithItsThreadingHeaders() {
        val state = composer(api()).openFrom(ComposeMode.REPLY, parent, identities, accounts, "today")
        assertEquals("a2", state.accountId)
        assertEquals("default", state.identity?.id)
        assertEquals(listOf(MailAddress("Bob", "bob@example.local")), state.to)
        assertEquals(emptyList(), state.cc)
        assertEquals("Re: Quarterly report", state.subject)
        assertEquals(listOf("parent@example.local"), state.replyContext?.inReplyTo)
        assertEquals(ParentKeywords.ANSWERED, state.replyContext?.parentKeyword)
    }

    @Test
    fun aReplyAllCarriesTheOtherRecipientsInCc() {
        val state = composer(api()).openFrom(ComposeMode.REPLY_ALL, parent, identities, accounts, "today")
        assertEquals(listOf(MailAddress("Carol", "carol@example.com")), state.cc)
        assertTrue(state.showCc)
    }

    @Test
    fun aForwardQuotesTheOriginalAndMarksTheParentForwarded() {
        val state = composer(api()).openFrom(ComposeMode.FORWARD, parent, identities, accounts, "today")
        assertEquals("Fwd: Quarterly report", state.subject)
        assertEquals(emptyList(), state.to)
        assertEquals(ParentKeywords.FORWARDED, state.replyContext?.parentKeyword)
        assertContains(state.bodyHtml, "Forwarded message")
    }

    @Test
    fun anUploadOverTheServersLimitIsRefusedBeforeItIsSent() = runTest {
        val api = api()
        val result = composer(api).attach("a2", "big.bin", "application/octet-stream", ByteArray(2048), inline = false)
        assertIs<AttachResult.Rejected>(result)
        assertContains(result.message, "at most")
        assertTrue(api.uploads.isEmpty())
    }

    @Test
    fun anAcceptedUploadBecomesAReadyAttachment() = runTest {
        val api = api()
        api.uploadBlobId = "blob-42"
        val result = composer(api).attach("a2", "note.txt", "text/plain", ByteArray(10), inline = false)
        assertIs<AttachResult.Added>(result)
        assertEquals("blob-42", result.attachment.blobId)
        assertTrue(result.attachment.isReady)
        assertEquals(listOf(Triple("a2", "text/plain", 10)), api.uploads)
    }

    @Test
    fun theFirstSaveCreatesTheDraftAndTheNextOneUpdatesIt() = runTest {
        val api = api()
        val composer = composer(api)
        val state = composer.openFrom(ComposeMode.REPLY, parent, identities, accounts, "today")
        val saved = composer.saveDraft(state, mailboxes)
        assertIs<ComposeResult.Saved>(saved)
        assertEquals("draft-1", saved.draftId)

        val again = composer.saveDraft(state.copy(draftId = saved.draftId), mailboxes)
        assertIs<ComposeResult.Saved>(again)
        assertEquals(1, api.emailCreates.size)
        assertEquals(1, api.emailReplaces.size)
        assertEquals("draft-1", api.emailReplaces[0].second)

        // The draft lands in the account's Drafts mailbox with $draft set.
        val written = api.emailCreates[0].second
        assertEquals(true, written["mailboxIds"]?.jsonObject?.get("9")?.jsonPrimitive?.booleanOrNull)
        assertEquals(true, written["keywords"]?.jsonObject?.get("\$draft")?.jsonPrimitive?.booleanOrNull)
    }

    @Test
    fun sendWritesBothBodyAlternativesAndMovesTheMessageOutOfDrafts() = runTest {
        val api = api()
        val composer = composer(api)
        val state = composer.openFrom(ComposeMode.REPLY, parent, identities, accounts, "today")
            .let { it.copy(bodyHtml = "<p>My <b>answer</b>.</p>" + it.bodyHtml) }
        val result = composer.send(state, mailboxes)
        assertIs<ComposeResult.Sent>(result)

        val call = api.sendCalls.single()
        assertEquals("a2", call.accountId)
        assertEquals("default", call.identityId)
        assertEquals("alice@example.local", call.envelope.mailFrom)
        assertEquals(listOf("bob@example.local"), call.envelope.rcptTo)
        assertEquals("11", call.parentId)
        assertEquals(ParentKeywords.ANSWERED, call.parentKeyword)

        val text = call.email["bodyValues"]!!.jsonObject["1"]!!.jsonObject["value"]!!.jsonPrimitive.content
        val html = call.email["bodyValues"]!!.jsonObject["2"]!!.jsonObject["value"]!!.jsonPrimitive.content
        assertContains(html, "<b>answer</b>")
        assertContains(text, "My answer.")
        assertContains(text, "> The original body.")

        // onSuccessUpdateEmail takes it out of Drafts into Sent (REQ-DFT).
        assertEquals(JsonPrimitive(null as String?), call.onSuccessUpdate["mailboxIds/9"])
        assertEquals(true, call.onSuccessUpdate["mailboxIds/8"]?.jsonPrimitive?.booleanOrNull)
        assertEquals(JsonPrimitive(null as String?), call.onSuccessUpdate["keywords/\$draft"])
    }

    @Test
    fun inlineImagesAndAttachmentsAreDistinguishedOnTheWire() = runTest {
        val api = api()
        val composer = composer(api)
        val inline = ComposeAttachment(
            key = "i", name = "chart.png", type = "image/png", size = 12,
            blobId = "blob-inline", status = AttachmentStatus.READY, inline = true, cid = "inline-1@herold.local",
        )
        val attached = ComposeAttachment(
            key = "a", name = "report.pdf", type = "application/pdf", size = 99,
            blobId = "blob-file", status = AttachmentStatus.READY,
        )
        val state = ComposeState(
            mode = ComposeMode.NEW,
            accountId = "a2",
            identity = identities[0],
            to = listOf(MailAddress(null, "bob@example.local")),
            subject = "with pictures",
            bodyHtml = """<p><img src="https://inline.herold.invalid/inline-1@herold.local"></p>""",
            attachments = listOf(inline, attached),
        )
        composer.send(state, mailboxes)
        val email = api.sendCalls.single().email

        // The body references the image as cid:, not as the editor's scheme.
        val html = email["bodyValues"]!!.jsonObject["2"]!!.jsonObject["value"]!!.jsonPrimitive.content
        assertContains(html, "src=\"cid:inline-1@herold.local\"")

        val parts = email["attachments"]!!.jsonArray.map { it.jsonObject }
        assertEquals("inline", parts[0]["disposition"]?.jsonPrimitive?.content)
        assertEquals("inline-1@herold.local", parts[0]["cid"]?.jsonPrimitive?.content)
        assertEquals("attachment", parts[1]["disposition"]?.jsonPrimitive?.content)

        // multipart/mixed [ multipart/related [ alternative, inline ], file ]
        val structure = email["bodyStructure"]!!.jsonObject
        assertEquals("multipart/mixed", structure["type"]?.jsonPrimitive?.content)
        val related = structure["subParts"]!!.jsonArray[0].jsonObject
        assertEquals("multipart/related", related["type"]?.jsonPrimitive?.content)
        assertEquals(
            "multipart/alternative",
            related["subParts"]!!.jsonArray[0].jsonObject["type"]?.jsonPrimitive?.content,
        )
        assertEquals(true, email["hasAttachment"]?.jsonPrimitive?.booleanOrNull)
    }

    @Test
    fun aBodyWithoutAttachmentsIsJustTheTwoAlternatives() = runTest {
        val api = api()
        val state = ComposeState(
            mode = ComposeMode.NEW,
            accountId = "a2",
            identity = identities[0],
            to = listOf(MailAddress(null, "bob@example.local")),
            bodyHtml = "<p>hello</p>",
        )
        composer(api).send(state, mailboxes)
        val structure = api.sendCalls.single().email["bodyStructure"]!!.jsonObject
        assertEquals("multipart/alternative", structure["type"]?.jsonPrimitive?.content)
        assertNull(api.sendCalls.single().email["attachments"])
    }

    @Test
    fun sendWithoutConnectivityFailsVisibly() = runTest {
        val api = api()
        api.composeFailure = RuntimeException("connection refused")
        val state = ComposeState(
            mode = ComposeMode.NEW,
            accountId = "a2",
            identity = identities[0],
            to = listOf(MailAddress(null, "bob@example.local")),
        )
        val result = composer(api).send(state, mailboxes)
        assertIs<ComposeResult.Failed>(result)
        assertTrue(result.offline)
        assertContains(result.message, "No connection")
    }

    @Test
    fun sendWithoutARecipientIsRefused() = runTest {
        val state = ComposeState(mode = ComposeMode.NEW, accountId = "a2", identity = identities[0])
        val result = composer(api()).send(state, mailboxes)
        assertIs<ComposeResult.Failed>(result)
        assertContains(result.message, "recipient")
    }

    @Test
    fun composingFromTheSubAccountSendsThroughThatAccountsMailboxes() = runTest {
        val api = api()
        val state = ComposeState(
            mode = ComposeMode.NEW,
            accountId = "a5",
            identity = identities[1],
            to = listOf(MailAddress(null, "bob@example.local")),
        )
        composer(api).send(state, mailboxes)
        val call = api.sendCalls.single()
        assertEquals("a5", call.accountId)
        assertEquals(true, call.email["mailboxIds"]?.jsonObject?.get("28")?.jsonPrimitive?.booleanOrNull)
        assertEquals(true, call.onSuccessUpdate["mailboxIds/27"]?.jsonPrimitive?.booleanOrNull)
    }
}
