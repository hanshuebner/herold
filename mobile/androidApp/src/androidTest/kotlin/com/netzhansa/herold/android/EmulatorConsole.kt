package com.netzhansa.herold.android

import androidx.test.platform.app.InstrumentationRegistry
import java.io.BufferedReader
import java.io.Closeable
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.net.Socket

/**
 * The emulator's console, as the device sees it. `adb emu <command>`
 * talks to a telnet listener on the host's loopback; from inside the
 * emulator that is 10.0.2.2, so a test can inject a sensor reading
 * itself rather than depending on the harness typing it at the right
 * moment.
 *
 * The listener asks for the token in `~/.emulator_console_auth_token`,
 * which the harness passes as an instrumentation argument. Without it
 * the console is unreachable and the check that needs it skips.
 */
class EmulatorConsole private constructor(
    private val socket: Socket,
    private val reader: BufferedReader,
    private val writer: OutputStreamWriter,
) : Closeable {

    /**
     * Swings the device: alternating strong acceleration for long
     * enough that the detector's window fills with accelerating
     * samples, then the phone back at rest.
     */
    fun shake(bursts: Int = DEFAULT_BURSTS) {
        repeat(bursts) { i ->
            val sign = if (i % 2 == 0) 1 else -1
            send("sensor set acceleration ${sign * SWING}:${sign * SWING}:${sign * SWING}")
            Thread.sleep(BURST_PAUSE_MS)
        }
        send("sensor set acceleration 0:9.81:0")
    }

    private fun send(command: String) {
        writer.write(command + "\n")
        writer.flush()
        drain()
    }

    /** Reads whatever the console answered, without blocking on silence. */
    private fun drain() {
        val deadline = System.currentTimeMillis() + READ_BUDGET_MS
        while (System.currentTimeMillis() < deadline) {
            if (!reader.ready()) {
                Thread.sleep(10)
                continue
            }
            val line = reader.readLine() ?: return
            if (line.startsWith("OK") || line.startsWith("KO")) return
        }
    }

    override fun close() {
        runCatching { send("quit") }
        runCatching { socket.close() }
    }

    companion object {
        /** Magnitude per axis, well past the detector's 13 m/s^2. */
        private const val SWING = 18

        private const val DEFAULT_BURSTS = 40
        private const val BURST_PAUSE_MS = 50L
        private const val READ_BUDGET_MS = 1_000L
        private const val CONNECT_TIMEOUT_MS = 5_000

        /**
         * The console named by `heroldEmulatorConsole` (default
         * `10.0.2.2:5554`) authenticated with `heroldEmulatorToken`.
         * Null when no token was passed, which is how a run on a
         * physical device skips the gesture check.
         */
        fun fromArguments(): EmulatorConsole? {
            val arguments = InstrumentationRegistry.getArguments()
            val token = arguments.getString("heroldEmulatorToken")?.takeIf { it.isNotBlank() }
                ?: return null
            val address = arguments.getString("heroldEmulatorConsole")?.takeIf { it.isNotBlank() }
                ?: "10.0.2.2:5554"
            val host = address.substringBefore(':')
            val port = address.substringAfter(':').toIntOrNull() ?: return null
            return runCatching {
                val socket = Socket()
                socket.connect(java.net.InetSocketAddress(host, port), CONNECT_TIMEOUT_MS)
                socket.soTimeout = CONNECT_TIMEOUT_MS
                val reader = BufferedReader(InputStreamReader(socket.getInputStream()))
                val writer = OutputStreamWriter(socket.getOutputStream())
                val console = EmulatorConsole(socket, reader, writer)
                console.send("auth $token")
                console
            }.getOrNull()
        }
    }
}
