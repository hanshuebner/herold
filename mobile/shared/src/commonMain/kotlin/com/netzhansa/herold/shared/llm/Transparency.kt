package com.netzhansa.herold.shared.llm

import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.WireLlmInspect
import com.netzhansa.herold.shared.jmap.WireLlmTransparency

/**
 * The LLM-transparency reads (suite G7, REQ-FILT-65..68): the account's
 * prompt-and-model singleton for the settings page, and the per-message
 * classifier detail behind "Why is this here?".
 *
 * These are read-only server state with no offline model of their own, so
 * they are fetched on demand rather than synced; the screens go through
 * this seam instead of holding a JMAP client.
 */
class Transparency(private val api: JmapApi) {
    private val inspected = mutableMapOf<String, WireLlmInspect?>()
    private var overview: WireLlmTransparency? = null

    /**
     * The singleton, cached for the session. Null when the server does
     * not advertise the capability or the read failed, which the page
     * states rather than showing an empty form.
     */
    suspend fun overview(accountId: String): WireLlmTransparency? {
        overview?.let { return it }
        val fetched = runCatching { api.llmTransparency(accountId) }.getOrNull() ?: return null
        overview = fetched
        return fetched
    }

    /**
     * What the classifier was asked and answered for one message. Null
     * when it never ran on it - a message delivered before the classifier
     * was configured, or one classification failed on.
     */
    suspend fun inspect(accountId: String, emailId: String): WireLlmInspect? {
        if (inspected.containsKey(emailId)) return inspected[emailId]
        val fetched = runCatching { api.llmInspect(accountId, listOf(emailId)) }
            .getOrNull()?.firstOrNull { it.id == emailId }
        inspected[emailId] = fetched
        return fetched
    }
}

/** The wording the transparency surfaces use, matching the suite's. */
object TransparencyText {
    const val TITLE = "How herold sorts your mail"
    const val INSPECT_TITLE = "Message classification"
    const val NOT_CLASSIFIED =
        "This message was not classified. It may have been delivered before the classifier " +
            "was configured, or classification may have failed."
    const val UNAVAILABLE = "This server does not report how it sorts your mail."

    /** A [0,1] score as a percentage, or a dash when the classifier gave none. */
    fun confidence(value: Double?): String {
        if (value == null) return "-"
        val percent = (value * 100).toInt()
        return "$percent%"
    }
}
