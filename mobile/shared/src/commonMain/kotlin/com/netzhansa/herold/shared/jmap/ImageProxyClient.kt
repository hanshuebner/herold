package com.netzhansa.herold.shared.jmap

import io.ktor.client.HttpClient
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.statement.bodyAsBytes
import io.ktor.http.HttpHeaders
import io.ktor.http.encodeURLParameter
import io.ktor.http.isSuccess

/**
 * herold's image proxy (`GET /proxy/image?url=...`, suite
 * `docs/design/web/notes/server-contract.md`). The reading pane routes a
 * remote image through it so the sender never sees the recipient's address
 * or IP; the bearer token authenticates the request the way the suite's
 * session cookie does for the browser.
 */
class ImageProxyClient(
    private val httpClient: HttpClient,
    private val client: JmapClient,
) {
    /**
     * Returns the proxied image, or null when the proxy refused it. The
     * caller renders nothing in that case rather than falling back to a
     * direct fetch, which would leak the open to the sender.
     */
    suspend fun fetch(url: String): DownloadedBlob? {
        val token = runCatching { client.bearerToken() }.getOrNull() ?: return null
        val proxied = "${client.baseUrl.trimEnd('/')}/proxy/image?url=${url.encodeURLParameter()}"
        val response = runCatching {
            httpClient.get(proxied) { header(HttpHeaders.Authorization, "Bearer $token") }
        }.getOrNull() ?: return null
        if (!response.status.isSuccess()) return null
        val contentType = response.headers[HttpHeaders.ContentType] ?: "application/octet-stream"
        return DownloadedBlob(contentType = contentType, bytes = response.bodyAsBytes())
    }
}
