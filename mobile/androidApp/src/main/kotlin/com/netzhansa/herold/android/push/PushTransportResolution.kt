package com.netzhansa.herold.android.push

import com.netzhansa.herold.shared.jmap.PushTransport

/**
 * What [choice] resolves to on this device, pulled out of [PushController]
 * so a host-JVM unit test can pin it without a `Context` or a
 * `PackageManager` in hand.
 *
 * A Play-Services device with no Firebase project in the build has
 * [fcmAvailable] false and [playServicesAvailable] true; Automatic reports
 * that combination as unavailable rather than falling through to
 * UnifiedPush, because a build shipped without push must say so instead of
 * quietly registering the device somewhere the server never delivers to
 * (re #499).
 */
internal fun resolveTransport(
    choice: PushTransportChoice,
    fcmAvailable: Boolean,
    playServicesAvailable: Boolean,
    unifiedPushAvailable: Boolean,
): PushTransport? = when (choice) {
    PushTransportChoice.FCM -> PushTransport.FCM.takeIf { fcmAvailable }
    PushTransportChoice.UNIFIED_PUSH -> PushTransport.UNIFIED_PUSH.takeIf { unifiedPushAvailable }
    PushTransportChoice.AUTOMATIC -> when {
        fcmAvailable -> PushTransport.FCM
        playServicesAvailable -> null
        unifiedPushAvailable -> PushTransport.UNIFIED_PUSH
        else -> null
    }
}
