// Command hookbench is the measurement core of tools/benchmark-hook.sh
// (plan P2.7, gate G6): it runs one `mem hook` process per sample, records the
// latency and exit code of every run, and decides the p50 <= 150 ms gate.
//
// It deliberately measures the real process — not an in-process call — because
// the hook budget covers process start, record scan, selection and render. The
// corpus is staged by the caller into a throwaway directory; this command never
// writes to it (the hook itself touches last_used and the recall log there).
//
// The artifact carries the five fields every gate artifact needs (AGENTS.md):
// the executed command, the real exit code, the version/SHA of the binary, the
// date, and the probe result with a PASS/FAIL status.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/index"
	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// gateID is the design gate this benchmark closes (design §9.4).
const gateID = "G6"

// minRecords is the design's own stress trigger: at 400 records the full
// rebuild becomes a reopen trigger, so a smaller corpus cannot prove G6.
const minRecords = 400

// injectionEvents are the two events a harness waits on inside the user's
// turn. `stop` is excluded on purpose: it reindexes the whole corpus and
// performs a local git commit, so it is a write path with its own budget, not
// the per-prompt latency this gate bounds.
var injectionEvents = []string{"session-start", "user-prompt"}

// payloads mirrors the shape each event receives from a claude-style harness.
var payloads = map[string]string{
	"session-start": `{"session_id":"g6-bench","source":"benchmark"}`,
	"user-prompt":   `{"session_id":"g6-bench","prompt":"verify cache index latency gate"}`,
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		bin         = flag.String("bin", "", "path to the mem binary under test (required)")
		home        = flag.String("home", "", "staged memory home to benchmark (required)")
		corpus      = flag.String("corpus", "", "corpus root the staged home came from (required)")
		command     = flag.String("command", "", "gate command line recorded in the artifact (required)")
		eventsFlag  = flag.String("events", strings.Join(injectionEvents, ","), "comma-separated hook events to measure")
		runs        = flag.Int("runs", 30, "timed samples per event")
		warmup      = flag.Int("warmup", 5, "untimed warm-up runs per event")
		thresholdMs = flag.Float64("threshold-ms", 150, "G6 p50 ceiling in milliseconds")
		delayMs     = flag.Int("delay-ms", 0, "blocking delay injected per run (negative control; 0 = off)")
		out         = flag.String("out", "", "artifact path; empty means print only (the gate never passes without an artifact)")
	)
	flag.Parse()

	if *bin == "" || *home == "" || *corpus == "" || *command == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "hookbench: -bin, -home, -corpus, -command and -out are required")
		return 2
	}
	if *runs < 1 || *warmup < 0 || *thresholdMs <= 0 || *delayMs < 0 {
		fmt.Fprintln(os.Stderr, "hookbench: -runs >= 1, -warmup >= 0, -threshold-ms > 0, -delay-ms >= 0 required")
		return 2
	}
	events, err := parseEvents(*eventsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hookbench: %v\n", err)
		return 2
	}

	corpusInfo, err := inspectCorpus(*corpus, *home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hookbench: %v\n", err)
		return 2
	}
	machine, err := inspectMachine(*bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hookbench: %v\n", err)
		return 2
	}

	results := make([]eventResult, 0, len(events))
	for _, ev := range events {
		res, rerr := measure(*bin, *home, ev, *runs, *warmup, *delayMs)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "hookbench: %s: %v\n", ev, rerr)
			return 2
		}
		results = append(results, res)
	}

	headline, failed := decide(results, *thresholdMs)
	status, exitCode := "PASS", 0
	if failed {
		status, exitCode = "FAIL", 1
	}

	art := artifact{
		Gate:        gateID,
		Status:      status,
		ExitCode:    exitCode,
		Date:        time.Now().UTC().Format(time.RFC3339),
		Command:     *command,
		ThresholdMs: *thresholdMs,
		P50Ms:       headline,
		GateBasis:   "worst per-event p50 (stop excluded: write path)",
		DelayMs:     *delayMs,
		Config: runConfig{
			Events: events,
			Runs:   *runs,
			Warmup: *warmup,
			Agent:  "claude",
		},
		Events:  results,
		Corpus:  corpusInfo,
		Profile: machine,
	}

	printReport(art)
	if err := writeArtifact(*out, art); err != nil {
		fmt.Fprintf(os.Stderr, "hookbench: %v\n", err)
		return 2
	}
	fmt.Printf("artifact: %s\n", *out)
	return exitCode
}

