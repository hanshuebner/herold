// Command herold is the single-binary entrypoint: it runs the server and
// hosts the CLI subcommands. See cmd/herold/cli for subcommand wiring.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "herold: a subcommand is required (try: herold --help)")
		os.Exit(2)
	}
	// Actual command tree is wired up by ops-observability-implementor in
	// internal/admin. Phase 0 keeps this entrypoint minimal so scaffolding
	// can compile without dragging in subsystems that do not exist yet.
	fmt.Fprintln(os.Stderr, "herold: command tree not yet implemented")
	os.Exit(1)
}
