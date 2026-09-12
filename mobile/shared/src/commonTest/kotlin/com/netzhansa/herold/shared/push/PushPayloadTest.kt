package com.netzhansa.herold.shared.push

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The payload-to-notification mapping (REQ-AND-PUSH-11/12). [MAIL_PAYLOAD]
 * is the envelope herold's dispatcher builds for a new message
 * (`internal/webpush/payload.go` buildEmailPayload), so a server-side
 * rename of a field fails here rather than on the device.
 */
class PushPayloadTest {

    @Test
    fun parsesTheStateChangeEnvelopeAndThePreviewFields() {
        val envelope = PushEnvelope.parse(MAIL_PAYLOAD)!!

        assertEquals(PushKind.MAIL, envelope.kind)
        assertEquals("a2", envelope.accountId)
        assertEquals(listOf("Email"), envelope.changedTypes)
        assertEquals("Bob Example <bob@example.local>", envelope.from)
        assertEquals("Lunch on Friday", envelope.subject)
        assertEquals("Are you free at noon? The place on the corner", envelope.preview)
        assertEquals("41", envelope.emailId)
        assertEquals("t17", envelope.threadId)
        assertEquals("3", envelope.inboxMailboxId)
    }

    @Test
    fun readsThePayloadOutOfTheFcmDataMap() {
        val envelope = PushEnvelope.fromData(mapOf("payload" to MAIL_PAYLOAD))
        assertEquals("t17", envelope?.threadId)
    }

    @Test
    fun aMailPayloadRendersSenderAsTitleAndSubjectPlusPreviewAsBody() {
        val notification = PushEnvelope.parse(MAIL_PAYLOAD)!!.mailNotification()!!

        assertEquals("Bob Example <bob@example.local>", notification.title)
        assertEquals("Lunch on Friday - Are you free at noon? The place on the corner", notification.body)
        assertEquals("t17", notification.threadId)
        assertEquals("41", notification.emailId)
        assertEquals("3", notification.inboxMailboxId)
        assertEquals("thread:a2:t17", notification.tag)
        assertEquals("account:a2", notification.groupKey)
    }

    @Test
    fun twoMessagesOnOneThreadShareTheTagSoTheyCoalesce() {
        val first = PushEnvelope.parse(MAIL_PAYLOAD)!!.mailNotification()!!
        val second = PushEnvelope.parse(MAIL_PAYLOAD.replace("\"41\"", "\"42\""))!!.mailNotification()!!

        assertEquals(first.tag, second.tag)
        assertEquals("42", second.emailId)
    }

    @Test
    fun aSenderlessPayloadStillRenders() {
        val payload = """{"@type":"StateChange","changed":{"a2":{"Email":"9"}},"kind":"mail",
            |"subject":"No sender","emailId":"7","threadId":"t7"}""".trimMargin()
        val notification = PushEnvelope.parse(payload)!!.mailNotification()!!

        assertEquals(MailNotification.DEFAULT_TITLE, notification.title)
        assertEquals("No sender", notification.body)
    }

    @Test
    fun aChatPayloadIsNotAMailNotificationAndPostsToTheChatChannel() {
        val payload = """{"@type":"StateChange","changed":{"a2":{"Message":"5"}},"kind":"chat",
            |"from":"Alice","body":"ping","conversationId":"12"}""".trimMargin()
        val envelope = PushEnvelope.parse(payload)!!

        assertNull(envelope.mailNotification())
        assertEquals(PushChannels.CHAT, envelope.kind!!.channelId())
        assertEquals("12", envelope.conversationId)
    }

    @Test
    fun everyKindHasItsOwnChannel() {
        val channels = PushKind.entries.map { it.channelId() }
        assertEquals(channels.size, channels.toSet().size)
        assertTrue(PushChannels.ALL.containsAll(channels))
    }

    @Test
    fun theVerificationHandshakeIsReadFromItsOwnDataKey() {
        val data = mapOf(
            "verification" to """{"@type":"PushVerification","pushSubscriptionId":"7",
                |"verificationCode":"abc123"}""".trimMargin(),
        )
        val handshake = PushVerification.fromData(data)!!

        assertEquals("7", handshake.subscriptionId)
        assertEquals("abc123", handshake.code)
        // It is not a payload push and renders nothing.
        assertNull(PushEnvelope.fromData(data))
    }

    @Test
    fun aPayloadPushIsNotMistakenForAHandshake() {
        assertNull(PushVerification.fromData(mapOf("payload" to MAIL_PAYLOAD)))
    }

    @Test
    fun garbageIsDroppedRatherThanThrown() {
        assertNull(PushEnvelope.parse("not json"))
        assertNull(PushEnvelope.fromData(emptyMap()))
    }

    private companion object {
        /**
         * Recorded from herold's dispatcher: the JMAP `StateChange`
         * envelope plus the bounded preview fields, as the FCM transport
         * sends it in `data.payload`.
         */
        const val MAIL_PAYLOAD = """{"@type":"StateChange","changed":{"a2":{"Email":"128"}},
            "kind":"mail","type":"email","from":"Bob Example <bob@example.local>",
            "body":"Lunch on Friday","subject":"Lunch on Friday",
            "preview":"Are you free at noon? The place on the corner","mailbox":"Inbox",
            "emailId":"41","msgid":"41","threadId":"t17","inboxMailboxId":"3"}"""
    }
}
