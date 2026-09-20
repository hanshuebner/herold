package com.netzhansa.herold.shared.diag

import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.outbox.BlobSpool
import com.netzhansa.herold.shared.outbox.BugReportPayload
import com.netzhansa.herold.shared.outbox.BugReportSpooledPart
import com.netzhansa.herold.shared.outbox.Outbox

/**
 * Hands a bug report to the durable outbox, which posts it to the
 * account server's `POST /api/v1/bug-reports` (REQ-AND-SYS-53, issue
 * #416). The bundle's files are spooled the way an attachment is and
 * the entry is drainable the moment it is written, so the upload starts
 * on the tap (issue #438); with no connection it waits in the queue and
 * leaves when there is one - a report written on a train goes out when
 * the train arrives. Nothing of it is filed in the user's mailboxes.
 */
class BugReportSender(
    private val outbox: Outbox,
    private val spool: BlobSpool,
) {
    /** Queues [bundle] for [accountId], ready for the next drain. */
    suspend fun queue(bundle: BugBundle, accountId: String): ComposeResult {
        val parts = bundle.files.map { file ->
            BugReportSpooledPart(
                name = file.name,
                type = file.type,
                size = file.bytes.size.toLong(),
                spool = spool.put(file.bytes, file.name),
            )
        }
        val entryId = outbox.enqueueBugReport(
            label = label(bundle.title),
            payload = BugReportPayload(
                accountId = accountId,
                title = bundle.title,
                parts = parts,
            ),
        )
        return ComposeResult.Queued(entryId)
    }

    companion object {
        /** What the outbox screen calls a queued report. */
        fun label(title: String): String = "Bug report: $title"
    }
}
