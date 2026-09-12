import java.security.KeyStore
import java.util.Base64
import java.util.Properties

// Native Jetpack Compose UI (docs/design/android/architecture/05-ui-shell.md).
// Owns all Android platform integration; reads the shared module's local
// store and drives its sync engine. The UI never calls JMAP directly.
plugins {
    alias(libs.plugins.androidApplication)
    alias(libs.plugins.kotlinAndroid)
    alias(libs.plugins.composeCompiler)
}

// ---------------------------------------------------------------------------
// Firebase project configuration (issue #328).
//
// The Firebase values are read at build time and initialised programmatically
// at runtime (HeroldApplication -> FirebaseSetup), so the google-services
// Gradle plugin is not applied and a checkout without credentials still
// builds: assembleDebug and the CI lane stay green, push simply stays off.
//
// Two sources, first match wins, both kept out of git:
//
//   1. mobile/androidApp/firebase.properties  - projectId, applicationId,
//      apiKey, projectNumber.
//   2. ~/.config/herold-android/google-services.json - the file as downloaded
//      from the Firebase console; the same four values are read out of it.
//      Override the location with $HEROLD_GOOGLE_SERVICES_JSON.
// ---------------------------------------------------------------------------

data class FirebaseConfig(
    val projectId: String,
    val applicationId: String,
    val apiKey: String,
    val projectNumber: String,
) {
    val configured: Boolean get() = applicationId.isNotBlank() && apiKey.isNotBlank()

    companion object {
        val EMPTY = FirebaseConfig("", "", "", "")
    }
}

fun readFirebaseProperties(file: File): FirebaseConfig? {
    if (!file.isFile) return null
    val props = Properties().apply { file.inputStream().use { load(it) } }
    return FirebaseConfig(
        projectId = props.getProperty("projectId", ""),
        applicationId = props.getProperty("applicationId", ""),
        apiKey = props.getProperty("apiKey", ""),
        projectNumber = props.getProperty("projectNumber", ""),
    )
}

// Minimal reader for the console's google-services.json: the first client of
// the package we ship, plus the project-level ids.
fun readGoogleServicesJson(file: File): FirebaseConfig? {
    if (!file.isFile) return null
    val text = file.readText()
    fun field(name: String, from: String = text): String =
        Regex("\"$name\"\\s*:\\s*\"([^\"]*)\"").find(from)?.groupValues?.get(1).orEmpty()
    val client = Regex("\\{[^{}]*\"mobilesdk_app_id\"[^{}]*}").find(text)?.value.orEmpty()
    return FirebaseConfig(
        projectId = field("project_id"),
        applicationId = field("mobilesdk_app_id", client.ifBlank { text }),
        apiKey = field("current_key"),
        projectNumber = field("project_number"),
    )
}

val firebaseConfig: FirebaseConfig = run {
    val explicit = System.getenv("HEROLD_GOOGLE_SERVICES_JSON")?.let { File(it) }
    val candidates = listOfNotNull(
        explicit,
        File(rootDir, "androidApp/google-services.json"),
        File(System.getProperty("user.home"), ".config/herold-android/google-services.json"),
    )
    readFirebaseProperties(File(rootDir, "androidApp/firebase.properties"))
        ?: candidates.firstNotNullOfOrNull { readGoogleServicesJson(it) }
        ?: FirebaseConfig.EMPTY
}

// ---------------------------------------------------------------------------
// Version: an `android-vX.Y.Z` tag names the release. CI passes the tag it was
// triggered by; a local build reads the nearest such tag and falls back to
// 0.1.0 / 1 when there is none.
// ---------------------------------------------------------------------------

fun nearestAndroidTag(): String? {
    val fromEnv = System.getenv("ANDROID_VERSION_TAG")
        ?: System.getenv("GITHUB_REF_NAME")?.takeIf { it.startsWith("android-v") }
    if (fromEnv != null) return fromEnv
    return runCatching {
        val process = ProcessBuilder(
            "git", "describe", "--tags", "--match", "android-v*", "--abbrev=0",
        ).directory(rootDir).redirectErrorStream(false).start()
        val out = process.inputStream.bufferedReader().readText().trim()
        process.waitFor()
        if (process.exitValue() == 0) out.ifBlank { null } else null
    }.getOrNull()
}

val androidVersionTag: String? = nearestAndroidTag()
    ?.removePrefix("android-v")?.takeIf { it.isNotBlank() }

val releaseVersionName: String = androidVersionTag ?: "0.1.0"

// A monotonic code from the tag's semantic version: 1.4.2 -> 10402. An
// untagged build stays at 1, so installing one never outranks a release.
val releaseVersionCode: Int = androidVersionTag?.let { tag ->
    val parts = tag.substringBefore('-').split('.').mapNotNull { it.toIntOrNull() }
    if (parts.size < 3) null else parts[0] * 10000 + parts[1] * 100 + parts[2]
} ?: 1

// ---------------------------------------------------------------------------
// Release signing: the keystore and its passwords come from the environment
// (Forgejo repository secrets in CI, a sourced env file locally), never from
// the tree. ANDROID_KEYSTORE_B64 is decoded into the build directory;
// ANDROID_KEYSTORE_FILE points at one already on disk.
// ---------------------------------------------------------------------------

