package com.netzhansa.herold.shared.jmap

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * Which correction a feedback record carries (`internal/protoadmin/
 * spam_feedback.go`): `spam` and `phishing` record a message the reader
 * put into Junk, `ham` one they took out of it.
 */
enum class SpamFeedbackKind(val wire: String) {
    SPAM("spam"),
    PHISHING("phishing"),
    HAM("ham"),
    ;

    companion object {
        fun from(value: String): SpamFeedbackKind =
            entries.firstOrNull { it.wire.equals(value, ignoreCase = true) } ?: SPAM
    }
}

/**
 * The server's spam-feedback surface (`POST /api/v1/spam-feedback`,
 * REQ-FILT-70, issue #382). The record is the correction signal alone -
 * the message's own move between Junk and the inbox is an `Email/set`
 * the outbox carries separately - and the server joins it to the
 * classifier's recorded verdict for the message and writes it to the
 * audit log as `mail.spam.feedback`, which is the corpus an operator
 * exports for tuning.
 *
 * It is its own interface rather than part of [JmapApi] for the reason
 * [BugReportApi] is: a REST endpoint of the same server on the same
 * bearer token, called only by the outbox drain.
 */
interface SpamFeedbackApi {
    /**
     * Records that [emailId] was corrected to [kind]. Throws
     * [JmapException] carrying the status the server answered with, so
     * the drain classifies a refusal, a busy server and a dead wire the
     * way it does for every other entry.
     */
    suspend fun postSpamFeedback(emailId: String, kind: SpamFeedbackKind)
}

/**
 * The request body `POST /api/v1/spam-feedback` takes: the numeric
 * message id JMAP names the message by, and the correction. The server
 * reads nothing else from the client - it looks the recorded verdict up
 * itself - so the body is these two fields and the Suite sends the
 * same pair (`web/apps/suite/src/lib/mail/store.svelte.ts`).
 */
fun spamFeedbackBody(emailId: String, kind: SpamFeedbackKind): JsonObject = buildJsonObject {
    put("emailId", emailId)
    put("kind", kind.wire)
}
