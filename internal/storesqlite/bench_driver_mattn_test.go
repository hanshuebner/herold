//go:build spike && sqlite_mattn

package storesqlite

import _ "github.com/mattn/go-sqlite3"

const (
	driverName = "sqlite3"
	driverTag  = "github.com/mattn/go-sqlite3"
)

func driverDSN(path string) string {
	return "file:" + path + "?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=30000"
}
