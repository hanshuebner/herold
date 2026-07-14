// Root build file. Declares plugin versions once via the version catalog
// (gradle/libs.versions.toml) and applies them false here so module builds
// only need `alias(libs.plugins.x)` without repeating a version.
plugins {
    alias(libs.plugins.kotlinMultiplatform) apply false
    alias(libs.plugins.kotlinAndroid) apply false
    alias(libs.plugins.kotlinSerialization) apply false
    alias(libs.plugins.composeCompiler) apply false
    alias(libs.plugins.androidApplication) apply false
    alias(libs.plugins.androidLibrary) apply false
    alias(libs.plugins.sqldelight) apply false
}