// parseEvents validates the requested events against the injection set.
func parseEvents(given string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, 2)
	for _, raw := range strings.Split(given, ",") {
		ev := strings.TrimSpace(raw)
		if ev == "" {
			continue
		}
		known := false
		for _, e := range injectionEvents {
			if e == ev {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("event %q is not measured: only %s are injection events (stop reindexes and commits)", ev, strings.Join(injectionEvents, ", "))
		}
		if !seen[ev] {
			seen[ev] = true
			out = append(out, ev)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no event selected")
	}
	return out, nil
}

// inspectCorpus re-checks the provenance boundary and counts what will be
// scanned. The shell already refuses an unlabeled corpus; failing here too
// keeps the artifact honest even when the helper is invoked directly.
func inspectCorpus(corpus, home string) (corpusInfo, error) {
	info := corpusInfo{Path: corpus, Kind: "synthetic reference corpus", MinRecords: minRecords, ByLayer: map[string]int{}}
	provPath := filepath.Join(corpus, "PROVENANCE.md")
	raw, err := os.ReadFile(provPath)
	if err != nil {
		return info, fmt.Errorf("corpus %s has no PROVENANCE.md: %w", corpus, err)
	}
	if !strings.Contains(string(raw), "provenance: synthetic") {
		return info, fmt.Errorf("corpus %s is not labeled synthetic (PROVENANCE.md must say \"provenance: synthetic\")", corpus)
	}
	info.Provenance = "synthetic"
	info.ProvenanceFile = filepath.ToSlash(filepath.Join(corpus, "PROVENANCE.md"))

	rep, err := store.ScanWithReport(home)
	if err != nil {
		return info, fmt.Errorf("scan staged corpus %s: %w", home, err)
	}
	if len(rep.Broken) > 0 {
		return info, fmt.Errorf("staged corpus %s has %d broken records; first: %s: %s", home, len(rep.Broken), rep.Broken[0].Rel, rep.Broken[0].Problem)
	}
	info.Records = len(rep.Records)
	for _, rec := range rep.Records {
		info.ByLayer[rec.Layer]++
	}
	if info.Records < minRecords {
		return info, fmt.Errorf("staged corpus %s has %d records; G6 needs >= %d (the design's incremental-index trigger)", home, info.Records, minRecords)
	}
	if _, err := os.Stat(index.IndexPath(home)); err != nil {
		return info, fmt.Errorf("staged corpus %s has no prebuilt index: %w (run `mem reindex` before measuring)", home, err)
	}
	return info, nil
}

// inspectMachine records the fixed machine/runtime profile the gate ran under,
// plus the SHA-256 of the binary under test.
func inspectMachine(bin string) (machineInfo, error) {
	sum, err := fileSHA256(bin)
	if err != nil {
		return machineInfo{}, fmt.Errorf("hash %s: %w", bin, err)
	}
	return machineInfo{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		OSVersion:   osVersion(),
		CPU:         cpuModel(),
		CPUs:        runtime.NumCPU(),
		GoMaxProcs:  runtime.GOMAXPROCS(0),
		Go:          runtime.Version(),
		BinarySHA:   sum,
		BinaryBytes: fileSize(bin),
	}, nil
}

// measure runs one event warm-up + sample sequence and summarises it.
func measure(bin, home, event string, runs, warmup, delayMs int) (eventResult, error) {
	res := eventResult{Event: event, Runs: runs, Warmup: warmup, ExitCodes: map[int]int{}}
	payload, ok := payloads[event]
	if !ok {
		return res, fmt.Errorf("no payload defined for event %q", event)
	}
	for i := range warmup {
		_, code, _, err := runOnce(bin, home, event, payload, delayMs)
		if err != nil || code != 0 {
			return res, fmt.Errorf("warm-up run %d failed (exit %d): %v", i+1, code, err)
		}
	}
	raw := make([]float64, 0, runs)
	for i := range runs {
		ms, code, stderr, err := runOnce(bin, home, event, payload, delayMs)
		if err != nil {
			return res, fmt.Errorf("run %d: %w", i+1, err)
		}
		res.ExitCodes[code]++
		if code != 0 && res.Failure == "" {
			res.Failure = fmt.Sprintf("run %d exited %d: %s", i+1, code, stderr)
		}
		raw = append(raw, ms)
	}
	sorted := append([]float64(nil), raw...)
	sort.Float64s(sorted)
	res.P50Ms = round3(percentile(sorted, 50))
	res.P95Ms = round3(percentile(sorted, 95))
	res.MinMs = round3(sorted[0])
	res.MaxMs = round3(sorted[len(sorted)-1])
	res.MeanMs = round3(mean(raw))
	res.Samples = len(sorted)
	return res, nil
}

// runOnce executes one hook process and returns its wall-clock latency in
// milliseconds, its exit code and its stderr tail. A nonzero exit is a gate
// failure rather than a harness error, so it is reported, not raised. The
// injected delay sits inside the measured window on purpose: it is the
// negative control that must trip the gate.
func runOnce(bin, home, event, payload string, delayMs int) (float64, int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "hook", "--agent", "claude", "--event", event)
	cmd.Env = hookEnv(home)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	start := time.Now()
	if delayMs > 0 {
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
	}
	err := cmd.Run()
	elapsed := time.Since(start)

	if ctx.Err() != nil {
		return 0, 0, "", fmt.Errorf("hook timed out after 60s")
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return 0, 0, "", err
		}
		code = exitErr.ExitCode()
	}
	return float64(elapsed) / float64(time.Millisecond), code, truncate(stderr.String(), 200), nil
}

