package com.netzhansa.herold.android

import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * The server's audit log as a check reads it back (`GET /api/v1/audit`,
 * REQ-FILT-71). It is admin-scoped, so the harness passes the dev
 * instance's own API key and admin URL (`heroldAdminKey`,
 * `heroldAdminUrl`); without them the checks that read it skip, the way
 * the bug-report ones do.
 *
 * It is what proves a spam correction reached the server: the endpoint
 * the phone posts to answers 204 and writes a `mail.spam.feedback`
 * record, which is the only trace of the feedback anywhere.
 */
object AuditApi {

    /** One row of the log, with the fields the spam checks read. */
    data class Entry(
        val id: Long,
        val action: String,
        val subject: String,
        val metadata: Map<String, String>,
    )

    /** The log's entries for [action], newest first. */
    fun list(baseUrl: String, key: String, action: String, limit: Int = 50): List<Entry> {
        val url = "$baseUrl/api/v1/audit?action=$action&limit=$limit"
        val connection = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 15_000
            readTimeout = 30_000
            setRequestProperty("Authorization", "Bearer $key")
        }
        val status = connection.responseCode
        val body = (if (status >= 400) connection.errorStream else connection.inputStream)
            ?.bufferedReader()?.use { it.readText() }.orEmpty()
        connection.disconnect()
        check(status == 200) { "listing the audit log answered $status: $body" }
        val items = JSONObject(body).getJSONArray("items")
        return (0 until items.length()).map { index ->
            val row = items.getJSONObject(index)
            val metadata = row.optJSONObject("metadata")
            Entry(
                id = row.optLong("id"),
                action = row.optString("action"),
                subject = row.optString("subject"),
                metadata = metadata?.keys()?.asSequence()?.associateWith { metadata.optString(it) }.orEmpty(),
            )
        }
    }
}
