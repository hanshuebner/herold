package com.netzhansa.herold.shared.auth

/**
 * Process-memory [TokenStore] for Phase 0's single foreground session
 * (used by the androidApp sign-in screen and by shared's own live-instance
 * test). It satisfies REQ-AND-AUTH-10's "never plaintext preferences or the
 * local database" constraint by never persisting the token at all -- it
 * lives only as long as the process does. A Keystore-backed durable actual
 * (EncryptedSharedPreferences, surviving process death and app restart) is
 * Phase 1 work, alongside the rest of the local store persistence
 * (docs/design/android/requirements/01-auth-and-token.md REQ-AND-AUTH-10).
 */
class InMemoryTokenStore : TokenStore {
    @Volatile
    private var token: String? = null

    override suspend fun currentToken(): String? = token

    override suspend fun store(token: String) {
        this.token = token
    }

    override suspend fun clear() {
        token = null
    }
}
