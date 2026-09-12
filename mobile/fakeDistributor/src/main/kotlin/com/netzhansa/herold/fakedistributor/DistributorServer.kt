package com.netzhansa.herold.fakedistributor

import android.content.Context
import android.content.Intent
import android.util.Log
import java.io.BufferedInputStream
import java.io.ByteArrayOutputStream
import java.io.OutputStream
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.atomic.AtomicBoolean

/**
 * The push side of the fake distributor: an HTTP listener on
 * [DistributorServer.PORT] that turns a POST into the UnifiedPush
 * MESSAGE broadcast the registered app receives.
 *
 *   POST /UP?token=<token>   the push; the body is handed on unchanged
 *   POST /control/gone       every later push answers 410, which is how
 *                            a dead endpoint is exercised
 *   POST /control/live       back to answering 201
 *   GET  /control/state      the delivery count and whether it is gone
 *
 * Nothing here interprets the body: a distributor carries encrypted
 * bytes it cannot read, and the acceptance run depends on that being
 * true of this one too.
 */
class DistributorServer(private val context: Context) {

    private val gone = AtomicBoolean(false)
    private var delivered = 0
    private var socket: ServerSocket? = null

    fun start() {
        if (socket != null) return
        val server = ServerSocket(PORT)
        socket = server
        Thread {
            while (!server.isClosed) {
                val client = try {
                    server.accept()
                } catch (e: Exception) {
                    if (!server.isClosed) Log.w(TAG, "accept failed: ${e.message}")
                    break
                }
                runCatching { handle(client) }
                    .onFailure { Log.w(TAG, "request failed: ${it.message}") }
                runCatching { client.close() }
            }
        }.apply { isDaemon = true }.start()
        Log.i(TAG, "fake distributor listening on $PORT")
    }

    fun stop() {
        runCatching { socket?.close() }
        socket = null
    }

    private fun handle(client: Socket) {
        val input = BufferedInputStream(client.getInputStream())
        val requestLine = readLine(input) ?: return
        var contentLength = 0
        while (true) {
            val header = readLine(input) ?: break
            if (header.isEmpty()) break
            val (name, value) = header.split(":", limit = 2).let {
                it[0].trim().lowercase() to it.getOrElse(1) { "" }.trim()
            }
            if (name == "content-length") contentLength = value.toIntOrNull() ?: 0
        }
        val body = ByteArray(contentLength)
        var read = 0
        while (read < contentLength) {
            val n = input.read(body, read, contentLength - read)
            if (n < 0) break
            read += n
        }
        val target = requestLine.split(" ").getOrElse(1) { "/" }
        val output = client.getOutputStream()
        when {
            target.startsWith("/control/gone") -> {
                gone.set(true)
                respond(output, 200, "gone")
            }
            target.startsWith("/control/live") -> {
                gone.set(false)
                respond(output, 200, "live")
            }
            target.startsWith("/control/state") ->
                respond(output, 200, "{\"delivered\":$delivered,\"gone\":${gone.get()}}")
            target.startsWith("/UP") -> deliver(target, body, output)
            else -> respond(output, 404, "no such endpoint")
        }
    }

    private fun deliver(target: String, body: ByteArray, output: OutputStream) {
        if (gone.get()) {
            // What a distributor answers once it has forgotten the
            // endpoint; herold destroys the subscription on it.
            respond(output, 410, "endpoint is gone")
            return
        }
        val token = target.substringAfter("token=", "").substringBefore('&')
        val registration = Registrations(context).byToken(token)
        if (registration == null) {
            respond(output, 404, "unknown token")
            return
        }
        context.sendBroadcast(
            Intent(ACTION_MESSAGE).apply {
                `package` = registration
                putExtra(EXTRA_TOKEN, token)
                putExtra(EXTRA_BYTES_MESSAGE, body)
                putExtra(EXTRA_MESSAGE_ID, "fake-" + (++delivered))
            },
        )
        Log.i(TAG, "delivered ${body.size} bytes to $registration")
        respond(output, 201, "delivered")
    }

    private fun respond(output: OutputStream, status: Int, body: String) {
        val bytes = body.encodeToByteArray()
        val head = "HTTP/1.1 $status ${reason(status)}\r\n" +
            "Content-Length: ${bytes.size}\r\n" +
            "Content-Type: text/plain\r\n" +
            "Connection: close\r\n\r\n"
        output.write(head.encodeToByteArray())
        output.write(bytes)
        output.flush()
    }

    private fun reason(status: Int): String = when (status) {
        200 -> "OK"
        201 -> "Created"
        404 -> "Not Found"
        410 -> "Gone"
        else -> "Unknown"
    }

    private fun readLine(input: BufferedInputStream): String? {
        val out = ByteArrayOutputStream()
        while (true) {
            val b = input.read()
            if (b < 0) return if (out.size() == 0) null else out.toString("UTF-8")
            if (b == '\n'.code) return out.toString("UTF-8").trimEnd('\r')
            out.write(b)
        }
    }

    companion object {
        const val TAG = "herold.fakedistributor"

        /**
         * The device port the acceptance run forwards a host port to; the
         * dev instance allowlists the host side of that pair.
         */
        const val PORT = 19280

        const val ACTION_MESSAGE = "org.unifiedpush.android.connector.MESSAGE"
        const val EXTRA_TOKEN = "token"
        const val EXTRA_BYTES_MESSAGE = "bytesMessage"
        const val EXTRA_MESSAGE_ID = "id"
    }
}
