package com.netzhansa.herold.shared.outbox

import com.netzhansa.herold.shared.domain.Keywords
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

private fun payload(threadId: String?, subject: String) = ComposePayload(
    accountId = "acct-a",
    identityId = "id-1",
    identityName = "Alice",
    identityEmail = "alice@example.local",
    to = listOf(OutboxAddress("Bob Example", "bob@example.local")),
    subject = subject,
    bodyHtml = "<p>on my way</p>",
    attachments = listOf(
        OutboxAttachment(name = "map.png", type = "image/png", size = 12, spool = "spool-1-map.png"),
    ),
    draftsMailboxId = "drafts-1",
    threadId = threadId,
    parentId = "e1",
)

private fun entry(
    id: Long,
    kind: OutboxKind = OutboxKind.SEND,
    state: OutboxState = OutboxState.QUEUED,
    threadId: String? = "t-1",
    subject: String = "Re: Hello",
    lastError: String? = null,
    permanent: Boolean = false,
) = OutboxEntry(
    id = id,
    accountId = "acct-a",
    kind = kind,
    label = "Send: $subject",
    payload = outboxJson.encodeToString(payload(threadId, subject)),
    revertJson = null,
    entityIds = emptyList(),
    createdAt = id * 1_000,
    state = state,
    attempts = 0,
    lastError = lastError,
    permanent = permanent,
    nextAttemptAt = 0,
)

class PendingMessageTest {

    @Test
    fun aQueuedReplyBelongsToItsConversation() {
        val pending = listOf(entry(1), entry(2, threadId = "t-2")).pendingMessagesIn("acct-a", "t-1")

        assertEquals(1, pending.size)
        assertEquals(1L, pending.single().entryId)
        assertEquals("alice@example.local", pending.single().fromEmail)
        assertEquals("Bob Example", pending.single().recipientLine)
        assertEquals(PendingMessage.MARKER_QUEUED, pending.single().marker)
    }

    @Test
    fun theMarkerSaysWhatTheMessageIsWaitingFor() {
        assertEquals(
            PendingMessage.MARKER_SENDING,
            listOf(entry(1, state = OutboxState.SENDING)).pendingMessages().single().marker,
        )
        assertEquals(
            PendingMessage.MARKER_DRAFT,
            listOf(entry(1, kind = OutboxKind.DRAFT)).pendingMessages().single().marker,
        )
        assertEquals(
            PendingMessage.MARKER_FAILED,
            listOf(
                entry(1, state = OutboxState.FAILED, lastError = "mailbox is read-only", permanent = true),
            ).pendingMessages().single().marker,
        )
    }

    @Test
    fun aRetryableErrorIsNotShownAsAFailure() {
        val pending = listOf(entry(1, lastError = "server is restarting")).pendingMessages().single()

        assertEquals(PendingMessage.MARKER_QUEUED, pending.marker)
        assertEquals(null, pending.failure)
    }

    @Test
    fun anActionEntryIsNotAMessage() {
        val action = entry(1).copy(kind = OutboxKind.ACTION, payload = "{\"patches\":{}}")

        assertTrue(listOf(action).pendingMessages().isEmpty())
    }

    @Test
    fun anEntryWithNoConversationIsNotPlacedInOne() {
        val pending = listOf(entry(1, threadId = null)).pendingMessages()

        assertEquals(1, pending.size)
        assertEquals(null, pending.single().threadId)
        assertTrue(listOf(entry(1, threadId = null)).pendingMessagesIn("acct-a", "t-1").isEmpty())
    }

    @Test
    fun theConversationsWithSomethingWaitingAreNamedWithTheirMarker() {
        val markers = listOf(entry(1), entry(2, kind = OutboxKind.DRAFT, threadId = "t-2"))
            .pendingMarkersByThread()

        assertEquals(mapOf("t-1" to PendingMessage.MARKER_QUEUED, "t-2" to PendingMessage.MARKER_DRAFT), markers)
    }

    @Test
    fun waitingMessagesAreOrderedAsTheyWereWritten() {
        val pending = listOf(entry(3), entry(1), entry(2)).pendingMessages()

        assertEquals(listOf(1L, 2L, 3L), pending.map { it.entryId })
    }

    @Test
    fun aWaitingMessageIsReadAsTheMessageItWillBe() {
        val message = listOf(entry(1)).pendingMessages().single()

        val email = message.asEmail()
        assertEquals(PendingMessage.ID_PREFIX + message.entryId, email.id)
        assertEquals("t-1", email.threadId)
        assertEquals("<p>on my way</p>", email.bodyHtml)
        assertEquals("Bob Example", email.toLine)
        assertEquals("on my way", email.preview)
        assertTrue(email.keywords.contains(Keywords.SEEN))
        val attachment = email.attachments.single()
        assertEquals("map.png", attachment.name)
        assertEquals("spool-1-map.png", PendingMessage.spoolHandle(attachment.blobId))
    }

    @Test
    fun anUploadedAttachmentIsReadFromItsBlob() {
        val message = listOf(entry(1)).pendingMessages().single()
            .let { it.copy(attachments = it.attachments.map { file -> file.copy(blobId = "blob-9") }) }

        val attachment = message.asEmail().attachments.single()
        assertEquals("blob-9", attachment.blobId)
        assertEquals(null, PendingMessage.spoolHandle(attachment.blobId))
    }
}
