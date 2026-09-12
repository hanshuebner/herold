package com.netzhansa.herold.android

import androidx.test.platform.app.InstrumentationRegistry
import java.io.BufferedReader
import java.io.InputStreamReader
import java.io.OutputStream
import java.security.KeyStore
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLServerSocket

/**
 * The HTTPS endpoint the List-Unsubscribe acceptance check points a
 * message's `List-Unsubscribe` header at, recording exactly what the app
 * sent (REQ-UNS-20).
 *
 * It speaks TLS because the one-click flow only applies to an `https:`
 * URL, and it runs in the instrumentation process - which is the app's
 * own process - so the request is a real OkHttp request over the device's
 * network stack to `127.0.0.1`. Its certificate is the build-time fixture
 * the debug network security config trusts.
 */
class UnsubscribeSink : AutoCloseable {
    /** One recorded request: the line, the headers, and the body. */
    data class Recorded(
        val method: String,
        val path: String,
        val headers: Map<String, String>,
        val body: String,
    ) {
        fun header(name: String): String? = headers[name.lowercase()]
    }

    private val server: SSLServerSocket = createServerSocket()
    private val received = mutableListOf<Recorded>()
    private val arrived = CountDownLatch(1)
    private var running = true

    /** The URL a `List-Unsubscribe` header points at. */
    val url: String get() = "https://127.0.0.1:${server.localPort}/unsub"

    init {
        Thread {
            while (running) {
                val socket = try {
                    server.accept()
                } catch (t: Throwable) {
                    return@Thread
                }
                runCatching { serve(socket) }
                runCatching { socket.close() }
            }
        }.apply { isDaemon = true }.start()
    }

    /** Waits for the first request, returning null when none arrives. */
    fun awaitRequest(timeoutMs: Long): Recorded? {
        arrived.await(timeoutMs, TimeUnit.MILLISECONDS)
        return synchronized(received) { received.firstOrNull() }
    }

    val requestCount: Int get() = synchronized(received) { received.size }

    override fun close() {
        running = false
        runCatching { server.close() }
    }

    private fun serve(socket: java.net.Socket) {
        socket.soTimeout = SOCKET_TIMEOUT_MS
        val reader = BufferedReader(InputStreamReader(socket.getInputStream()))
        val requestLine = reader.readLine() ?: return
        val parts = requestLine.split(" ")
        val headers = mutableMapOf<String, String>()
        while (true) {
            val line = reader.readLine() ?: break
            if (line.isBlank()) break
            val colon = line.indexOf(':')
            if (colon > 0) {
                headers[line.substring(0, colon).trim().lowercase()] = line.substring(colon + 1).trim()
            }
        }
        val length = headers["content-length"]?.toIntOrNull() ?: 0
        val body = if (length > 0) {
            val buffer = CharArray(length)
            var read = 0
            while (read < length) {
                val count = reader.read(buffer, read, length - read)
                if (count <= 0) break
                read += count
            }
            String(buffer, 0, read)
        } else {
            ""
        }
        synchronized(received) {
            received.add(
                Recorded(
                    method = parts.getOrElse(0) { "" },
                    path = parts.getOrElse(1) { "" },
                    headers = headers.toMap(),
                    body = body,
                ),
            )
        }
        respond(socket.getOutputStream())
        arrived.countDown()
    }

    private fun respond(out: OutputStream) {
        val payload = "unsubscribed\n".toByteArray()
        out.write(
            (
                "HTTP/1.1 200 OK\r\n" +
                    "Content-Type: text/plain\r\n" +
                    "Content-Length: ${payload.size}\r\n" +
                    "Connection: close\r\n\r\n"
                ).toByteArray(),
        )
        out.write(payload)
        out.flush()
    }

    private companion object {
        const val SOCKET_TIMEOUT_MS = 10_000

        /**
         * A TLS listener on a kernel-picked loopback port, keyed with the
         * fixture the build generated (`build.gradle.kts`,
         * generateAcceptanceTls).
         */
        fun createServerSocket(): SSLServerSocket {
            val assets = InstrumentationRegistry.getInstrumentation().context.assets
            val password = BuildConfig.ACCEPTANCE_TLS_PASSWORD.toCharArray()
            val keystore = KeyStore.getInstance("PKCS12").apply {
                assets.open("acceptance-sink.p12").use { load(it, password) }
            }
            val keys = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm()).apply {
                init(keystore, password)
            }
            val context = SSLContext.getInstance("TLS").apply {
                init(keys.keyManagers, null, null)
            }
            return (context.serverSocketFactory.createServerSocket(0, 1) as SSLServerSocket)
        }
    }
}
