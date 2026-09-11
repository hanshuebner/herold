package com.netzhansa.herold.shared.store

import app.cash.sqldelight.db.SqlDriver

/**
 * Platform seam for the SQLDelight driver (Android SQLite now, SQLite via
 * the native driver when iOS lands). The schema's own `Callback` runs the
 * generated migrations, so a schema change ships as a numbered `.sqm` file
 * and upgrades in place.
 */
expect class DatabaseDriverFactory {
    fun createDriver(): SqlDriver
}

/** Opens the local store's database on the platform driver. */
fun createDatabase(factory: DatabaseDriverFactory): HeroldDatabase =
    HeroldDatabase(factory.createDriver())
