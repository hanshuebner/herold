package com.netzhansa.herold.android.diag

import android.content.Context
import android.hardware.Sensor
import android.hardware.SensorEvent
import android.hardware.SensorEventListener
import android.hardware.SensorManager
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.netzhansa.herold.shared.diag.ShakeDetector

/**
 * Opens the report sheet when the phone is shaken (REQ-AND-SYS-51).
 *
 * The listener is registered only while the shell is resumed, so a
 * phone shaken in a pocket with the app in the background costs nothing
 * and reports nothing; [enabled] is the settings toggle, and switching
 * it off unregisters the sensor rather than ignoring its samples.
 */
@Composable
fun ShakeToReport(enabled: Boolean, onShake: () -> Unit) {
    val context = LocalContext.current
    val detector = remember { ShakeDetector() }
    val latest by rememberUpdatedState(onShake)

    LifecycleResumeEffect(enabled) {
        val manager = if (enabled) {
            context.getSystemService(Context.SENSOR_SERVICE) as? SensorManager
        } else {
            null
        }
        val sensor = manager?.getDefaultSensor(Sensor.TYPE_ACCELEROMETER)
        val listener = object : SensorEventListener {
            override fun onSensorChanged(event: SensorEvent) {
                if (event.values.size < 3) return
                val shaken = detector.onSample(
                    event.values[0].toDouble(),
                    event.values[1].toDouble(),
                    event.values[2].toDouble(),
                    System.currentTimeMillis(),
                )
                if (shaken) {
                    DiagLog.i(TAG, "shake detected; opening the report sheet")
                    latest()
                }
            }

            override fun onAccuracyChanged(sensor: Sensor?, accuracy: Int) = Unit
        }
        detector.reset()
        if (sensor != null) {
            manager.registerListener(listener, sensor, SensorManager.SENSOR_DELAY_GAME)
        }
        onPauseOrDispose {
            if (sensor != null) manager.unregisterListener(listener)
        }
    }
}

private const val TAG = "herold.shake"