fun resolveKeystore(): File? {
    System.getenv("ANDROID_KEYSTORE_FILE")?.let { path ->
        val file = File(path)
        if (file.isFile) return file
    }
    val encoded = System.getenv("ANDROID_KEYSTORE_B64")?.trim().orEmpty()
    if (encoded.isBlank()) return null
    val target = File(layout.buildDirectory.get().asFile, "signing/release.keystore")
    target.parentFile.mkdirs()
    target.writeBytes(Base64.getMimeDecoder().decode(encoded))
    target.setReadable(false, false)
    target.setReadable(true, true)
    return target
}

val releaseKeystore: File? = resolveKeystore()

/**
 * The password that actually opens the signing key. A PKCS12 keystore
 * protects its entries with the store password, so a separately recorded
 * key password does not unlock them; probe the configured one and fall back
 * to the store password when it does not open the key.
 */
fun resolveKeyPassword(keystore: File, storePassword: String, alias: String, configured: String): String {
    fun opens(candidate: String): Boolean = runCatching {
        val store = KeyStore.getInstance(keystore, storePassword.toCharArray())
        store.getKey(alias, candidate.toCharArray()) != null
    }.getOrDefault(false)
    return when {
        configured.isNotBlank() && opens(configured) -> configured
        opens(storePassword) -> storePassword
        else -> configured
    }
}

android {
    namespace = "com.netzhansa.herold.android"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.netzhansa.herold.android"
        minSdk = 26
        targetSdk = 36
        versionCode = releaseVersionCode
        versionName = releaseVersionName
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"

        buildConfigField("String", "FIREBASE_PROJECT_ID", "\"${firebaseConfig.projectId}\"")
        buildConfigField("String", "FIREBASE_APPLICATION_ID", "\"${firebaseConfig.applicationId}\"")
        buildConfigField("String", "FIREBASE_API_KEY", "\"${firebaseConfig.apiKey}\"")
        buildConfigField("String", "FIREBASE_PROJECT_NUMBER", "\"${firebaseConfig.projectNumber}\"")
    }

    signingConfigs {
        val keystore = releaseKeystore
        if (keystore != null) {
            val store = System.getenv("ANDROID_KEYSTORE_PASSWORD").orEmpty()
            val alias = System.getenv("ANDROID_KEY_ALIAS").orEmpty()
            create("release") {
                storeFile = keystore
                storePassword = store
                keyAlias = alias
                keyPassword = resolveKeyPassword(
                    keystore,
                    store,
                    alias,
                    System.getenv("ANDROID_KEY_PASSWORD").orEmpty(),
                )
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release")
        }
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

// Reports what the build picked up, so a release run shows whether it is
// signed and whether push is wired without printing any secret.
tasks.register("printBuildFacts") {
    val signed = releaseKeystore != null
    val firebase = firebaseConfig.configured
    val version = "$releaseVersionName ($releaseVersionCode)"
    doLast {
        println("version: $version")
        println("release signing: ${if (signed) "configured" else "absent"}")
        println("firebase: ${if (firebase) "configured" else "absent"}")
    }
}

dependencies {
    implementation(project(":shared"))

    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.work.runtime.ktx)
    // The system browser the OAuth2 authorization-code flow runs in, and
    // the unlock prompt that gates releasing the token (REQ-AND-AUTH-01/11).
    implementation(libs.androidx.browser)
    implementation(libs.androidx.biometric)
    implementation(libs.kotlinx.coroutines.core)
    // Needed on this module's own compile classpath because MainActivity
    // wires shared's HttpClient-typed factory directly into JmapClient
    // (shared's ktor-client-core dependency is `implementation`, so it is
    // not otherwise visible transitively to androidApp).
    implementation(libs.ktor.client.core)

    implementation(platform(libs.firebase.bom))
    implementation(libs.firebase.messaging)

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.material.icons.extended)
    implementation(libs.androidx.compose.foundation)
    implementation(libs.androidx.navigation.compose)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.kotlinx.datetime)

    debugImplementation(libs.androidx.compose.ui.tooling)
    debugImplementation(libs.androidx.compose.ui.test.manifest)

    testImplementation(libs.kotlin.test)

    androidTestImplementation(platform(libs.androidx.compose.bom))
    androidTestImplementation(libs.androidx.test.ext.junit)
    androidTestImplementation(libs.androidx.espresso.core)
    // Stubs the system file picker so the attachment path runs without
    // driving the Storage Access Framework UI.
    androidTestImplementation(libs.androidx.espresso.intents)
    androidTestImplementation(libs.androidx.compose.ui.test.junit4)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.androidx.test.rules)
    androidTestImplementation(libs.androidx.test.uiautomator)
    androidTestImplementation(libs.androidx.work.testing)
    androidTestImplementation(platform(libs.firebase.bom))
    androidTestImplementation(libs.firebase.messaging)
    androidTestImplementation(libs.kotlinx.coroutines.core)
    androidTestImplementation(libs.ktor.client.core)
    androidTestImplementation(libs.ktor.client.okhttp)
    androidTestImplementation(libs.kotlinx.serialization.json)
}
