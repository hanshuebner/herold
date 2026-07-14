package com.netzhansa.herold.shared.auth

/**
 * Secure-storage seam for the bearer token
 * (docs/design/android/requirements/01-auth-and-token.md). The token never
 * reaches the local SQLDelight store or logs; the androidMain actual is
 * backed by Android Keystore-encrypted storage. The commonMain interface
 * lets the sync engine and JMAP client depend on this without knowing the
 * platform storage mechanism.
 */
interface TokenStore {
    suspend fun currentToken(): String?

    suspend fun store(token: String)

    suspend fun clear()
}
