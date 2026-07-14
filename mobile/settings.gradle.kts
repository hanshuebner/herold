// Root Gradle build for the herold Android client. This is a self-contained
// Gradle project rooted at mobile/ -- it is not part of the Go module tree
// and go build ./... never traverses it (see docs/design/android/
// implementation-plan.md, "Monorepo integration").
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "herold-mobile"

include(":shared")
include(":androidApp")
