package cli

import (
	"encoding/json"
	"fmt"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/ext"
)

type maintainReport struct {
	DryRun             bool     `json:"dry_run"`
	Problems           []string `json:"problems"`
	Warnings           []string `json:"warnings"`
	Compacted          int      `json:"compacted"`
	CompactCandidates  int      `json:"compact_candidates"`
	Archived           int      `json:"archived"`
	ArchiveCandidates  int      `json:"archive_candidates"`
	PromoteCandidates  int      `json:"promote_candidates"`
	ExtVaults          int      `json:"ext_vaults"`
	ExtUpdated         int      `json:"ext_updated"`
}

func cmdMaintain(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	dry := fs.Bool("dry-run", false, "จำลองรอบ sleep-time consolidation โดยไม่แก้ไฟล์")
	asJSON := fs.Bool("json", false, "พิมพ์สรุปเป็น JSON")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}

	probs, warns, verr := domain.Verify(root)
	if verr != nil {
		return e.fail(verr)
	}
	compRes, cerr := domain.Compact(root, domain.CompactOptions{Days: 7, MinGroup: 3, DryRun: *dry})
	if cerr != nil {
		return e.fail(cerr)
	}
	archRes, aerr := domain.Archive(root, domain.ArchiveOptions{Threshold: 0.15, DryRun: *dry})
	if aerr != nil {
		return e.fail(aerr)
	}
	promRes, perr := domain.Promote(root, domain.PromoteOptions{MinUses: 3, DryRun: true})
	if perr != nil {
		return e.fail(perr)
	}

	var extRes ext.ReindexResult
	if !*dry {
		if rerr := domain.ReindexRoot(root); rerr != nil {
			return e.fail(rerr)
		}
		if cfg, lerr := ext.LoadConfig(root); lerr == nil && len(cfg.Vaults) > 0 {
			extRes, _ = ext.Reindex(root, "")
		}
	}

	rep := maintainReport{
		DryRun:            *dry,
		Problems:          probs,
		Warnings:          warns,
		Compacted:         len(compRes.Written),
		CompactCandidates: len(compRes.Groups),
		Archived:          len(archRes.Moved),
		ArchiveCandidates: len(archRes.Candidates),
		PromoteCandidates: len(promRes.Candidates),
		ExtVaults:         extRes.Vaults,
		ExtUpdated:        extRes.Updated,
	}
	if rep.Problems == nil {
		rep.Problems = []string{}
	}
	if rep.Warnings == nil {
		rep.Warnings = []string{}
	}

	if *asJSON {
		enc := json.NewEncoder(e.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		mode := "apply"
		if *dry {
			mode = "dry-run"
		}
		fmt.Fprintf(e.Stdout, "maintain (%s): problems=%d warnings=%d · compact=%d/%d · archive=%d/%d · promote-candidates=%d · ext-updated=%d\n",
			mode, len(rep.Problems), len(rep.Warnings),
			rep.Compacted, rep.CompactCandidates,
			rep.Archived, rep.ArchiveCandidates,
			rep.PromoteCandidates, rep.ExtUpdated)
	}
	if len(rep.Problems) > 0 {
		return ExitError
	}
	return ExitOK
}
