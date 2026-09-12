package com.netzhansa.herold.shared.auth

/**
 * Cryptographically strong random bytes for the PKCE verifier and the
 * OAuth2 `state`. The platform's CSPRNG fills it (`java.security.SecureRandom`
 * on Android); commonMain only declares the need.
 */
expect fun secureRandomBytes(count: Int): ByteArray
