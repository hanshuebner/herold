package com.netzhansa.herold.shared.auth

/**
 * What the client holds after the authorization-code exchange: the
 * short-lived access token it puts in `Authorization: Bearer`, the
 * rotating refresh token that mints the next one, and the instant the
 * access token stops being accepted (REQ-AND-AUTH-02/04). A milestone-1
 * device token has no refresh token and no expiry.
 */
data class TokenSet(
    val accessToken: String,
    val refreshToken: String? = null,
    val expiresAtMillis: Long? = null,
) {
    /** True when [nowMillis] is within [skewMillis] of the expiry the server announced. */
    fun expiresWithin(nowMillis: Long, skewMillis: Long): Boolean {
        val expiry = expiresAtMillis ?: return false
        return nowMillis + skewMillis >= expiry
    }
}

/**
 * Secure-storage seam for the bearer credential
 * (docs/design/android/requirements/01-auth-and-token.md). Tokens never
 * reach the local SQLDelight store or a log; the androidMain actual is
 * backed by Android Keystore-encrypted storage. The commonMain interface
 * lets the sync engine and JMAP client depend on this without knowing the
 * platform storage mechanism.
 */
interface TokenStore {
    suspend fun tokens(): TokenSet?

    suspend fun store(tokens: TokenSet)

    suspend fun clear()

    /** The access token alone, which is all the transport needs. */
    suspend fun currentToken(): String? = tokens()?.accessToken

    /** Stores a bare bearer token: no refresh, no announced expiry. */
    suspend fun store(token: String) = store(TokenSet(token))
}

/**
 * Where the in-flight authorization request waits while the system
 * browser has the foreground. The PKCE verifier and the `state` must
 * survive the app process being killed behind the Custom Tab, so they
 * live in the same Keystore-backed storage as the tokens
 * (REQ-AND-AUTH-02/10).
 */
data class PendingAuthorization(
    val verifier: String,
    val state: String,
    val baseUrl: String,
    val redirectUri: String,
)

interface PendingAuthorizationStore {
    suspend fun savePending(pending: PendingAuthorization)

    suspend fun pending(): PendingAuthorization?

    suspend fun clearPending()
}
