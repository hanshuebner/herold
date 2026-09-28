package com.netzhansa.herold.android.push

import com.netzhansa.herold.shared.jmap.PushTransport
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * What Automatic resolves to (issue #499): a release APK built without a
 * Firebase project must not swap a Play-Services device onto UnifiedPush,
 * because that device would use FCM the moment the build offered it, and a
 * silent UnifiedPush registration hides that the build shipped without
 * push.
 */
class PushTransportResolutionTest {

    @Test
    fun `automatic prefers fcm whenever it is available`() {
        assertEquals(
            PushTransport.FCM,
            resolveTransport(
                choice = PushTransportChoice.AUTOMATIC,
                fcmAvailable = true,
                playServicesAvailable = true,
                unifiedPushAvailable = true,
            ),
        )
    }

    @Test
    fun `automatic never selects unifiedpush on a play-services device`() {
        // The build has no Firebase project (fcmAvailable is false) but the
        // device carries Play Services and a distributor is installed -
        // exactly the 0.11.0 release-APK combination from #499.
        assertNull(
            resolveTransport(
                choice = PushTransportChoice.AUTOMATIC,
                fcmAvailable = false,
                playServicesAvailable = true,
                unifiedPushAvailable = true,
            ),
        )
    }

    @Test
    fun `automatic falls through to unifiedpush without play services`() {
        assertEquals(
            PushTransport.UNIFIED_PUSH,
            resolveTransport(
                choice = PushTransportChoice.AUTOMATIC,
                fcmAvailable = false,
                playServicesAvailable = false,
                unifiedPushAvailable = true,
            ),
        )
    }

    @Test
    fun `automatic is unavailable with no transport at all`() {
        assertNull(
            resolveTransport(
                choice = PushTransportChoice.AUTOMATIC,
                fcmAvailable = false,
                playServicesAvailable = false,
                unifiedPushAvailable = false,
            ),
        )
    }

    @Test
    fun `an explicit unifiedpush choice is honoured even with play services present`() {
        assertEquals(
            PushTransport.UNIFIED_PUSH,
            resolveTransport(
                choice = PushTransportChoice.UNIFIED_PUSH,
                fcmAvailable = false,
                playServicesAvailable = true,
                unifiedPushAvailable = true,
            ),
        )
    }

    @Test
    fun `an explicit fcm choice fails without fcm available`() {
        assertNull(
            resolveTransport(
                choice = PushTransportChoice.FCM,
                fcmAvailable = false,
                playServicesAvailable = true,
                unifiedPushAvailable = true,
            ),
        )
    }
}
