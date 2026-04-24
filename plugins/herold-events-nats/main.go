// Command herold-events-nats is a first-party herold plugin: event publisher plugin that forwards typed events to a NATS server.
//
// Actual implementation lands in Phase 1 or 2 (see docs/implementation/02-phasing.md).
// Phase 0 keeps a stub entrypoint so the plugin binary compiles.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "herold-events-nats: not yet implemented")
	os.Exit(1)
}
