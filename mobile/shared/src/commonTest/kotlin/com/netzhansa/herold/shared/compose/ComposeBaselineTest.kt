package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Account
import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.domain.MailAddress
import com.netzhansa.herold.shared.fake.FakeJmapApi
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * Whether the reader changed anything since the composer opened, which
 * is what a close reads to decide there is a draft worth keeping
 * (issue #371).
 *
 * Each composer kind is opened and then closed untouched, and opened
 * and edited in each of the ways the screen can edit it.
 */
class ComposeBaselineTest {

    private val identity = Identity(
        accountId = "a2",
        id = "default",
        name = "Alice",
        email = "alice@example.local",
    )

    private val accounts = listOf(Account(id = "a2", name = "Alice", isPrimary = true))

    private val parent = Email(
        accountId = "a2",
        id = "11",
        threadId = "t11",
        fromName = "Bob",
        fromEmail = "bob@example.local",
        toAddresses = listOf(MailAddress(null, "alice@example.local")),
        subject = "Quarterly report",
        messageId = listOf("parent@example.local"),
        bodyHtml = "<p>The original body.</p>",
        attachments = listOf(
            Attachment(blobId = "blob-1", name = "report.pdf", type = "application/pdf", size = 2048),
        ),
    )

    private val draft = Email(
        accountId = "a2",
        id = "d1",
        threadId = "t11",
        fromName = "Alice",
        fromEmail = "alice@example.local",
        toAddresses = listOf(MailAddress("Bob", "bob@example.local")),
        subject = "Re: Quarterly report",
        inReplyTo = listOf("parent@example.local"),
        references = listOf("parent@example.local"),
        bodyHtml = "<p>nearly done</p>",
    )

    private val composer = Composer(FakeJmapApi())

    private fun opened(mode: ComposeMode): ComposeState = when (mode) {
        ComposeMode.NEW -> composer.openNew(listOf(identity), accounts, "a2")
        ComposeMode.EDIT_DRAFT -> composer.openDraft(draft, listOf(identity), accounts)
        else -> composer.openFrom(mode, parent, listOf(identity), accounts, "Mon, 1 Jan")
    }

    /** A composer left exactly as it opened has nothing to keep. */
    @Test
    fun anUntouchedComposerIsUnchanged() {
        listOf(ComposeMode.NEW, ComposeMode.REPLY, ComposeMode.REPLY_ALL, ComposeMode.FORWARD, ComposeMode.EDIT_DRAFT)
            .forEach { mode ->
                val state = opened(mode)
                assertFalse(
                    state.changedSince(ComposeBaseline.of(state)),
                    "$mode reports a change the reader did not make",
                )
            }
    }

    /**
     * A reply opens carrying a recipient, a subject and the quote, so
     * absolute emptiness cannot be what a close reads.
     */
    @Test
    fun aReplyOpensWithSomethingInEveryFieldAClosePreviouslyRead() {
        val reply = opened(ComposeMode.REPLY)

        assertTrue(reply.hasRecipient)
        assertTrue(reply.subject.isNotBlank())
        assertTrue(HtmlText.toPlainText(reply.bodyHtml).isNotBlank())
    }

    /** What the reader types into the body. */
    @Test
    fun typingInTheBodyIsAChange() {
        listOf(ComposeMode.NEW, ComposeMode.REPLY, ComposeMode.FORWARD, ComposeMode.EDIT_DRAFT).forEach { mode ->
            val state = opened(mode)
            val baseline = ComposeBaseline.of(state)
            val typed = state.copy(bodyHtml = "<p>Thanks, will do.</p>" + state.bodyHtml)

            assertTrue(typed.changedSince(baseline), "$mode misses what was typed in the body")
        }
    }

    /** What the reader types into the subject. */
    @Test
    fun editingTheSubjectIsAChange() {
        listOf(ComposeMode.NEW, ComposeMode.REPLY, ComposeMode.FORWARD, ComposeMode.EDIT_DRAFT).forEach { mode ->
            val state = opened(mode)
            val baseline = ComposeBaseline.of(state)

            assertTrue(
                state.copy(subject = state.subject + " (draft)").changedSince(baseline),
                "$mode misses an edited subject",
            )
        }
    }

    /** An address added, and an address taken away. */
    @Test
    fun changingTheRecipientsIsAChange() {
        val reply = opened(ComposeMode.REPLY)
        val baseline = ComposeBaseline.of(reply)

        assertTrue(reply.copy(cc = listOf(MailAddress(null, "carol@example.com"))).changedSince(baseline))
        assertTrue(reply.copy(to = emptyList()).changedSince(baseline))

        val fresh = opened(ComposeMode.NEW)
        assertTrue(
            fresh.copy(to = listOf(MailAddress(null, "bob@example.local")))
                .changedSince(ComposeBaseline.of(fresh)),
        )
    }

    /** A file the reader added, over the ones a forward brought along. */
    @Test
    fun attachingAFileIsAChange() {
        val forward = opened(ComposeMode.FORWARD)
        val baseline = ComposeBaseline.of(forward)
        val added = ComposeAttachment(
            key = "blob-2:notes.txt",
            name = "notes.txt",
            type = "text/plain",
            size = 12,
            blobId = "blob-2",
            status = AttachmentStatus.READY,
        )

        assertTrue(forward.attachments.isNotEmpty(), "a forward carries the parent's files")
        assertTrue(forward.copy(attachments = forward.attachments + added).changedSince(baseline))
        assertTrue(forward.copy(attachments = emptyList()).changedSince(baseline))
    }

    /**
     * The editor republishes the document it was seeded with in its own
     * markup as it loads: the same words in different markup are not an
     * edit.
     */
    @Test
    fun theEditorsOwnMarkupOfTheSameWordsIsNotAChange() {
        val reply = opened(ComposeMode.REPLY)
        val baseline = ComposeBaseline.of(reply)
        val republished = reply.copy(
            bodyHtml = "<p><br></p>\n  <blockquote>\n<div>The original body.</div>\n</blockquote>\n",
        )

        assertFalse(
            republished.changedSince(baseline.withBody(republished.bodyHtml)),
            "the body the editor published as it loaded reads as an edit",
        )
        assertTrue(
            republished.copy(bodyHtml = "<p>Yes.</p>" + republished.bodyHtml)
                .changedSince(baseline.withBody(republished.bodyHtml)),
            "a body typed after the editor loaded is missed",
        )
    }

    /** Whitespace the editor moves around is not an edit; words are. */
    @Test
    fun whitespaceIsNotContentButWordsAre() {
        val empty = ComposeState(mode = ComposeMode.NEW, accountId = "a2", identity = identity)

        assertFalse(empty.copy(bodyHtml = "<p></p><p></p>").changedSince(ComposeBaseline.EMPTY))
        assertFalse(empty.copy(bodyHtml = "<p>&nbsp;</p>").changedSince(ComposeBaseline.EMPTY))
        assertTrue(empty.copy(bodyHtml = "<p>something</p>").changedSince(ComposeBaseline.EMPTY))
        assertTrue(empty.copy(subject = "hi").changedSince(ComposeBaseline.EMPTY))
        assertTrue(
            empty.copy(to = listOf(MailAddress(null, "bob@example.local")))
                .changedSince(ComposeBaseline.EMPTY),
        )
    }

    /**
     * A draft reopened for editing and closed as it stands is the same
     * draft: it is neither written again nor thrown away.
     */
    @Test
    fun aReopenedDraftClosedAsItStandsIsUnchanged() {
        val state = opened(ComposeMode.EDIT_DRAFT)
        val baseline = ComposeBaseline.of(state)

        assertFalse(state.changedSince(baseline))
        assertTrue(state.copy(bodyHtml = "<p>nearly done, and now done</p>").changedSince(baseline))
    }
}
