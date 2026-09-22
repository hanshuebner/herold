package com.netzhansa.herold.android.push

import com.netzhansa.herold.shared.push.DismissReason
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * What the FCM receiver and the UnifiedPush receiver make of a
 * `mail-dismiss` push (issue #481): both hand their data map to
 * [PushDelivery], which resolves it to the withdrawal the shade acts on
 * and to nothing else. On the host JVM, since the resolution is the
 * payload and the payload alone.
 */
class PushDismissalRoutingTest {

    @Test
    fun `a mail-dismiss push resolves to the withdrawal it names`() {
        val dismissal = PushDelivery.dismissalOf(mapOf("payload" to DISMISS_PAYLOAD))!!

        assertEquals("a2", dismissal.accountId)
        assertEquals("t17", dismissal.threadId)
        assertEquals("41", dismissal.emailId)
        assertEquals(DismissReason.SEEN, dismissal.reason)
    }

    @Test
    fun `an arriving message is not a withdrawal`() {
        assertNull(PushDelivery.dismissalOf(mapOf("payload" to MAIL_PAYLOAD)))
    }

    @Test
    fun `a push carrying no payload withdraws nothing`() {
        assertNull(PushDelivery.dismissalOf(emptyMap()))
        assertNull(PushDelivery.dismissalOf(mapOf("payload" to "not json")))
    }

    private companion object {
        const val DISMISS_PAYLOAD = """{"@type":"StateChange","changed":{"a2":{"Email":"129"}},
            "kind":"mail-dismiss","type":"mail-dismiss",
            "emailId":"41","threadId":"t17","reason":"seen"}"""

        const val MAIL_PAYLOAD = """{"@type":"StateChange","changed":{"a2":{"Email":"128"}},
            "kind":"mail","type":"email","from":"Bob Example <bob@example.local>",
            "subject":"Lunch on Friday","emailId":"41","threadId":"t17"}"""
    }
}
