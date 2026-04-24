// Command herold-dns-route53 is a first-party herold plugin: ACME DNS-01 solver and record publisher via AWS Route53.
//
// Actual implementation lands in Phase 1 or 2 (see docs/implementation/02-phasing.md).
// Phase 0 keeps a stub entrypoint so the plugin binary compiles.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "herold-dns-route53: not yet implemented")
	os.Exit(1)
}
