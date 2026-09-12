package com.netzhansa.herold.android

import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * The account's own REST surface, called from the test process with a
 * bearer token: the api-keys list (where an OAuth2 access token is a
 * row, which is how a test expires one) and the credentials list the
 * sessions screen reads.
 */
object AccountApi {

    /** One entry of `GET /api/v1/api-keys`. */
    data class ApiKey(val id: Long, val label: String, val createdAt: String)

    fun apiKeys(baseUrl: String, token: String): List<ApiKey> {
        val body = request("GET", "$baseUrl/api/v1/api-keys", token).second
        val items = JSONObject(body).getJSONArray("items")
        return (0 until items.length()).map { i ->
            val row = items.getJSONObject(i)
            ApiKey(row.getLong("id"), row.optString("label"), row.optString("created_at"))
        }
    }

    /**
     * Deletes the access-token row of the newest OAuth2 grant issued to
     * [clientId]. The refresh token is untouched, so the next call the
     * app makes is a 401 that its silent refresh has to recover from
     * (REQ-AND-AUTH-04).
     */
    fun expireAccessToken(baseUrl: String, token: String, clientId: String): Long {
        val row = apiKeys(baseUrl, token)
            .filter { it.label == "device: oauth2:$clientId" }
            .maxByOrNull { it.createdAt }
            ?: error("no OAuth2 access-token row for $clientId")
        val status = request("DELETE", "$baseUrl/api/v1/api-keys/${row.id}", token).first
        check(status == 204) { "deleting the access-token row answered $status" }
        return row.id
    }

    /** Raw `GET /api/v1/auth/credentials` entries. */
    fun credentials(baseUrl: String, token: String): List<JSONObject> {
        val body = request("GET", "$baseUrl/api/v1/auth/credentials", token).second
        val items = JSONObject(body).getJSONArray("items")
        return (0 until items.length()).map { items.getJSONObject(it) }
    }

    fun ownGrantId(baseUrl: String, token: String, clientId: String): String =
        credentials(baseUrl, token)
            .filter { it.optString("kind") == "oauth2_grant" && it.optString("client_id") == clientId }
            .maxByOrNull { it.optString("created_at") }
            ?.getString("id")
            ?: error("no OAuth2 grant for $clientId")

    fun revokeCredential(baseUrl: String, token: String, kind: String, id: String): Int =
        request("DELETE", "$baseUrl/api/v1/auth/credentials/$kind/$id", token).first

    private fun request(method: String, url: String, token: String): Pair<Int, String> {
        val connection = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = 15_000
            readTimeout = 15_000
            setRequestProperty("Authorization", "Bearer $token")
        }
        val status = connection.responseCode
        val body = (if (status >= 400) connection.errorStream else connection.inputStream)
            ?.bufferedReader()?.use { it.readText() }.orEmpty()
        connection.disconnect()
        return status to body
    }
}
