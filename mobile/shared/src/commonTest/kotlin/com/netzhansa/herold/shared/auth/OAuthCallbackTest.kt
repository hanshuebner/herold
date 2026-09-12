package com.netzhansa.herold.shared.auth

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs

/**
 * What the browser hands back on the app's private-use scheme. The
 * parser has to hold for a URI shape a generic http parser rejects
 * (`com.netzhansa.herold:/oauth2/callback?...`, RFC 8252 section 7.1).
 */
class OAuthCallbackTest {

    private val redirect = OAuthClientConfig.DEFAULT_REDIRECT_URI

    @Test
    fun aCodeAndStateAreRead() {
        val callback = OAuthClient.parseCallback("$redirect?code=abc123&state=xyz", redirect)
        assertEquals(OAuthCallback.Code("abc123", "xyz"), callback)
    }

    @Test
    fun percentEncodingIsUndone() {
        val callback = OAuthClient.parseCallback("$redirect?code=a%2Fb%2Bc&state=s%3D1", redirect)
        assertEquals(OAuthCallback.Code("a/b+c", "s=1"), callback)
    }

    @Test
    fun anErrorRedirectIsRecognised() {
        val callback = OAuthClient.parseCallback(
            "$redirect?error=access_denied&error_description=user%20said%20no&state=xyz",
            redirect,
        )
        assertEquals(OAuthCallback.Denied("access_denied", "user said no", "xyz"), callback)
    }

    @Test
    fun anotherAppsUriIsNotACallback() {
        assertIs<OAuthCallback.NotACallback>(
            OAuthClient.parseCallback("com.example.other:/oauth2/callback?code=abc", redirect),
        )
        assertIs<OAuthCallback.NotACallback>(
            OAuthClient.parseCallback("$redirect?state=xyz", redirect),
        )
        assertIs<OAuthCallback.NotACallback>(OAuthClient.parseCallback(redirect, redirect))
    }

    @Test
    fun theAuthorizeUrlCarriesTheChallengeAndNotTheVerifier() {
        val pending = PendingAuthorization(
            verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
            state = "st4te",
            baseUrl = "https://mail.example.test/",
            redirectUri = redirect,
        )
        val url = OAuthClient(FakeHttp.client { respondJson("{}") }).authorizeUrl(pending)
        assertEquals(true, url.startsWith("https://mail.example.test/oauth2/authorize?"))
        assertEquals(true, url.contains("response_type=code"))
        assertEquals(true, url.contains("client_id=herold-android"))
        assertEquals(true, url.contains("code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"))
        assertEquals(true, url.contains("code_challenge_method=S256"))
        assertEquals(true, url.contains("redirect_uri=com.netzhansa.herold%3A%2Foauth2%2Fcallback"))
        assertEquals(false, url.contains(pending.verifier))
    }
}
