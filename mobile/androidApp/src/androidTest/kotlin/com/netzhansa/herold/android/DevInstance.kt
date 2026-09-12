package com.netzhansa.herold.android

import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.AuthClient
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.createHttpClient
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.sync.toStoreRow
import java.io.BufferedReader
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.net.Socket

/**
 * The ephemeral herold the instrumented acceptance run drives
 * (`scripts/dev-instance.sh`). The harness passes its URL as an
 * instrumentation argument, so the same tests run against any instance:
 *
 *   ./gradlew :androidApp:connectedDebugAndroidTest \
 *     -Pandroid.testInstrumentationRunnerArguments.heroldBaseUrl=http://10.0.2.2:<port>
 *
 * From the emulator the host's loopback is 10.0.2.2.
 */
object DevInstance {
    private fun argument(name: String): String? =
        InstrumentationRegistry.getArguments().getString(name)?.takeIf { it.isNotBlank() }

    val baseUrl: String get() = argument("heroldBaseUrl") ?: "http://10.0.2.2:8080"
    val email: String get() = argument("heroldEmail") ?: "alice@example.local"
    val password: String get() = argument("heroldPassword") ?: "testpass123..."

    /** A principal with TOTP enrolled, used for the two-factor checks. */
    val totpEmail: String get() = argument("heroldTotpEmail") ?: "admin@example.local"

    /** The TOTP secret scripts/dev-instance.sh prints as ADMIN_TOTP_SECRET. */
    val totpSecret: String? get() = argument("heroldTotpSecret")

    /** The instance's SMTP listener, for delivering a message to a principal. */
    val smtpAddr: String get() = argument("heroldSmtpAddr") ?: "10.0.2.2:2525"

    /**
     * Delivers one message to [email] over the instance's SMTP listener, so
     * a test provisions the mail it needs instead of depending on what an
     * earlier run left behind. Returns the subject it used.
     */
    fun deliverMail(subject: String, from: String = "Bob Example <bob@example.local>", body: String): String {
        val (host, port) = smtpAddr.split(":")
        Socket(host, port.toInt()).use { socket ->
            socket.soTimeout = 20_000
            val reader = BufferedReader(InputStreamReader(socket.getInputStream()))
            val writer = OutputStreamWriter(socket.getOutputStream())
            fun expect(prefix: String) {
                var line = reader.readLine() ?: error("SMTP closed while waiting for $prefix")
                // Skip the multi-line continuations of an EHLO-style reply.
                while (line.length > 3 && line[3] == '-') {
                    line = reader.readLine() ?: error("SMTP closed mid-reply")
                }
                check(line.startsWith(prefix)) { "SMTP said \"$line\", expected $prefix" }
            }
            fun send(command: String, expected: String) {
                writer.write(command + "\r\n")
                writer.flush()
                expect(expected)
            }
            expect("220")
            send("HELO acceptance.test", "250")
            send("MAIL FROM:<${from.substringAfter('<').substringBefore('>')}>", "250")
            send("RCPT TO:<$email>", "250")
            send("DATA", "354")
            writer.write(
                "From: $from\r\nTo: $email\r\nSubject: $subject\r\n\r\n$body\r\n.\r\n",
            )
            writer.flush()
            expect("250")
            send("QUIT", "221")
        }
        return subject
    }

    /**
     * An independent JMAP client signed in as [email], for asserting server
     * state directly rather than trusting the screen.
     */
    suspend fun serverClient(): JmapClient {
        val httpClient = createHttpClient()
        val tokenStore = InMemoryTokenStore()
        val result = AuthClient(httpClient, tokenStore).signIn(baseUrl, email, password)
        check(result is SignInResult.Success) { "dev-instance sign-in failed: $result" }
        return JmapClient(httpClient, baseUrl, tokenStore)
    }

    /** The server's view of one message, by id. */
    suspend fun serverEmail(client: JmapClient, accountId: String, id: String): Email? =
        client.emailGet(accountId, listOf(id)).list.firstOrNull()?.toStoreRow(accountId)
}
