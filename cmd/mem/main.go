// Command mem is the single binary of the modular monolith (design §6):
// every subcommand routes through internal/cli, including `mem mcp`, whose
// handler resolves the home and the harness identity before handing the
// process to the MCP stdio server (internal/mcp) on os.Stdin/os.Stdout.
//
// Routing `mcp` through the CLI router — instead of short-circuiting it here —
// is what keeps stdout clean end to end: cli.Main writes nothing to stdout for
// a successful `mem mcp`, so every byte on the stream is the SDK's JSON-RPC
// framing (stdout purity, design §6 "mcp" row), while `--home`/`--agent` and
// $MEM_HOME resolve exactly as they do for every other subcommand.
package main

import (
	"os"

	"github.com/atomsbaza/my-memory-engine/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
