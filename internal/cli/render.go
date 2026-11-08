// `mem render` — maintain the pointer/instruction block inside each agent's
// own files (Python cmd_render, REQ-DOM-01 render modes).
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atomsbaza/my-memory-engine/internal/domain"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// cmdRender writes the generated block for every agent whose mode is render or
// pointer, appends it when the file has no block yet, replaces an existing
// block in place, and reports a skip for pull/tool modes.
func cmdRender(e *Env, c command, argv []string) int {
	fs := e.flagSet(c)
	var only stringsFlag
	fs.Var(&only, "agent", "จำกัดเฉพาะ agent ที่ระบุ (ซ้ำได้)")
	dryRun := fs.Bool("dry-run", false, "แสดงสิ่งที่จะเขียน ไม่เขียนจริง")
	printBlock := fs.Bool("print", false, "พิมพ์ block ที่จะเขียนออก stdout (ไม่เขียน)")
	if _, code, stop := e.parseArgs(c, fs, argv); stop {
		return code
	}
	root, err := e.root()
	if err != nil {
		return e.fail(err)
	}
	recs, err := e.scan(root)
	if err != nil {
		return e.fail(err)
	}
	sch, err := e.schemaOf(root)
	if err != nil {
		return e.fail(err)
	}
	agents, err := domain.LoadAgents(root)
	if err != nil {
		return e.fail(err)
	}

	wanted := map[string]bool{}
	for _, name := range only {
		wanted[name] = true
	}

	// Plan first: several agents may point at the same file (symlinks), and
	// that file must be written exactly once.
	type job struct {
		agent domain.AgentDef
		path  string
	}
	var jobs []job
	for _, a := range agents {
		if len(wanted) > 0 && !wanted[a.Name] {
			continue
		}
		switch a.Mode {
		case "pull", "tool":
			why := "ดึงจากมันอย่างเดียว ไม่เขียนทับ"
			if a.Mode == "tool" {
				why = "ใช้ผ่าน MCP/hook เท่านั้น — ไม่มี instruction file"
			}
			fmt.Fprintf(e.Stdout, "skip %s (mode=%s — %s)\n", a.Name, a.Mode, why)
			continue
		}
		path, perr := store.ExpandPath(a.File)
		if perr != nil {
			return e.fail(perr)
		}
		if _, serr := os.Stat(path); serr != nil && !a.Create {
			fmt.Fprintf(e.Stdout, "skip %s — ไม่มีไฟล์ %s (ตั้ง create=true ถ้าต้องการให้สร้าง)\n", a.Name, path)
			continue
		}
		jobs = append(jobs, job{agent: a, path: path})
	}

	byReal := map[string][]job{}
	order := []string{}
	for _, j := range jobs {
		real := j.path
		if resolved, rerr := filepath.EvalSymlinks(j.path); rerr == nil {
			real = resolved
		} else if abs, aerr := filepath.Abs(j.path); aerr == nil {
			real = abs
		}
		if _, seen := byReal[real]; !seen {
			order = append(order, real)
		}
		byReal[real] = append(byReal[real], j)
	}
	sort.Strings(order)

	changed := 0
	for _, real := range order {
		group := byReal[real]
		agent := group[0].agent
		path := group[0].path
		if len(group) > 1 {
			names := make([]string, 0, len(group))
			for _, j := range group {
				names = append(names, j.agent.Name)
			}
			fmt.Fprintf(e.Stdout, "note %s ชี้ไฟล์เดียวกัน (%s) — เขียน block เดียว และใช้ --source agent:<ชื่อ agent> แทนการผูกกับตัวใดตัวหนึ่ง\n",
				strings.Join(names, ", "), path)
			agent.WriteCmd = "add --layer lesson --scope <scope> --source agent:<agent> --file -"
		}

		block := domain.RenderBlock(root, recs, agent, sch.Budgets["rules"])
		if *printBlock {
			fmt.Fprintf(e.Stdout, "# ===== %s -> %s (%s) =====\n%s", agent.Name, path, agent.Mode, block)
			continue
		}

		old := ""
		if data, rerr := os.ReadFile(path); rerr == nil {
			old = string(data)
		}
		marked := strings.Contains(old, domain.RenderBegin) && strings.Contains(old, domain.RenderEnd) &&
			strings.Index(old, domain.RenderBegin) < strings.Index(old, domain.RenderEnd)

		var next string
		switch {
		case agent.Mode == "pointer":
			next = block
		case marked:
			pre := strings.TrimRight(old[:strings.Index(old, domain.RenderBegin)], " \t\n")
			post := old[strings.Index(old, domain.RenderEnd)+len(domain.RenderEnd):]
			post = strings.TrimLeft(post, "\n")
			next = pre
			if pre != "" {
				next += "\n\n"
			}
			next += block
			if strings.TrimSpace(post) != "" {
				next += "\n" + post
			}
		default:
			next = ""
			if strings.TrimRight(old, " \t\n") != "" {
				next = strings.TrimRight(old, " \t\n") + "\n\n"
			}
			next += block
		}

		if next == old {
			fmt.Fprintf(e.Stdout, "ok   %s — ไม่เปลี่ยน\n", agent.Name)
			continue
		}
		if *dryRun {
			note := "ต่อท้ายไฟล์เดิม"
			if marked {
				note = "มีบล็อกเดิมอยู่แล้ว -> แทนที่"
			} else if old == "" {
				note = "ไฟล์ใหม่"
			}
			fmt.Fprintf(e.Stdout, "DRY  %s -> %s  %d -> %d chars (%+d, %s)\n",
				agent.Name, path, runeLen(old), runeLen(next), runeLen(next)-runeLen(old), note)
			changed++
			continue
		}
		if merr := os.MkdirAll(filepath.Dir(path), 0o755); merr != nil {
			return e.fail(merr)
		}
		if werr := os.WriteFile(path, []byte(next), 0o644); werr != nil {
			return e.fail(werr)
		}
		changed++
		fmt.Fprintf(e.Stdout, "wrote %s -> %s\n", agent.Name, path)
	}

	if changed > 0 {
		verb := "ถูกอัปเดต"
		if *dryRun {
			verb = "จะถูกอัปเดต (dry-run — ยังไม่เขียน)"
		}
		fmt.Fprintf(e.Stdout, "\n%d ไฟล์%s · ไฟล์เหล่านี้อยู่นอก repo นี้ — ตรวจด้วยตาเปล่าหรือ git ของ agent นั้น\n", changed, verb)
	} else if !*dryRun && !*printBlock {
		fmt.Fprintln(e.Stdout, "ไม่มีอะไรต้องอัปเดต")
	}
	return ExitOK
}
