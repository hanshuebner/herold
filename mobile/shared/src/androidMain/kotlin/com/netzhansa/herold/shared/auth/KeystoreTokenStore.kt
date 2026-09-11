package com.netzhansa.herold.shared.auth

import android.content.Context
import android.content.SharedPreferences
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * The bearer token in Android Keystore-backed encrypted storage
 * (REQ-AND-AUTH-10). The key protecting the preference file lives in the
 * Keystore and never leaves it; the token is never written to plaintext
 * preferences, to the local database, or to a log.
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
) : TokenStore {

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

    override suspend fun currentToken(): String? = withContext(Dispatchers.IO) {
        preferences.getString(KEY_TOKEN, null)
    }

    override suspend fun store(token: String) = withContext(Dispatchers.IO) {
        preferences.edit().putString(KEY_TOKEN, token).commit()
        Unit
    }

    override suspend fun clear() = withContext(Dispatchers.IO) {
        preferences.edit().remove(KEY_TOKEN).commit()
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

    private companion object {
        const val KEY_TOKEN = "bearer_token"
        const val KEY_BASE_URL = "base_url"
    }
}
