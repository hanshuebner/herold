package com.netzhansa.herold.shared.auth

/**
 * PKCE (RFC 7636) for the authorization-code grant. herold requires the
 * `S256` method: the authorize request carries
 * `code_challenge=base64url(sha256(verifier))` and the token request
 * carries the verifier itself, which the server re-hashes
 * (internal/directory/oauth2.go; REQ-AND-AUTH-02). A verifier never
 * leaves the device except in the token request over TLS, and lives in
 * Keystore-backed storage while the browser holds the foreground.
 */
object Pkce {

    /** The length RFC 7636 section 4.1 calls out for a 256-bit verifier. */
    const val VERIFIER_BYTES: Int = 32

    /** A fresh verifier from [bytes] of entropy; 32 bytes encode to 43 characters. */
    fun verifier(bytes: ByteArray): String {
        require(bytes.size >= VERIFIER_BYTES) {
            "a PKCE verifier needs at least $VERIFIER_BYTES bytes of entropy"
        }
        return Base64Url.encode(bytes)
    }

    /** The `S256` challenge for [verifier]. */
    fun challenge(verifier: String): String =
        Base64Url.encode(Sha256.digest(verifier.encodeToByteArray()))

    const val CHALLENGE_METHOD: String = "S256"
}

/** An opaque `state` value, correlating the authorize request with its callback. */
object OAuthState {
    fun of(bytes: ByteArray): String = Base64Url.encode(bytes)
}