// hookEnv isolates the run: $MEM_HOME points at the staged copy and $HOME at an
// empty directory, so a bug in home resolution can never reach a real memory
// repository. Git config is pinned away from the developer's own hooks.
func hookEnv(home string) []string {
	fakeHome := filepath.Join(filepath.Dir(home), ".bench-home")
	_ = os.MkdirAll(fakeHome, 0o755)
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "MEM_HOME="),
			strings.HasPrefix(kv, "MY_MEMORY_HOME="),
			strings.HasPrefix(kv, "MEM_AGENT="),
			strings.HasPrefix(kv, "HOME="),
			strings.HasPrefix(kv, "XDG_CONFIG_HOME="),
			strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL="),
			strings.HasPrefix(kv, "GIT_CONFIG_SYSTEM="):
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"MEM_HOME="+home,
		"HOME="+fakeHome,
		"XDG_CONFIG_HOME="+filepath.Join(fakeHome, ".config"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
}

// decide applies the gate: every event's p50 must be within the threshold and
// every hook process must have exited 0. It returns the headline value (the
// worst per-event p50) and whether the gate failed.
func decide(results []eventResult, thresholdMs float64) (float64, bool) {
	worst := 0.0
	failed := false
	for _, res := range results {
		if res.P50Ms > worst {
			worst = res.P50Ms
		}
		if res.P50Ms > thresholdMs {
			failed = true
		}
		for code := range res.ExitCodes {
			if code != 0 {
				failed = true
			}
		}
	}
	return worst, failed
}

// percentile is nearest-rank: rank = ceil(p/100 * n), clamped to the sample.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// printReport renders the human-readable gate result the script prints.
func printReport(a artifact) {
	fmt.Printf("%s hook latency gate — synthetic reference corpus\n", a.Gate)
	fmt.Printf("corpus:   %s (%d records, provenance: %s)\n", a.Corpus.Path, a.Corpus.Records, a.Corpus.Provenance)
	fmt.Printf("profile:  %s/%s · %s · go %s · %d cpus · maxprocs %d\n",
		a.Profile.OS, a.Profile.Arch, a.Profile.CPU, a.Profile.Go, a.Profile.CPUs, a.Profile.GoMaxProcs)
	fmt.Printf("binary:   sha256 %s\n", a.Profile.BinarySHA)
	if a.DelayMs > 0 {
		fmt.Printf("control:  injected blocking delay %d ms per run (negative control)\n", a.DelayMs)
	}
	fmt.Printf("\n%-14s %5s %10s %10s %10s %10s %8s\n", "event", "runs", "p50", "p95", "min", "max", "exit")
	for _, e := range a.Events {
		fmt.Printf("%-14s %5d %9.1fms %9.1fms %9.1fms %9.1fms %8s\n",
			e.Event, e.Runs, e.P50Ms, e.P95Ms, e.MinMs, e.MaxMs, exitSummary(e.ExitCodes))
	}
	fmt.Printf("\np50 (%s): %.1f ms · threshold %.0f ms → %s\n", "worst event", a.P50Ms, a.ThresholdMs, a.Status)
}

