package com.netzhansa.herold.shared.jmap

import com.netzhansa.herold.shared.auth.FakeHttp
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.auth.respondJson
import io.ktor.client.engine.mock.toByteArray
import io.ktor.client.request.HttpRequestData
import io.ktor.http.HttpStatusCode
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import kotlin.test.fail

/**
 * What `POST /api/v1/bug-reports` carries (issue #416, #417): one
 * multipart request whose parts are the drop's files, each named and
 * filed under its drop name, on the account's bearer token. The server
 * writes the drop directory from exactly these names
 * (`internal/protoadmin/bugreports.go`).
 */
class BugReportPostTest {

    private val parts = listOf(
        BugReportPart("report.json", "application/json", "{\"title\":\"blank thread\"}\n".encodeToByteArray()),
        BugReportPart("report.md", "text/markdown", "# Bug: blank thread\n".encodeToByteArray()),
        BugReportPart("logs.txt", "text/plain", "INFO shell state=mail\n".encodeToByteArray()),
        BugReportPart("screenshot-1.png", "image/png", byteArrayOf(0x89.toByte(), 0x50, 0x4E, 0x47)),
    )

    private class Recorded(var request: HttpRequestData? = null, var body: String = "")

    private suspend fun client(
        recorded: Recorded,
        status: HttpStatusCode = HttpStatusCode.Created,
        response: String = "{\"id\":\"20260917T101010Z-0f1e2d3c\"}",
    ): JmapClient {
        val tokens = InMemoryTokenStore().apply { store("device-token-1") }
        val http = FakeHttp.client { request ->
            recorded.request = request
            recorded.body = request.body.toByteArray().decodeToString()
            respondJson(response, status)
        }
        return JmapClient(http, "http://10.0.2.2:8080", tokens)
    }

    @Test
    fun theBundleGoesUpAsOneMultipartRequestOnTheBearerToken() = runTest {
        val recorded = Recorded()
        val id = client(recorded).postBugReport(parts)

        assertEquals("20260917T101010Z-0f1e2d3c", id)
        val request = recorded.request ?: fail("nothing was posted")
        assertEquals("POST", request.method.value)
        assertEquals("http://10.0.2.2:8080/api/v1/bug-reports", request.url.toString())
        assertEquals("Bearer device-token-1", request.headers["Authorization"])
        assertTrue(
            request.body.contentType?.toString()?.startsWith("multipart/form-data") == true,
            "the body is ${request.body.contentType}",
        )
    }

    @Test
    fun everyDropFileIsItsOwnPartUnderItsDropName() = runTest {
        val recorded = Recorded()
        client(recorded).postBugReport(parts)

        parts.forEach { part ->
            assertTrue(
                recorded.body.contains("name=\"${part.name}\""),
                "no part named ${part.name} in ${recorded.body}",
            )
            // The server reads the drop's files out of the multipart
            // form's FILE parts, which is what the filename makes them.
            assertTrue(
                recorded.body.contains("filename=\"${part.name}\""),
                "part ${part.name} carries no filename in ${recorded.body}",
            )
            assertTrue(
                recorded.body.contains("Content-Type: ${part.type}"),
                "part ${part.name} carries no ${part.type} in ${recorded.body}",
            )
        }
        assertTrue(recorded.body.contains("# Bug: blank thread"), recorded.body)
        assertTrue(recorded.body.contains("{\"title\":\"blank thread\"}"), recorded.body)
    }

    @Test
    fun aRefusedReportCarriesTheServersProblemDetail() = runTest {
        val recorded = Recorded()
        val client = client(
            recorded,
            status = HttpStatusCode.PayloadTooLarge,
            response = "{\"type\":\"about:blank\",\"title\":\"payload_too_large\"," +
                "\"detail\":\"part \\\"screenshot-1.png\\\" exceeds the 8388608 byte limit\"}",
        )
        val failure = runCatching { client.postBugReport(parts) }.exceptionOrNull()

        val jmap = failure as? JmapException ?: fail("the refusal was not a JmapException: $failure")
        assertEquals(413, jmap.status)
        assertTrue(
            jmap.message!!.contains("exceeds the 8388608 byte limit"),
            "the reason is lost: ${jmap.message}",
        )
        // 413 is the server's decision, so the entry stops rather than retrying.
        assertEquals(FailureKind.REFUSED, classifyFailure(jmap))
    }

    /**
     * The refusal that cost three crash traces (issue #420): a server
     * that does not know a part answers 400. Nothing about the bundle
     * is wrong for a server that does know it, so this is the server
     * being behind - the client waits for it rather than giving the
     * report up.
     */
    @Test
    fun aPartTheServerDoesNotKnowIsTheServerBeingBehind() = runTest {
        val recorded = Recorded()
        val client = client(
            recorded,
            status = HttpStatusCode.BadRequest,
            response = "{\"type\":\"about:blank\",\"title\":\"bad_request\"," +
                "\"detail\":\"unexpected part \\\"crash.txt\\\"\"}",
        )
        val failure = runCatching { client.postBugReport(parts) }.exceptionOrNull()

        val jmap = failure as? JmapException ?: fail("the refusal was not a JmapException: $failure")
        assertEquals(400, jmap.status)
        assertEquals(FailureKind.UNSUPPORTED, classifyFailure(jmap))
    }

    @Test
    fun aBusyServerIsWorthAnotherAttempt() = runTest {
        val recorded = Recorded()
        val client = client(recorded, status = HttpStatusCode.ServiceUnavailable, response = "{}")
        val failure = runCatching { client.postBugReport(parts) }.exceptionOrNull()

        assertEquals(FailureKind.BUSY, classifyFailure(failure!!))
    }
}
