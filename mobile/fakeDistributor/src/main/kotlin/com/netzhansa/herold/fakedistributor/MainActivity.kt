package com.netzhansa.herold.fakedistributor

import android.app.Activity
import android.os.Bundle
import android.widget.TextView

/**
 * Brings the distributor to the foreground so its service may start, and
 * says what it is. The acceptance run launches it once before it drives
 * a registration.
 */
class MainActivity : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        DistributorService.start(this)
        setContentView(
            TextView(this).apply {
                text = "Fake UnifiedPush distributor listening on ${DistributorServer.PORT}"
                setPadding(48, 48, 48, 48)
            },
        )
    }
}
