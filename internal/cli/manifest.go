// Generated command documentation. The doc body comes from the registry, so the
// documented surface cannot drift from the code (NFR-COMPAT-01, AC-P2-07):
// internal/cli/cli_test.go compares docs/cli-commands.md against
// renderManifestDoc().
package cli

import (
	"fmt"
	"strings"
)

// renderManifestDoc is the canonical body of docs/cli-commands.md.
func renderManifestDoc() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# mem command surface\n\n")
	fmt.Fprintf(&b, "%d commands: %d legacy Python families + `init` + `doctor` + `embed`.\n\n", CommandCount, LegacyCommandCount)
	fmt.Fprintf(&b, "Generated from `cli.Manifest()` — edit internal/cli/cli.go, not this file.\n\n")
	fmt.Fprintf(&b, "| command | synopsis | help | surface |\n")
	fmt.Fprintf(&b, "|---|---|---|---|\n")
	for _, c := range commands {
		surface := "engine"
		if c.Legacy {
			surface = "legacy"
		}
		synopsis := c.Args
		if synopsis == "" {
			synopsis = "—"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s |\n", c.Name, synopsis, c.Help, surface)
	}
	fmt.Fprintf(&b, "\nExit codes: `0` success · `1` failed check/generic failure · `2` unsafe input or usage error · `3` conflict (dedup block, supersede misuse, sync rebase conflict).\n")
	return b.String()
}
