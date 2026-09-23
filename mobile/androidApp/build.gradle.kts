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
//      apiKey, projectNumber, and the optional packageName the project is
//      registered for.
//   2. ~/.config/herold-android/google-services.json - the file as downloaded
//      from the Firebase console; the same five values are read out of it.
//      Override the location with $HEROLD_GOOGLE_SERVICES_JSON.
//
// A Firebase app is registered for one specific Android package; FCM does
// not deliver to a different one. The debug build type carries its own
// applicationId (issue #484), so its config is scoped by packageName
// (FirebaseConfig.scopedTo below) and runs with push off, logging why,
// until a debug entry is registered and recorded.
// ---------------------------------------------------------------------------

data class FirebaseConfig(
    val projectId: String,
    val applicationId: String,
    val apiKey: String,
    val projectNumber: String,
    // The Android package this project is registered for in the Firebase
    // console. Blank in older/hand-written firebase.properties files, which
    // predate this field and were always written for the release id
    // (issue #484): scopedTo() below treats a blank packageName as an
    // implicit claim on the release id, not a wildcard for every id.
    val packageName: String = "",
) {
    val configured: Boolean get() = applicationId.isNotBlank() && apiKey.isNotBlank()

    /**
     * This config as it applies to one build's actual Android package:
     * unchanged when [packageName] names that package, or names none and
     * [targetApplicationId] is the release id (the historical assumption);
     * [FirebaseConfig.EMPTY] otherwise, so a build under a package the
     * Firebase project does not know about runs with push off rather than
     * registering a token FCM can never deliver to.
     */
    fun scopedTo(targetApplicationId: String, releaseApplicationId: String): FirebaseConfig {
        if (!configured) return this
        val matches = if (packageName.isBlank()) {
            targetApplicationId == releaseApplicationId
        } else {
            packageName == targetApplicationId
        }
        if (matches) return this
        val recordedFor = packageName.ifBlank { "$releaseApplicationId (assumed; no packageName recorded)" }
        println(
            "herold: Firebase config is registered for $recordedFor, not $targetApplicationId; " +
                "push stays off for this build. Add \"packageName=$targetApplicationId\" to " +
                "firebase.properties once that package has its own Firebase app.",
        )
        return EMPTY
    }

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
        packageName = props.getProperty("packageName", ""),
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
        packageName = field("package_name"),
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

// The commit the build came from, so a bug report names the tree it was
// built from and not only its version (issue #407). CI passes it in the
// environment; a local build reads it from git and an export has none.
fun buildCommit(): String {
    System.getenv("HEROLD_BUILD_COMMIT")?.takeIf { it.isNotBlank() }?.let { return it.trim() }
    System.getenv("GITHUB_SHA")?.takeIf { it.isNotBlank() }?.let { return it.trim().take(8) }
    return runCatching {
        val process = ProcessBuilder("git", "rev-parse", "--short=8", "HEAD")
            .directory(rootDir).redirectErrorStream(false).start()
        val out = process.inputStream.bufferedReader().readText().trim()
        process.waitFor()
        if (process.exitValue() == 0) out else ""
    }.getOrDefault("")
}

val buildCommit: String = buildCommit()


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

// ---------------------------------------------------------------------------
// Acceptance TLS fixture (issue #361).
//
// REQ-UNS-20's one-click unsubscribe only applies to an `https:` URL, so the
// instrumented check runs its recording sink over TLS. keytool generates a
// self-signed pair into the build directory on first configuration: the
// certificate becomes a debug-only raw resource the network security config
// trusts under `debug-overrides`, the keystore an androidTest asset the sink
// serves with. Android ignores `debug-overrides` in a non-debuggable build and
// neither half is committed, so a shipped APK trusts nothing extra.
// ---------------------------------------------------------------------------

val acceptanceTlsDir: File = File(layout.buildDirectory.get().asFile, "generated/acceptanceTls")
val acceptanceTlsResDir: File = File(acceptanceTlsDir, "res")
val acceptanceTlsAssetsDir: File = File(acceptanceTlsDir, "assets")

/** The password protecting the generated fixture; it guards nothing real. */
val acceptanceTlsPassword = "acceptance"

fun generateAcceptanceTls() {
    val keystore = File(acceptanceTlsAssetsDir, "acceptance-sink.p12")
    val certificate = File(acceptanceTlsResDir, "raw/acceptance_sink.pem")
    if (keystore.isFile && certificate.isFile) return
    keystore.parentFile.mkdirs()
    certificate.parentFile.mkdirs()
    keystore.delete()
    certificate.delete()
    val keytool = File(File(System.getProperty("java.home"), "bin"), "keytool").absolutePath
    fun run(vararg args: String) {
        val process = ProcessBuilder(listOf(keytool) + args)
            .redirectErrorStream(true)
            .start()
        val output = process.inputStream.bufferedReader().readText()
        check(process.waitFor() == 0) { "keytool failed: $output" }
    }
    run(
        "-genkeypair", "-alias", "sink", "-keyalg", "RSA", "-keysize", "2048",
        "-validity", "3650", "-dname", "CN=herold-acceptance-sink",
        "-ext", "san=ip:127.0.0.1,dns:localhost",
        "-storetype", "PKCS12", "-keystore", keystore.absolutePath,
        "-storepass", acceptanceTlsPassword, "-keypass", acceptanceTlsPassword,
    )
    run(
        "-exportcert", "-rfc", "-alias", "sink",
        "-keystore", keystore.absolutePath, "-storepass", acceptanceTlsPassword,
        "-file", certificate.absolutePath,
    )
}

generateAcceptanceTls()

// The signed release package, and the debug package derived from it. App
// Links verification (assetlinks.json, [server.ui] android_app_links) and
// the shortcuts.xml/strings.xml debug overlays name releaseApplicationId
// and debugApplicationId respectively; keeping both here gives the build
// script one place that defines what ".debug" means (issue #484).
val releaseApplicationId = "com.netzhansa.herold.android"
val debugApplicationId = "$releaseApplicationId.debug"

android {
    namespace = releaseApplicationId
    compileSdk = 36

    defaultConfig {
        applicationId = releaseApplicationId
        minSdk = 26
        targetSdk = 36
        versionCode = releaseVersionCode
        versionName = releaseVersionName
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"

        buildConfigField("String", "ACCEPTANCE_TLS_PASSWORD", "\"$acceptanceTlsPassword\"")
        buildConfigField("String", "GIT_COMMIT", "\"$buildCommit\"")
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

    sourceSets {
        getByName("debug") { res.srcDir(acceptanceTlsResDir) }
        getByName("androidTest") {
            assets.srcDir(acceptanceTlsAssetsDir)
            // The quoted-history shapes the instrumented spec delivers are
            // the same table the shared module's host-JVM check reads, so
            // the two cannot drift on what a shape is (issue #456).
            kotlin.srcDir("../shared/src/foldShapes/kotlin")
        }
    }

    buildTypes {
        debug {
            // A package of its own, so installing this build can never
            // replace or uninstall the signed release app on the same
            // device (issue #484). src/debug/res carries the matching
            // app_name and shortcuts.xml overrides.
            applicationIdSuffix = ".debug"
            val debugFirebase = firebaseConfig.scopedTo(debugApplicationId, releaseApplicationId)
            buildConfigField("String", "FIREBASE_PROJECT_ID", "\"${debugFirebase.projectId}\"")
            buildConfigField("String", "FIREBASE_APPLICATION_ID", "\"${debugFirebase.applicationId}\"")
            buildConfigField("String", "FIREBASE_API_KEY", "\"${debugFirebase.apiKey}\"")
            buildConfigField("String", "FIREBASE_PROJECT_NUMBER", "\"${debugFirebase.projectNumber}\"")
        }
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release")
            val releaseFirebase = firebaseConfig.scopedTo(releaseApplicationId, releaseApplicationId)
            buildConfigField("String", "FIREBASE_PROJECT_ID", "\"${releaseFirebase.projectId}\"")
            buildConfigField("String", "FIREBASE_APPLICATION_ID", "\"${releaseFirebase.applicationId}\"")
            buildConfigField("String", "FIREBASE_API_KEY", "\"${releaseFirebase.apiKey}\"")
            buildConfigField("String", "FIREBASE_PROJECT_NUMBER", "\"${releaseFirebase.projectNumber}\"")
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
        println("commit: ${buildCommit.ifBlank { "(unknown)" }}")
        println("release signing: ${if (signed) "configured" else "absent"}")
        println("firebase: ${if (firebase) "configured" else "absent"}")
    }
}

// ---------------------------------------------------------------------------
// Device safety guard (issue #484).
//
// A connected-test or install task installs on every device `adb` lists and,
// on cleanup, uninstalls the app and test packages from each -- with no
// `ANDROID_SERIAL` set to scope it, that fan-out reached the maintainer's
// Pixel alongside a local emulator and took the signed release app down with
// it. This task runs before every such task and refuses when more than one
// device is attached, or when any attached device is not an emulator,
// unless the caller opts in with -Pherold.allowDevices=true.
// ---------------------------------------------------------------------------

val checkAttachedDevices = tasks.register("checkAttachedDevices") {
    group = "verification"
    description = "Fails when more than one device, or a non-emulator device, is attached (issue #484)."
    doLast {
        val allowDevices = (findProperty("herold.allowDevices") as String?)?.toBoolean() ?: false
        val process = ProcessBuilder("adb", "devices", "-l")
            .redirectErrorStream(true)
            .start()
        val output = process.inputStream.bufferedReader().readText()
        val exitCode = process.waitFor()
        check(exitCode == 0) { "adb devices -l failed (exit $exitCode):\n$output" }
        // adb prints optional daemon-startup chatter before the marker line;
        // only what follows it is device listing.
        val deviceLines = output.lineSequence()
            .dropWhile { !it.startsWith("List of devices attached") }
            .drop(1)
            .map { it.trim() }
            .filter { it.isNotEmpty() }
            .toList()
        if (deviceLines.isEmpty()) return@doLast
        val nonEmulator = deviceLines.filterNot { it.substringBefore(' ').startsWith("emulator-") }
        if (allowDevices) return@doLast
        if (deviceLines.size > 1 || nonEmulator.isNotEmpty()) {
            throw GradleException(
                buildString {
                    appendLine(
                        "Refusing to install or run instrumented tests: this task installs " +
                            "on every attached device and its cleanup can uninstall an " +
                            "unrelated app from a real phone (issue #484).",
                    )
                    appendLine("Attached devices (adb devices -l):")
                    deviceLines.forEach { appendLine("  $it") }
                    append(
                        "Attach only the one emulator under test (set ANDROID_SERIAL to its " +
                            "serial), or pass -Pherold.allowDevices=true to override.",
                    )
                },
            )
        }
    }
}

tasks.configureEach {
    if (name != checkAttachedDevices.name && (name.startsWith("connected") || name.startsWith("install"))) {
        dependsOn(checkAttachedDevices)
    }
}

dependencies {
    implementation(project(":shared"))

    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.work.runtime.ktx)
    // The home-screen widget (REQ-AND-SYS-20).
    implementation(libs.androidx.glance.appwidget)
    implementation(libs.androidx.glance.material3)
    // The system browser the OAuth2 authorization-code flow runs in, and
    // the unlock prompt that gates releasing the token (REQ-AND-AUTH-01/11).
    implementation(libs.androidx.browser)
    implementation(libs.androidx.biometric)
    // The FragmentActivity the prompt is hosted in, at the version that
    // passes activity-result request codes through unchanged (issue #359).
    implementation(libs.androidx.fragment)
    implementation(libs.kotlinx.coroutines.core)
    // Needed on this module's own compile classpath because MainActivity
    // wires shared's HttpClient-typed factory directly into JmapClient
    // (shared's ktor-client-core dependency is `implementation`, so it is
    // not otherwise visible transitively to androidApp).
    implementation(libs.ktor.client.core)

    implementation(platform(libs.firebase.bom))
    implementation(libs.firebase.messaging)
    implementation(libs.unifiedpush.connector)

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
