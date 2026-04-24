// Command herold-spam-llm is a first-party herold plugin: spam classifier plugin that calls an OpenAI-compatible HTTP endpoint (defaults to a local Ollama).
//
// Actual implementation lands in Phase 1 or 2 (see docs/implementation/02-phasing.md).
// Phase 0 keeps a stub entrypoint so the plugin binary compiles.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "herold-spam-llm: not yet implemented")
	os.Exit(1)
}
