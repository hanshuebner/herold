package com.netzhansa.herold.shared.store

import android.content.Context
import app.cash.sqldelight.db.SqlDriver
import app.cash.sqldelight.driver.android.AndroidSqliteDriver

/**
 * Android driver over a file-backed SQLite database. `AndroidSqliteDriver`'s
 * callback applies the generated migrations on open, so an app update with a
 * newer schema version upgrades the user's store rather than losing it.
 */
actual class DatabaseDriverFactory(
    private val context: Context,
    private val databaseName: String = "herold.db",
) {
    actual fun createDriver(): SqlDriver = AndroidSqliteDriver(
        schema = HeroldDatabase.Schema,
        context = context,
        name = databaseName,
        callback = AndroidSqliteDriver.Callback(HeroldDatabase.Schema),
    )
}
