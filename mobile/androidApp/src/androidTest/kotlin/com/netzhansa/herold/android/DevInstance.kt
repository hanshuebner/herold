package com.netzhansa.herold.android

import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.AuthClient
import com.netzhansa.herold.shared.auth.InMemoryTokenStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.createHttpClient
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.Capability
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
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

    /**
     * The OAuth2 native client the instance registered
     * (`scripts/dev-instance.sh` prints it as OAUTH2_CLIENT_ID); the
     * app's own default, unless the harness overrides it.
     */
    val oauthClientId: String get() = argument("heroldOauthClientId") ?: "herold-android"

    /**
     * The in-tree fake FCM endpoint every dev instance runs
     * (`scripts/dev-instance.sh` prints it as FAKEFCM_HTTP_ADDR). Its
     * GET/DELETE /messages API is how a test reads the pushes herold sent.
     */
    val fakeFcmAddr: String get() = argument("heroldFakeFcmAddr") ?: "10.0.2.2:9099"

    /** The instance's SMTP listener, for delivering a message to a principal. */
    val smtpAddr: String get() = argument("heroldSmtpAddr") ?: "10.0.2.2:2525"

    /**
     * A key with the `bug-reports` scope (or admin), for reading back
     * what the reporter posted (issue #416). Mint one on the instance
     * with `herold api-key create <principal> --scope bug-reports` and
     * pass it as `heroldBugReportsKey`; without it the bug-report
     * checks skip, since the phone's own token cannot list reports.
     */
    val bugReportsKey: String? get() = argument("heroldBugReportsKey")

    /**
     * An HTML message carrying an inline PNG, for the reading pane's
     * `cid:` path. Delivered like any other mail, so the client sees the
     * same shape a real sender produces.
     */
    fun deliverMailWithInlineImage(
        subject: String,
        from: String = "Bob Example <bob@example.local>",
        cid: String = "inline-" + System.nanoTime() + "@acceptance.test",
        /** The HTML part, which references the image as `cid:<cid>`. */
        html: String = "<html><body><p>Inline image below.</p>" +
            "<p><img src=\"cid:$cid\" alt=\"dot\"></p></body></html>",
        to: String = email,
    ): String {
        val boundary = "herold-acceptance-" + System.nanoTime()
        val headers = "MIME-Version: 1.0\r\n" +
            "Content-Type: multipart/related; boundary=\"$boundary\"\r\n"
        val body = buildString {
            append("--$boundary\r\n")
            append("Content-Type: text/html; charset=utf-8\r\n\r\n")
            append(html)
            append("\r\n--$boundary\r\n")
            append("Content-Type: image/png\r\n")
            append("Content-Transfer-Encoding: base64\r\n")
            append("Content-ID: <$cid>\r\n")
            append("Content-Disposition: inline; filename=dot.png\r\n\r\n")
            append(INLINE_PNG_BASE64)
            append("\r\n--$boundary--\r\n")
        }
        return deliverRaw(subject, from, body, extraHeaders = headers, to = to)
    }

    /**
     * An HTML message carrying [bytes] as an image, inline or attached, so
     * the reading pane's handling of a camera-sized photo can be driven
     * from a seeded message (issue #341).
     */
    fun deliverMailWithImage(
        subject: String,
        bytes: ByteArray,
        name: String,
        inline: Boolean,
        from: String = "Bob Example <bob@example.local>",
    ): String {
        val cid = "image-" + System.nanoTime() + "@acceptance.test"
        val boundary = "herold-acceptance-" + System.nanoTime()
        val headers = "MIME-Version: 1.0\r\n" +
            "Content-Type: multipart/related; boundary=\"$boundary\"\r\n"
        val encoded = android.util.Base64.encodeToString(bytes, android.util.Base64.DEFAULT)
        val body = buildString {
            append("--$boundary\r\n")
            append("Content-Type: text/html; charset=utf-8\r\n\r\n")
            if (inline) {
                append("<html><body><p>Photo below.</p><p><img src=\"cid:$cid\" alt=\"photo\"></p></body></html>\r\n")
            } else {
                append("<html><body><p>Photo attached.</p></body></html>\r\n")
            }
            append("--$boundary\r\n")
            append("Content-Type: image/jpeg\r\n")
            append("Content-Transfer-Encoding: base64\r\n")
            if (inline) append("Content-ID: <$cid>\r\n")
            append("Content-Disposition: ${if (inline) "inline" else "attachment"}; filename=$name\r\n\r\n")
            append(encoded)
            append("\r\n--$boundary--\r\n")
        }
        return deliverRaw(subject, from, body, extraHeaders = headers)
    }

    /**
     * Delivers one message to [email] over the instance's SMTP listener, so
     * a test provisions the mail it needs instead of depending on what an
     * earlier run left behind. Returns the subject it used.
     */
    fun deliverMail(
        subject: String,
        from: String = "Bob Example <bob@example.local>",
        body: String,
        messageId: String = "acceptance-" + System.nanoTime() + "@acceptance.test",
        to: String = email,
        /** Extra header lines, each CRLF-terminated: List-Unsubscribe and friends. */
        headers: String = "",
    ): String = deliverRaw(subject, from, body, messageId, extraHeaders = headers, to = to)

    /** The SMTP conversation both seeding helpers share. */
    private fun deliverRaw(
        subject: String,
        from: String,
        body: String,
        messageId: String = "acceptance-" + System.nanoTime() + "@acceptance.test",
        extraHeaders: String = "",
        to: String = email,
    ): String {
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
            send("RCPT TO:<$to>", "250")
            send("DATA", "354")
            writer.write(
                "From: $from\r\nTo: $to\r\nSubject: $subject\r\n" +
                    "Message-ID: <$messageId>\r\n" + extraHeaders + "\r\n$body\r\n.\r\n",
            )
            writer.flush()
            expect("250")
            send("QUIT", "221")
        }
        return subject
    }

    /**
     * Waits until the instance has filed [subject] in [principal]'s inbox.
     * Ingest runs a body-metadata pass before a delivered message answers
     * an `Email/query`, so a client-side wait that starts at the SMTP 250
     * would be measuring that pass rather than the client's sync.
     */
    suspend fun awaitFiled(subject: String, principal: String = email) {
        val client = clientFor(principal)
        val accountId = client.session().mailAccountId!!
        val inbox = client.mailboxGet(accountId).list.first { it.role == "inbox" }.id
        repeat(FILING_POLLS) {
            val ids = client.emailQueryInbox(accountId, inbox, 20)
            val filed = client.emailGet(accountId, ids).list
                .map { it.toStoreRow(accountId) }
                .any { it.subject == subject }
            if (filed) return
            kotlinx.coroutines.delay(FILING_POLL_MS)
        }
        error("the instance never filed \"$subject\" for $principal")
    }

    /** How long the instance's ingest is given, and at what cadence. */
    private const val FILING_POLLS = 60
    private const val FILING_POLL_MS = 1_000L

    /** The principal a composed message is addressed to and read back from. */
    val recipientEmail: String get() = argument("heroldRecipient") ?: "bob@example.local"

    /** A 1x1 PNG, base64 as a MIME part carries it. */
    private const val INLINE_PNG_BASE64 =
        "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAIAQMAAAD+wSzIAAAABlBMVEX///+/v7+jQ3Y5AAAADklEQVQI12P4AIX8EAgALgAD/aNpbtEAAAAASUVORK5CYII="

    /**
     * An independent JMAP client signed in as [email], for asserting server
     * state directly rather than trusting the screen.
     */
    suspend fun serverClient(): JmapClient = clientFor(email)

    /**
     * A bearer token of a second session for [principal], for a test
     * that has to act as another signed-in client of the same account.
     */
    suspend fun deviceToken(principal: String): String {
        val tokenStore = InMemoryTokenStore()
        val result = AuthClient(createHttpClient(), tokenStore).signIn(
            baseUrl = baseUrl,
            email = principal,
            password = password,
            totpCode = totpSecret?.let { Totp.code(it) },
            deviceLabel = "acceptance second session",
        )
        check(result is SignInResult.Success) { "second-session sign-in as $principal failed: $result" }
        return result.token
    }

    /** The same, signed in as the recipient, for reading a sent message back. */
    suspend fun recipientClient(): JmapClient = clientFor(recipientEmail)

    private suspend fun clientFor(principal: String): JmapClient {
        val httpClient = createHttpClient()
        val tokenStore = InMemoryTokenStore()
        val result = AuthClient(httpClient, tokenStore).signIn(baseUrl, principal, password)
        check(result is SignInResult.Success) { "dev-instance sign-in as $principal failed: $result" }
        return JmapClient(httpClient, baseUrl, tokenStore)
    }

    /**
     * Gives [email] a sub-account to hold, for the checks that read
     * across accounts (issue #343). `scripts/dev-instance.sh
     * HEROLD_DEV_SUB_ACCOUNTS=1` seeds a separable identity, and the
     * session advertises a second account once that identity has been
     * separated - so a test provisions it here the way the push suites
     * provision their own mail, rather than a run depending on a
     * hand-issued JMAP call.
     *
     * Returns the mail accounts the session advertises afterwards.
     */
    suspend fun ensureSubAccount(): List<String> {
        val client = serverClient()
        val held = client.session().mailAccountIds()
        if (held.size >= 2) return held
        val accountId = client.session().mailAccountId
            ?: error("the session for $email carries no mail account")
        val separable = client.identityGet(accountId).list.firstOrNull { it.mayDelete && !it.isDefault }
            ?: error(
                "$email holds no separable identity; start the instance with " +
                    "HEROLD_DEV_SUB_ACCOUNTS=1 so one is seeded",
            )
        val args = buildJsonObject {
            put("accountId", accountId)
            putJsonObject("update") {
                putJsonObject(separable.id) { put("separated", true) }
            }
        }
        val result = client.batch(
            listOf(JmapClient.MethodCall("Identity/set", args, "c0")),
            listOf(Capability.CORE, Capability.MAIL, Capability.SUBMISSION, Capability.SUB_ACCOUNTS),
        ).single().args
        result["notUpdated"]?.jsonObject?.get(separable.id)?.let { refusal ->
            error("separating ${separable.email} was refused: $refusal")
        }
        // The identity moves to its sub-account as the call returns; the
        // session descriptor is re-read until it carries the new account.
        repeat(SEPARATION_POLLS) {
            val accounts = client.refreshSession().mailAccountIds()
            if (accounts.size >= 2) return accounts
            kotlinx.coroutines.delay(SEPARATION_POLL_MS)
        }
        error("the session never advertised a sub-account for ${separable.email}")
    }

    /** How long the separation is given to show up in the session. */
    private const val SEPARATION_POLLS = 30
    private const val SEPARATION_POLL_MS = 500L

    /** The server's view of one message, by id. */
    suspend fun serverEmail(client: JmapClient, accountId: String, id: String): Email? =
        client.emailGet(accountId, listOf(id)).list.firstOrNull()?.toStoreRow(accountId)
}
