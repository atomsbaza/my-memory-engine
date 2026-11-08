// Command mem is the single binary of the modular monolith (design §6):
// every subcommand routes through internal/cli, except `mem mcp`, which is
// short-circuited here so the MCP stdio server owns os.Stdin/os.Stdout
// from the first instruction — no CLI banner or usage text can pollute
// the JSON-RPC stream (stdout purity, design §6 "mcp" row).
package main

import (
	"context"
	"os"

	"github.com/atomsbaza/my-memory-engine/internal/cli"
	"github.com/atomsbaza/my-memory-engine/internal/mcp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := mcp.RunStdio(context.Background()); err != nil {
			os.Stderr.WriteString("mem: mcp server: " + err.Error() + "\n")
			os.Exit(cli.ExitError)
		}
		return
	}
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
