package com.netzhansa.herold.shared.auth

/**
 * Process-memory [TokenStore], used by tests and by short-lived tooling
 * clients. It satisfies REQ-AND-AUTH-10's "never plaintext preferences or
 * the local database" constraint by never persisting anything -- the
 * tokens live only as long as the process does. The app itself uses the
 * Keystore-backed store.
 */
class InMemoryTokenStore : TokenStore, PendingAuthorizationStore {
    @Volatile
    private var held: TokenSet? = null

    @Volatile
    private var pendingAuth: PendingAuthorization? = null

    override suspend fun tokens(): TokenSet? = held

    override suspend fun store(tokens: TokenSet) {
        held = tokens
    }

    override suspend fun clear() {
        held = null
    }

    override suspend fun savePending(pending: PendingAuthorization) {
        pendingAuth = pending
    }

    override suspend fun pending(): PendingAuthorization? = pendingAuth

    override suspend fun clearPending() {
        pendingAuth = null
    }
}
