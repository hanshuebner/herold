package com.netzhansa.herold.android

import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.net.HttpURLConnection
import java.net.URL
import java.util.zip.ZipInputStream

/**
 * The server's bug-reports surface as the test reads it back (issue
 * #416): the maintainer's end of what the phone posted. It is called
 * with a bug-reports-scoped (or admin) API key, which the harness passes
 * as the `heroldBugReportsKey` instrumentation argument - the reporter's
 * own end-user token cannot list or download, which is the point of the
 * scope.
 */
object BugReportsApi {

    /** One row of `GET /api/v1/bug-reports`. */
    data class Report(
        val id: String,
        val receivedAt: String,
        val email: String,
        val title: String,
        val route: String,
        val descriptionEntered: Boolean,
        val screenshotCount: Int,
    )

    fun list(baseUrl: String, key: String): List<Report> {
        val (status, body) = request("GET", "$baseUrl/api/v1/bug-reports", key)
        check(status == 200) { "listing bug reports answered $status: $body" }
        val items = JSONObject(body).getJSONArray("items")
        return (0 until items.length()).map { i ->
            val row = items.getJSONObject(i)
            Report(
                id = row.getString("id"),
                receivedAt = row.optString("received_at"),
                email = row.optString("email"),
                title = row.optString("title"),
                route = row.optString("route"),
                descriptionEntered = row.optBoolean("description_entered"),
                screenshotCount = row.optInt("screenshot_count"),
            )
        }
    }

    /** The drop of one report, by filename, as `herold bug-fetch` expands it. */
    fun drop(baseUrl: String, key: String, id: String): Map<String, ByteArray> {
        val connection = open("GET", "$baseUrl/api/v1/bug-reports/$id", key)
        val status = connection.responseCode
        val bytes = (if (status >= 400) connection.errorStream else connection.inputStream)
            ?.use { it.readBytes() } ?: ByteArray(0)
        connection.disconnect()
        check(status == 200) { "downloading $id answered $status: ${bytes.decodeToString()}" }
        val files = mutableMapOf<String, ByteArray>()
        ZipInputStream(ByteArrayInputStream(bytes)).use { zip ->
            while (true) {
                val entry = zip.nextEntry ?: break
                if (!entry.isDirectory) files[entry.name.substringAfterLast('/')] = zip.readBytes()
                zip.closeEntry()
            }
        }
        return files
    }

    fun delete(baseUrl: String, key: String, id: String) {
        val (status, body) = request("DELETE", "$baseUrl/api/v1/bug-reports/$id", key)
        check(status == 204) { "deleting $id answered $status: $body" }
    }

    /** `report.json` of a report's drop. */
    fun reportJson(baseUrl: String, key: String, id: String): JSONObject =
        JSONObject(
            drop(baseUrl, key, id)["report.json"]?.decodeToString()
                ?: error("the drop of $id carries no report.json"),
        )

    private fun request(method: String, url: String, key: String): Pair<Int, String> {
        val connection = open(method, url, key)
        val status = connection.responseCode
        val body = (if (status >= 400) connection.errorStream else connection.inputStream)
            ?.bufferedReader()?.use { it.readText() }.orEmpty()
        connection.disconnect()
        return status to body
    }

    private fun open(method: String, url: String, key: String): HttpURLConnection =
        (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = 15_000
            readTimeout = 30_000
            setRequestProperty("Authorization", "Bearer $key")
        }
}
