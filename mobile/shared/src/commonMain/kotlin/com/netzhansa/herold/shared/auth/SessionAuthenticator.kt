package com.netzhansa.herold.shared.auth

import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** Raised when a request needs a bearer token and the session no longer has one. */
class SessionExpiredException(message: String) : Exception(message)

/**
 * What the transport asks for a bearer token. Keeping the JMAP client
 * behind this seam is what lets one refresh serve every caller: the
 * client knows only "give me a token" and "that token was refused".
 */
interface TokenProvider {
    /** A token believed valid, refreshed first if it is about to expire. */
    suspend fun accessToken(): String

    /**
     * A replacement for [staleToken], which the server just refused with
     * a 401. Null when the session is gone and the user must sign in
     * again. A concurrent caller that already refreshed gets the token
     * that caller obtained, without a second round trip.
     */
    suspend fun refreshAfterUnauthorized(staleToken: String): String?
}

/** A [TokenProvider] over a plain stored token; it has nothing to refresh with. */
class StoredTokenProvider(private val store: TokenStore) : TokenProvider {
    override suspend fun accessToken(): String =
        store.currentToken() ?: throw SessionExpiredException("no bearer token stored")

    override suspend fun refreshAfterUnauthorized(staleToken: String): String? = null
}

/**
 * Gates releasing the tokens to the network layer behind the device's
 * biometric / device-credential unlock (REQ-AND-AUTH-11). The default
 * gate is open: unlock is opt-in.
 */
interface UnlockGate {
    /** True when the tokens may be used right now. */
    fun isUnlocked(): Boolean

    /** Suspends until the user has unlocked. */
    suspend fun awaitUnlocked()

    companion object {
        val OPEN: UnlockGate = object : UnlockGate {
            override fun isUnlocked(): Boolean = true

            override suspend fun awaitUnlocked() = Unit
        }
    }
}

/**
 * The one place a bearer token comes from (REQ-AND-AUTH-03/04). It
 * refreshes ahead of the announced expiry, refreshes once on a 401, and
 * collapses concurrent refreshes into a single token request so a sync
 * pass, an outbox drain and an EventSource reconnect racing each other
 * do not burn three refresh tokens against a rotating family.
 *
 * A refused refresh - the family revoked, the token reused, the grant
 * expired - clears the credential and reports the session lost, which
 * is what puts the shell back on the sign-in screen (REQ-AND-AUTH-20).
 * A refresh that could not be delivered at all leaves the credential
 * alone: being offline is not being signed out.
 */
class SessionAuthenticator(
    private val store: TokenStore,
    private val oauth: OAuthClient,
    private val baseUrl: String,
    private val now: () -> Long,
    private val unlockGate: UnlockGate = UnlockGate.OPEN,
    private val onSessionLost: suspend () -> Unit = {},
) : TokenProvider {

    private val refreshMutex = Mutex()

    override suspend fun accessToken(): String {
        unlockGate.awaitUnlocked()
        val held = store.tokens() ?: throw SessionExpiredException("no bearer token stored")
        if (held.refreshToken == null || !held.expiresWithin(now(), EXPIRY_SKEW_MILLIS)) {
            return held.accessToken
        }
        val refreshed = try {
            refresh(held.accessToken)
        } catch (undeliverable: SessionExpiredException) {
            // The refresh could not be delivered. The held token is
            // inside its skew window, not yet expired, so the request
            // goes out with it and the 401 path decides.
            return held.accessToken
        }
        return refreshed ?: throw SessionExpiredException("the session ended; sign in again")
    }

    override suspend fun refreshAfterUnauthorized(staleToken: String): String? {
        unlockGate.awaitUnlocked()
        return refresh(staleToken)
    }

    /**
     * One refresh at a time. A caller whose [staleToken] is no longer
     * what is stored arrived behind a refresh that already happened and
     * takes its result.
     */
    private suspend fun refresh(staleToken: String): String? = refreshMutex.withLock {
        val held = store.tokens() ?: return null
        if (held.accessToken != staleToken) return held.accessToken
        val refreshToken = held.refreshToken
        if (refreshToken == null) {
            // A credential with nothing to refresh with cannot come
            // back from a 401; the user signs in again.
            loseSession()
            return null
        }
        when (val outcome = oauth.refresh(baseUrl, refreshToken, now())) {
            is TokenExchange.Success -> {
                // The server rotates the refresh token; a response that
                // omitted one would leave the family unusable, so the
                // previous one is kept in that case.
                val refreshed = outcome.tokens.copy(
                    refreshToken = outcome.tokens.refreshToken ?: refreshToken,
                )
                store.store(refreshed)
                refreshed.accessToken
            }

            is TokenExchange.Rejected -> {
                loseSession()
                null
            }

            is TokenExchange.Transport -> throw SessionExpiredException(
                "the session could not be refreshed: ${outcome.message}",
            )
        }
    }

    private suspend fun loseSession() {
        store.clear()
        onSessionLost()
    }

    private companion object {
        /** Refresh this far ahead of the announced expiry, covering clock skew and flight time. */
        const val EXPIRY_SKEW_MILLIS = 60_000L
    }
}
