package com.netzhansa.herold.shared.auth

/** The outcome of finishing an authorization the browser handed back. */
sealed interface OAuthSignInResult {
    data class Success(val tokens: TokenSet, val baseUrl: String) : OAuthSignInResult

    /** The authorization did not complete; the message is shown on the sign-in screen. */
    data class Failed(val message: String) : OAuthSignInResult
}

/**
 * Drives the two halves of the authorization-code flow that the app
 * itself owns (REQ-AND-AUTH-01/02): minting the authorization request
 * before the Custom Tab opens, and finishing it when the browser
 * redirects back. The browser in between is the platform's, not this
 * class's.
 *
 * The verifier and `state` are written to Keystore-backed storage
 * before the browser is launched, so an app process killed behind the
 * Custom Tab still completes the exchange when the callback brings it
 * back.
 */
class OAuthSignIn(
    private val oauth: OAuthClient,
    private val tokens: TokenStore,
    private val pending: PendingAuthorizationStore,
    private val config: OAuthClientConfig = OAuthClientConfig.DEFAULT,
    private val now: () -> Long,
    private val randomBytes: (Int) -> ByteArray = ::secureRandomBytes,
) {

    /** Mints an authorization request against [baseUrl] and returns the URL to open. */
    suspend fun begin(baseUrl: String): String {
        val request = PendingAuthorization(
            verifier = Pkce.verifier(randomBytes(Pkce.VERIFIER_BYTES)),
            state = OAuthState.of(randomBytes(STATE_BYTES)),
            baseUrl = baseUrl.trimEnd('/'),
            redirectUri = config.redirectUri,
        )
        pending.savePending(request)
        return oauth.authorizeUrl(request)
    }

    /** True while a Custom Tab is expected to come back with a code. */
    suspend fun inProgress(): Boolean = pending.pending() != null

    /** Abandons an authorization the user backed out of. */
    suspend fun abandon() = pending.clearPending()

    /**
     * Finishes the authorization [callbackUri] carries: checks the
     * `state` against the one this app issued, exchanges the code with
     * the verifier, and stores the token pair.
     */
    suspend fun complete(callbackUri: String): OAuthSignInResult {
        val request = pending.pending()
            ?: return OAuthSignInResult.Failed("no sign-in was in progress")
        return when (val callback = OAuthClient.parseCallback(callbackUri, request.redirectUri)) {
            is OAuthCallback.NotACallback ->
                OAuthSignInResult.Failed("the browser came back without an authorization code")

            is OAuthCallback.Denied -> {
                pending.clearPending()
                OAuthSignInResult.Failed(callback.description ?: callback.error)
            }

            is OAuthCallback.Code -> {
                if (callback.state != request.state) {
                    // A code arriving under a state this app did not
                    // issue is not this app's authorization.
                    pending.clearPending()
                    return OAuthSignInResult.Failed("the sign-in response did not match the request")
                }
                when (
                    val exchange = oauth.exchangeCode(
                        baseUrl = request.baseUrl,
                        code = callback.code,
                        verifier = request.verifier,
                        redirectUri = request.redirectUri,
                        nowMillis = now(),
                    )
                ) {
                    is TokenExchange.Success -> {
                        tokens.store(exchange.tokens)
                        pending.clearPending()
                        OAuthSignInResult.Success(exchange.tokens, request.baseUrl)
                    }

                    is TokenExchange.Rejected -> {
                        pending.clearPending()
                        OAuthSignInResult.Failed(exchange.description ?: exchange.error)
                    }

                    // Retryable: the request is left pending so the user
                    // can complete it without signing in again.
                    is TokenExchange.Transport -> OAuthSignInResult.Failed(exchange.message)
                }
            }
        }
    }

    /** The base URL the pending authorization was started against. */
    suspend fun pendingBaseUrl(): String? = pending.pending()?.baseUrl

    private companion object {
        const val STATE_BYTES = 16
    }
}
