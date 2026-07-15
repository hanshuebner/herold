package com.netzhansa.herold.shared.domain

import kotlin.test.Test
import kotlin.test.assertEquals

class MailboxTest {
    @Test
    fun holdsTheFieldsTheMailboxListRenders() {
        val mailbox = Mailbox(id = "mb-1", name = "Inbox", unreadEmails = 3, totalEmails = 10)

        assertEquals("mb-1", mailbox.id)
        assertEquals("Inbox", mailbox.name)
        assertEquals(3, mailbox.unreadEmails)
        assertEquals(10, mailbox.totalEmails)
    }
}
