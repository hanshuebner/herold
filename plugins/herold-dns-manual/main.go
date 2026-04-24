// Command herold-dns-manual is a first-party herold plugin: ACME DNS-01 solver that emits records for the operator to publish manually, with a webhook-style confirmation path.
//
// Actual implementation lands in Phase 1 or 2 (see docs/implementation/02-phasing.md).
// Phase 0 keeps a stub entrypoint so the plugin binary compiles.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "herold-dns-manual: not yet implemented")
	os.Exit(1)
}
