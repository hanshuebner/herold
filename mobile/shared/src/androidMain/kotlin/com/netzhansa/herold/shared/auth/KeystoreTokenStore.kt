package com.netzhansa.herold.shared.auth

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * The bearer credential in Android Keystore-backed encrypted storage
 * (REQ-AND-AUTH-10): access token, refresh token, announced expiry, and
 * the PKCE verifier of an authorization still in the browser. The key
 * protecting the preference file lives in the Keystore and never leaves
 * it; nothing here is written to plaintext preferences, to the local
 * database, or to a log.
 *
 * androidx.security-crypto carries a deprecation on EncryptedSharedPreferences;
 * it remains the Jetpack surface for Keystore-backed preference storage and is
 * what REQ-AND-AUTH-10 names. A move to a Keystore-wrapped DataStore is
 * tracked with the milestone 2 auth hardening.
 */
@Suppress("DEPRECATION")
class KeystoreTokenStore(
    context: Context,
    fileName: String = "herold-credentials",
) : TokenStore, PendingAuthorizationStore {

    private val appContext = context.applicationContext

    private val preferences: SharedPreferences by lazy {
        val masterKey = MasterKey.Builder(appContext)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        EncryptedSharedPreferences.create(
            appContext,
            fileName,
            masterKey,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    }

    override suspend fun tokens(): TokenSet? = withContext(Dispatchers.IO) {
        val access = preferences.getString(KEY_TOKEN, null) ?: return@withContext null
        val expiry = preferences.getLong(KEY_EXPIRES_AT, 0L)
        TokenSet(
            accessToken = access,
            refreshToken = preferences.getString(KEY_REFRESH_TOKEN, null),
            expiresAtMillis = expiry.takeIf { it > 0L },
        )
    }

    override suspend fun store(tokens: TokenSet) = withContext(Dispatchers.IO) {
        preferences.edit()
            .putString(KEY_TOKEN, tokens.accessToken)
            .apply {
                if (tokens.refreshToken != null) {
                    putString(KEY_REFRESH_TOKEN, tokens.refreshToken)
                } else {
                    remove(KEY_REFRESH_TOKEN)
                }
                if (tokens.expiresAtMillis != null) {
                    putLong(KEY_EXPIRES_AT, tokens.expiresAtMillis)
                } else {
                    remove(KEY_EXPIRES_AT)
                }
            }
            .commit()
        Unit
    }

    override suspend fun clear() = withContext(Dispatchers.IO) {
        preferences.edit()
            .remove(KEY_TOKEN)
            .remove(KEY_REFRESH_TOKEN)
            .remove(KEY_EXPIRES_AT)
            .remove(KEY_GRANT_ID)
            .commit()
        Unit
    }

    override suspend fun savePending(pending: PendingAuthorization) = withContext(Dispatchers.IO) {
        preferences.edit()
            .putString(KEY_PKCE_VERIFIER, pending.verifier)
            .putString(KEY_OAUTH_STATE, pending.state)
            .putString(KEY_PENDING_BASE_URL, pending.baseUrl)
            .putString(KEY_PENDING_REDIRECT, pending.redirectUri)
            .commit()
        Unit
    }

    override suspend fun pending(): PendingAuthorization? = withContext(Dispatchers.IO) {
        val verifier = preferences.getString(KEY_PKCE_VERIFIER, null) ?: return@withContext null
        val state = preferences.getString(KEY_OAUTH_STATE, null) ?: return@withContext null
        val baseUrl = preferences.getString(KEY_PENDING_BASE_URL, null) ?: return@withContext null
        val redirect = preferences.getString(KEY_PENDING_REDIRECT, null) ?: return@withContext null
        PendingAuthorization(verifier, state, baseUrl, redirect)
    }

    override suspend fun clearPending() = withContext(Dispatchers.IO) {
        preferences.edit()
            .remove(KEY_PKCE_VERIFIER)
            .remove(KEY_OAUTH_STATE)
            .remove(KEY_PENDING_BASE_URL)
            .remove(KEY_PENDING_REDIRECT)
            .commit()
        Unit
    }

    /** The base URL the token was minted against; not a credential, but it belongs with it. */
    suspend fun baseUrl(): String? = withContext(Dispatchers.IO) {
        preferences.getString(KEY_BASE_URL, null)
    }

    suspend fun setBaseUrl(baseUrl: String) = withContext(Dispatchers.IO) {
        preferences.edit().putString(KEY_BASE_URL, baseUrl).commit()
        Unit
    }

    /**
     * The id of this device's own OAuth2 grant in the account's
     * credentials list, so the sessions screen can mark it and sign-out
     * can revoke it (REQ-AND-AUTH-21/22). It is a family id, not a
     * credential: it survives every refresh-token rotation.
     */
    suspend fun grantId(): String? = withContext(Dispatchers.IO) {
        preferences.getString(KEY_GRANT_ID, null)
    }

    suspend fun setGrantId(id: String?) = withContext(Dispatchers.IO) {
        val editor = preferences.edit()
        if (id == null) editor.remove(KEY_GRANT_ID) else editor.putString(KEY_GRANT_ID, id)
        editor.commit()
        Unit
    }

    /** The principal the held tokens belong to, so a different one clears the local store. */
    suspend fun principal(): String? = withContext(Dispatchers.IO) {
        preferences.getString(KEY_PRINCIPAL, null)
    }

    suspend fun setPrincipal(email: String?) = withContext(Dispatchers.IO) {
        val editor = preferences.edit()
        if (email == null) editor.remove(KEY_PRINCIPAL) else editor.putString(KEY_PRINCIPAL, email)
        editor.commit()
        Unit
    }

    private companion object {
        const val KEY_TOKEN = "bearer_token"
        const val KEY_REFRESH_TOKEN = "refresh_token"
        const val KEY_EXPIRES_AT = "access_token_expires_at"
        const val KEY_BASE_URL = "base_url"
        const val KEY_PKCE_VERIFIER = "pkce_verifier"
        const val KEY_OAUTH_STATE = "oauth_state"
        const val KEY_PENDING_BASE_URL = "pending_base_url"
        const val KEY_PENDING_REDIRECT = "pending_redirect_uri"
        const val KEY_GRANT_ID = "grant_family_id"
        const val KEY_PRINCIPAL = "principal_email"
    }
}
