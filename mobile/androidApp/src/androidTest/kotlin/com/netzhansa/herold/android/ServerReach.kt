package com.netzhansa.herold.android

import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.URI

/**
 * The stretch of wire between the app and the dev instance, which a
 * test can take away and give back while the radio stays up
 * (issue #433).
 *
 * It is a TCP relay on the device, in the instrumentation process -
 * which is the app's own process - so the app's requests are real
 * requests over the device's network stack. Taking it away is what the
 * reported phone met: a name that would not resolve with a validated
 * network underneath, so the platform says the device is online while
 * nothing the client sends arrives. It comes back on the same port, so
 * the session the app signed in on keeps working.
 *
 * herold forms the session descriptor's URLs from the request's Host
 * header when no public base URL is configured, so the app goes on
 * talking to the relay for the API, uploads and the event stream.
 */
class ServerReach(target: String) : AutoCloseable {

    private val targetHost: String
    private val targetPort: Int
    private var server: ServerSocket
    private val live = mutableListOf<Socket>()

    /** Where the app signs in: the relay's end of the wire. */
    val baseUrl: String get() = "http://127.0.0.1:${server.localPort}"

    init {
        val uri = URI(target)
        targetHost = uri.host
        targetPort = if (uri.port > 0) uri.port else 80
        server = ServerSocket()
        server.reuseAddress = true
        server.bind(InetSocketAddress("127.0.0.1", 0))
        serve(server)
    }

    /** Nothing the app sends reaches the server from here on. */
    fun takeAway() {
        val port = server.localPort
        runCatching { server.close() }
        synchronized(live) {
            live.forEach { runCatching { it.close() } }
            live.clear()
        }
        // The port stays this relay's, so the app's session survives the
        // outage and the same base URL works when the wire comes back.
        held = port
    }

    /** The wire is back, on the port the app is signed in against. */
    fun giveBack() {
        val port = held ?: return
        server = ServerSocket()
        server.reuseAddress = true
        server.bind(InetSocketAddress("127.0.0.1", port))
        serve(server)
        held = null
    }

    private var held: Int? = null

    private fun serve(socket: ServerSocket) {
        Thread {
            while (true) {
                val accepted = try {
                    socket.accept()
                } catch (t: Throwable) {
                    return@Thread
                }
                synchronized(live) { live.add(accepted) }
                Thread { relay(accepted) }.apply { isDaemon = true }.start()
            }
        }.apply { isDaemon = true }.start()
    }

    private fun relay(from: Socket) {
        val to = try {
            Socket(targetHost, targetPort)
        } catch (t: Throwable) {
            runCatching { from.close() }
            return
        }
        synchronized(live) { live.add(to) }
        pump(from, to)
        pump(to, from)
    }

    /**
     * The connections open right now go silent and stay open: bytes
     * written into them are dropped in both directions and neither end
     * is told. It is the half-open socket a phone is left holding when
     * its network goes out from under an established connection - the
     * event stream the server still writes to and the client still
     * believes in (issue #436). Connections opened afterwards are
     * relayed as usual.
     */
    fun swallow() {
        synchronized(live) { condemned.addAll(live) }
    }

    private val condemned: MutableSet<Socket> =
        java.util.Collections.newSetFromMap(java.util.concurrent.ConcurrentHashMap<Socket, Boolean>())

    private fun pump(from: Socket, to: Socket) {
        Thread {
            val buffer = ByteArray(8 * 1024)
            runCatching {
                while (true) {
                    val read = from.getInputStream().read(buffer)
                    if (read < 0) break
                    if (from in condemned || to in condemned) continue
                    to.getOutputStream().write(buffer, 0, read)
                    to.getOutputStream().flush()
                }
            }
            runCatching { to.close() }
            runCatching { from.close() }
        }.apply { isDaemon = true }.start()
    }

    override fun close() {
        takeAway()
    }
}
