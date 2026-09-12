package com.netzhansa.herold.shared.mail

import com.netzhansa.herold.shared.domain.Email
import io.ktor.client.HttpClient
import io.ktor.client.request.header
import io.ktor.client.request.post
import io.ktor.client.request.setBody
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.HttpHeaders
import io.ktor.http.contentType
import io.ktor.http.isSuccess

/**
 * The RFC 8058 one-click POST (suite REQ-UNS-20). The request carries the
 * fixed body and nothing else: no bearer token, no cookie, no referrer,
 * no identifying header. The client it runs on is the app's plain HTTP
 * client, which installs no cookie storage and no auth plugin, so the
 * unsubscribe endpoint learns nothing about the account beyond whatever
 * the sender already encoded in the URL.
 */
class UnsubscribeClient(private val httpClient: HttpClient) {
    /** What a one-click POST did, for the message the shell shows. */
    data class Result(val ok: Boolean, val status: Int? = null, val error: String? = null)

    suspend fun postOneClick(url: String): Result = try {
        val response = httpClient.post(url) {
            contentType(ContentType.Application.FormUrlEncoded)
            // RFC 8058 section 3.1: the POST carries exactly this body.
            setBody(ONE_CLICK_BODY)
            // Ktor sends no Referer of its own; stating the RFC's
            // no-referrer intent explicitly keeps an engine that would
            // add one from doing so.
            header(HttpHeaders.Referrer, "")
        }
        // The body is read so the connection is released; its content is
        // the sender's confirmation page, which the phone does not show.
        response.bodyAsText()
        Result(ok = response.status.isSuccess(), status = response.status.value)
    } catch (t: Throwable) {
        Result(ok = false, error = t.message)
    }

    companion object {
        const val ONE_CLICK_BODY = "List-Unsubscribe=One-Click"
    }
}

/**
 * The thread-level Unsubscribe affordance's model (REQ-UNS-10/11/12): the
 * newest message of the conversation that advertises a mechanism, and the
 * mechanism it advertises. Null when no message in the thread carries a
 * usable `List-Unsubscribe`.
 */
data class UnsubscribeOffer(
    val email: Email,
    val mechanism: ListHeaders.Mechanism,
) {
    /** The name the success message uses (REQ-UNS-40). */
    val senderDisplay: String get() = email.senderDisplay

    companion object {
        /**
         * Scans [emails] newest-first, so a list message followed by a
         * one-off reply from the same sender still offers the list's
         * mechanism.
         */
        fun of(emails: List<Email>): UnsubscribeOffer? {
            for (index in emails.indices.reversed()) {
                val email = emails[index]
                val mechanism = ListHeaders.chooseMechanism(
                    email.listUnsubscribe,
                    email.listUnsubscribePost,
                ) ?: continue
                return UnsubscribeOffer(email, mechanism)
            }
            return null
        }
    }
}

/** The wording the suite uses, so both clients say the same thing. */
object UnsubscribeMessages {
    const val BUTTON = "Unsubscribe"

    /** REQ-UNS-04. */
    const val CLEARTEXT =
        "The sender's unsubscribe link is unencrypted; use the link in the message body if you trust it."

    /** REQ-UNS-40. */
    fun success(sender: String): String = "Unsubscribed from $sender"

    /** REQ-UNS-41. */
    const val FAILED = "Unsubscribe failed - try the link in the message body"
}