// exitSummary renders the per-code counts, e.g. "0×15" or "0×9 1×1".
func exitSummary(codes map[int]int) string {
	keys := make([]int, 0, len(codes))
	for code := range codes {
		keys = append(keys, code)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, code := range keys {
		parts = append(parts, fmt.Sprintf("%d×%d", code, codes[code]))
	}
	return strings.Join(parts, " ")
}

// writeArtifact writes the JSON evidence next to the run. A gate run without
// its artifact is not evidence, so a write failure is an error, not a warning.
func writeArtifact(path string, a artifact) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create artifact dir: %w", err)
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal artifact: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write artifact %s: %w", path, err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// cpuModel names the CPU when the platform exposes it; GOARCH otherwise.
func cpuModel() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			if model := strings.TrimSpace(string(out)); model != "" {
				return model
			}
		}
	case "linux":
		if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "model name") {
					if _, model, ok := strings.Cut(line, ":"); ok {
						return strings.TrimSpace(model)
					}
				}
			}
		}
	}
	return runtime.GOARCH
}

// osVersion names the OS release when it is cheap to read; GOOS otherwise.
func osVersion() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return "macOS " + v
			}
		}
	case "linux":
		if raw, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				}
			}
		}
	}
	return runtime.GOOS
}

// ---------------------------------------------------------------------------
// Artifact schema
// ---------------------------------------------------------------------------

// artifact is the machine-readable G6 evidence (AC-P2-18, AC-P2-19).
type artifact struct {
	Gate        string        `json:"gate"`
	Status      string        `json:"status"`
	ExitCode    int           `json:"exit_code"`
	Date        string        `json:"date"`
	Command     string        `json:"command"`
	ThresholdMs float64       `json:"threshold_ms"`
	P50Ms       float64       `json:"p50_ms"`
	GateBasis   string        `json:"gate_basis"`
	DelayMs     int           `json:"delay_ms"`
	Config      runConfig     `json:"config"`
	Events      []eventResult `json:"events"`
	Corpus      corpusInfo    `json:"corpus"`
	Profile     machineInfo   `json:"profile"`
}

type runConfig struct {
	Events []string `json:"events"`
	Runs   int      `json:"runs"`
	Warmup int      `json:"warmup"`
	Agent  string   `json:"agent"`
}

type eventResult struct {
	Event     string      `json:"event"`
	Runs      int         `json:"runs"`
	Warmup    int         `json:"warmup"`
	Samples   int         `json:"samples"`
	P50Ms     float64     `json:"p50_ms"`
	P95Ms     float64     `json:"p95_ms"`
	MinMs     float64     `json:"min_ms"`
	MaxMs     float64     `json:"max_ms"`
	MeanMs    float64     `json:"mean_ms"`
	ExitCodes map[int]int `json:"exit_codes"`
	Failure   string      `json:"failure,omitempty"`
}

type corpusInfo struct {
	Path           string         `json:"path"`
	Kind           string         `json:"kind"`
	Provenance     string         `json:"provenance"`
	ProvenanceFile string         `json:"provenance_file"`
	Records        int            `json:"records"`
	MinRecords     int            `json:"min_records"`
	ByLayer        map[string]int `json:"by_layer"`
}

type machineInfo struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	OSVersion   string `json:"os_version"`
	CPU         string `json:"cpu"`
	CPUs        int    `json:"cpus"`
	GoMaxProcs  int    `json:"gomaxprocs"`
	Go          string `json:"go"`
	BinarySHA   string `json:"binary_sha256"`
	BinaryBytes int64  `json:"binary_bytes"`
}
