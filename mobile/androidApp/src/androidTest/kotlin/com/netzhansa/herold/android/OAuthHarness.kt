package com.netzhansa.herold.android

import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

/**
 * Drives herold's own `/oauth2/authorize` login page from the test
 * process, standing in for the system browser.
 *
 * The acceptance run exercises the real Custom Tab once, in
 * [OAuthAcceptanceTest]'s sign-in bullet. Every other bullet needs a
 * signed-in app rather than a browser, and driving Chrome four more
 * times is both slow and the flakiest part of the run - so those take
 * this path, which speaks to the same endpoints with the same PKCE
 * parameters the app minted and hands the app the same redirect URI
 * the browser would have.
 */
object OAuthHarness {

    /**
     * Signs in at [authorizeUrl] as [email] and returns the redirect
     * URI herold answers with, including the authorization code.
     */
    fun authorize(
        authorizeUrl: String,
        email: String,
        password: String,
        totpSecret: String?,
    ): String {
        val (html, cookie) = get(authorizeUrl)
        val req = field(html, "req") ?: error("the login page carried no req token")
        val csrf = field(html, "csrf") ?: error("the login page carried no csrf token")

        var form = mapOf("req" to req, "csrf" to csrf, "email" to email, "password" to password)
        var (status, body, location) = post(authorizeUrl, form, cookie)
        if (status == 200 && body.contains("name=\"totp_code\"")) {
            // The principal has TOTP enrolled: the page came back
            // asking for the second factor.
            val secret = totpSecret ?: error("the principal needs a TOTP code and the harness has no secret")
            form = form + ("totp_code" to Totp.code(secret))
            val second = post(authorizeUrl, form, cookie)
            status = second.first
            body = second.second
            location = second.third
        }
        check(status == 302) { "the authorize POST answered $status: ${body.take(300)}" }
        return location ?: error("the authorize POST carried no Location")
    }

    /** True when the page asked for a TOTP code, for a test that asserts it did. */
    fun requiresTotp(authorizeUrl: String, email: String, password: String): Boolean {
        val (html, cookie) = get(authorizeUrl)
        val req = field(html, "req") ?: return false
        val csrf = field(html, "csrf") ?: return false
        val (status, body, _) = post(
            authorizeUrl,
            mapOf("req" to req, "csrf" to csrf, "email" to email, "password" to password),
            cookie,
        )
        return status == 200 && body.contains("name=\"totp_code\"")
    }

    private fun get(url: String): Pair<String, String> {
        val connection = (URL(url).openConnection() as HttpURLConnection).apply {
            instanceFollowRedirects = false
            connectTimeout = 15_000
            readTimeout = 15_000
        }
        val body = connection.inputStream.bufferedReader().use { it.readText() }
        val cookie = connection.headerFields["Set-Cookie"]
            ?.firstOrNull { it.startsWith(CSRF_COOKIE) }
            ?.substringBefore(';')
            ?: error("the login page set no CSRF cookie")
        connection.disconnect()
        return body to cookie
    }

    private fun post(
        url: String,
        form: Map<String, String>,
        cookie: String,
    ): Triple<Int, String, String?> {
        val connection = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "POST"
            instanceFollowRedirects = false
            doOutput = true
            connectTimeout = 15_000
            readTimeout = 15_000
            setRequestProperty("Content-Type", "application/x-www-form-urlencoded")
            setRequestProperty("Cookie", cookie)
        }
        val encoded = form.entries.joinToString("&") { (key, value) ->
            "$key=${URLEncoder.encode(value, "UTF-8")}"
        }
        connection.outputStream.use { it.write(encoded.toByteArray()) }
        val status = connection.responseCode
        val body = (if (status >= 400) connection.errorStream else connection.inputStream)
            ?.bufferedReader()?.use { it.readText() }.orEmpty()
        val location = connection.getHeaderField("Location")
        connection.disconnect()
        return Triple(status, body, location)
    }

    private fun field(html: String, name: String): String? =
        Regex("name=\"$name\" value=\"([^\"]*)\"").find(html)?.groupValues?.get(1)
            ?.replace("&amp;", "&")

    private const val CSRF_COOKIE = "herold_oauth2_csrf="
}
