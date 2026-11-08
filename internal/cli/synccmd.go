// `mem sync` — refresh the derived index, commit locally, then fetch →
// pull --rebase --autostash → push against the home's own upstream. The git
// mechanics live in internal/sync; this handler owns the index refresh, the
// Python output contract and the exit-code mapping.
package cli

import (
	"fmt"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/sync"
)

// cmdSync runs the sync pipeline (Python cmd_sync, REQ-SYNC-01/02).
func cmdSync(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	message := fs.String("message", "", "commit message")
	fs.StringVar(message, "m", "", "commit message (ย่อ)")
	noPush := fs.Bool("no-push", false, "commit local เท่านั้น ไม่ push")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}

	// The index is derived, local and gitignored; refresh it before the commit
	// so a broken rebuild is visible instead of silently shipped.
	if err := domain.ReindexRoot(root); err != nil {
		return e.fail(err)
	}

	res, runErr := sync.Run(root, sync.Options{
		Message: *message,
		NoPush:  *noPush,
		Log:     e.Stderr,
	})
	if runErr != nil {
		// Partial outcome first: the caller must see what did happen before
		// the failure (NFR-OPS-01).
		e.reportSync(res)
		return e.fail(runErr)
	}
	e.reportSync(res)
	return ExitOK
}

// reportSync prints the human summary of a sync run.
func (e *Env) reportSync(res sync.Result) {
	if res.Committed {
		fmt.Fprintf(e.Stdout, "committed %d files\n", res.CommittedFiles)
	} else {
		fmt.Fprintln(e.Stdout, "ไม่มีอะไรเปลี่ยน")
	}
	for _, note := range res.Notes {
		fmt.Fprintf(e.Stdout, "%s\n", note)
	}
	if res.Pushed {
		fmt.Fprintln(e.Stdout, "pushed")
	}
}
