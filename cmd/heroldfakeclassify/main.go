// Command heroldfakeclassify is the real out-of-process classifier plugin
// binary wrapping internal/testfakes/fakeclassify's deterministic rule
// table (re #364). Unlike the other dev-instance fakes (heroldfakesmtp,
// heroldfakefcm), this one is not started directly by
// scripts/dev-instance.sh: it is configured as a [[plugin]] block in the
// generated system.toml, and the herold server itself spawns it over
// stdio JSON-RPC the same way it spawns any first-party plugin.
package main

import (
	"fmt"
	"os"

	"github.com/hanshuebner/herold/internal/testfakes/fakeclassify"
	"github.com/hanshuebner/herold/plugins/sdk"
)

func main() {
	if err := sdk.Run(fakeclassify.Manifest(), fakeclassify.NewHandler()); err != nil {
		fmt.Fprintf(os.Stderr, "heroldfakeclassify: %v\n", err)
		os.Exit(1)
	}
}
