//go:build spike && sqlite_modernc

package storesqlite

import _ "modernc.org/sqlite"

const (
	driverName = "sqlite"
	driverTag  = "modernc.org/sqlite"
)

func driverDSN(path string) string {
	return path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(30000)"
}
