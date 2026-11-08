// Command mem-install wires harness CLIs (claude, codex, gemini, opencode,
// cursor, hermes, omp) to the engine's MCP server and session hooks. All
// logic lives in internal/harness; this entry point only owns the process.
package main

import (
	"os"

	"github.com/atomsbaza/my-memory-engine/internal/harness"
)

func main() {
	os.Exit(harness.Run(os.Args[1:], os.Stdout, os.Stderr))
}
