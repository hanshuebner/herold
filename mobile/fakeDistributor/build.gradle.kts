// A UnifiedPush distributor for the acceptance run (issue #229). The
// emulator has Play Services and no distributor of its own, and a
// self-hosted ntfy would hand out an endpoint the host-side herold
// cannot reach; this one publishes its endpoint on a device port that
// `adb forward` exposes to the host, so a dev instance delivers to it
// over the same loopback allowlist the fake FCM uses.
//
// Test tooling: never shipped, never a dependency of :androidApp.
plugins {
    alias(libs.plugins.androidApplication)
    alias(libs.plugins.kotlinAndroid)
}

android {
    namespace = "com.netzhansa.herold.fakedistributor"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.netzhansa.herold.fakedistributor"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "1.0"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
}
