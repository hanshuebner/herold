// Command herold-echo is a first-party herold plugin: trivial echo plugin used by the SDK test suite to exercise handshake, configure, health, and shutdown end-to-end.
//
// Actual implementation lands in Phase 1 or 2 (see docs/implementation/02-phasing.md).
// Phase 0 keeps a stub entrypoint so the plugin binary compiles.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "herold-echo: not yet implemented")
	os.Exit(1)
}
