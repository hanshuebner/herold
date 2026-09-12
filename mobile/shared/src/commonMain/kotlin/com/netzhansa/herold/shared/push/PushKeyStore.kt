package com.netzhansa.herold.shared.push

/**
 * Where the subscription's RFC 8291 key pair and auth secret live
 * (REQ-AND-PUSH-04). They are credentials - anyone holding them can
 * decrypt what herold pushes to this device - so they go in the same
 * Keystore-backed storage as the bearer token and never into the local
 * database or a log. The keys outlive a single endpoint: a distributor
 * that rotates its endpoint re-registers the same key pair.
 */
interface PushKeyStore {
    suspend fun pushKeys(): WebPushKeys?

    suspend fun storePushKeys(keys: WebPushKeys)

    suspend fun clearPushKeys()

    /**
     * The endpoint the UnifiedPush distributor handed out. Anyone holding
     * it can push to this device, so it lives here rather than in ordinary
     * preferences; null once the distributor has taken it back.
     */
    suspend fun pushEndpoint(): String?

    suspend fun setPushEndpoint(endpoint: String?)

    /** The keys this install holds, minting and storing them on first use. */
    suspend fun pushKeysOrGenerate(): WebPushKeys =
        pushKeys() ?: WebPushKeys.generate().also { storePushKeys(it) }
}
