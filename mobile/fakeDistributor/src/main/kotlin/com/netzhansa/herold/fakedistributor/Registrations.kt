package com.netzhansa.herold.fakedistributor

import android.content.Context

/**
 * Which app holds which instance token. A real distributor keeps the
 * same mapping; it is what lets a push name its recipient without the
 * sender knowing the app.
 */
class Registrations(context: Context) {

    private val prefs = context.getSharedPreferences("registrations", Context.MODE_PRIVATE)

    fun save(token: String, application: String) {
        prefs.edit().putString(token, application).apply()
    }

    fun byToken(token: String): String? = prefs.getString(token, null)

    fun forget(token: String) {
        prefs.edit().remove(token).apply()
    }

    /** The endpoint handed out for [token], as the host reaches it. */
    fun endpointFor(token: String): String = "http://127.0.0.1:${DistributorServer.PORT}/UP?token=$token"
}
