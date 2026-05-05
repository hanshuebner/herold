package load

import "runtime"

// runtime_SetBlockProfileRate is a thin wrapper so harness.go
// can call it without importing "runtime" only for this one function.
// Using the stdlib runtime package directly keeps CGO_ENABLED=0.
func runtime_SetBlockProfileRate(rate int) {
	runtime.SetBlockProfileRate(rate)
}
