package com.netzhansa.herold.android

import android.content.Context
import com.google.firebase.messaging.RemoteMessage
import com.netzhansa.herold.android.push.HeroldMessagingService

/**
 * Hands a data-only message to the messaging service's handler the way the
 * Firebase SDK would. Google's delivery leg is the one part of the path a
 * test cannot drive; everything from `onMessageReceived` down is the code
 * the device runs.
 */
fun injectPush(context: Context, payload: String) {
    val service = object : HeroldMessagingService() {
        override fun getApplicationContext(): Context = context
    }
    service.onMessageReceived(
        RemoteMessage.Builder("herold@fcm.test").addData("payload", payload).build(),
    )
}
