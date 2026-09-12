// Pure-Kotlin core: JMAP client, sync engine, local store, domain models,
// auth/token logic (docs/design/android/architecture/01-system-overview.md).
// No UI. androidMain carries platform actuals (Keystore, connectivity,
// clock); iosMain is added when the iOS app starts.
plugins {
    alias(libs.plugins.kotlinMultiplatform)
    alias(libs.plugins.androidLibrary)
    alias(libs.plugins.kotlinSerialization)
    alias(libs.plugins.sqldelight)
}

kotlin {
    androidTarget {
        compilerOptions {
            jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
        }
    }

    sourceSets {
        commonMain.dependencies {
            implementation(libs.kotlinx.coroutines.core)
            implementation(libs.kotlinx.serialization.json)
            api(libs.kotlinx.datetime)
            api(libs.sqldelight.coroutines.extensions)
            implementation(libs.ktor.client.core)
            implementation(libs.ktor.client.content.negotiation)
            implementation(libs.ktor.serialization.kotlinx.json)
        }
        commonTest.dependencies {
            implementation(libs.kotlin.test)
            implementation(libs.kotlinx.coroutines.core)
            implementation(libs.kotlinx.coroutines.test)
            // Drives the OAuth2 token endpoint's client half without a
            // server: the auth tests assert what goes on the wire.
            implementation(libs.ktor.client.mock)
            implementation(libs.ktor.client.core)
        }
        androidMain.dependencies {
            implementation(libs.ktor.client.okhttp)
            implementation(libs.sqldelight.android.driver)
            implementation(libs.androidx.security.crypto)
            implementation(libs.kotlinx.coroutines.android)
        }
    }
}

android {
    namespace = "com.netzhansa.herold.shared"
    compileSdk = 36

    defaultConfig {
        minSdk = 26
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

sqldelight {
    databases {
        create("HeroldDatabase") {
            packageName.set("com.netzhansa.herold.shared.store")
            // The generated schema file is the baseline later milestones
            // migrate from: a schema change adds <version>.sqm next to the
            // .sq files and bumps this version, and verifyMigrations
            // replays them against the recorded schema at build time.
            schemaOutputDirectory.set(file("src/commonMain/sqldelight/databases"))
            verifyMigrations.set(true)
        }
    }
}
