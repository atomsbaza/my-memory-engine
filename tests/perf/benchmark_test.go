// Package perf_test drives the G6 hook latency gate end to end
// (AC-P2-18, NFR-PERF-01): the shell tool builds the real `mem` binary, stages
// the synthetic reference corpus, measures the hook and writes a JSON artifact
// with the machine profile, the real exit code and the p50.
//
// The negative control is part of the contract: an injected blocking delay must
// make the gate exit non-zero, otherwise "p50 <= 150 ms" would be an
// unfalsifiable claim.
package perf_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	scriptPath = "tools/benchmark-hook.sh"
	corpusPath = "testdata/perf/reference"
	corpusRel  = "testdata/perf/reference"
	threshold  = 150.0
)

// root is the repository root: the gate tool documents being run from there,
// and every path it records stays repository-relative.
var (
	root   string
	memBin string
)

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		// The perf harness drives the hook through bash scripts; Windows
		// perf is out of scope for the P4 cross-platform gate.
		fmt.Println("skip: perf harness requires bash")
		os.Exit(0)
	}
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	root = filepath.Dir(filepath.Dir(wd))

	dir, err := os.MkdirTemp("", "mem-perf-")
	if err != nil {
		panic(err)
	}
	memBin = filepath.Join(dir, "mem")
	build := exec.Command("go", "build", "-o", memBin, "./cmd/mem")
	build.Dir = root
	if out, berr := build.CombinedOutput(); berr != nil {
		os.Stderr.WriteString("go build cmd/mem: " + berr.Error() + "\n" + string(out))
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runScript executes the gate tool and returns its real exit code plus both
// streams. A non-zero exit is a result, not a test failure.
func runScript(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{scriptPath}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %s: %v", scriptPath, err)
		}
		code = exitErr.ExitCode()
	}
	return code, out.String(), errb.String()
}

// artifact is the subset of the JSON evidence these tests assert on.
type artifact struct {
	Gate        string  `json:"gate"`
	Status      string  `json:"status"`
	ExitCode    int     `json:"exit_code"`
	Date        string  `json:"date"`
	Command     string  `json:"command"`
	ThresholdMs float64 `json:"threshold_ms"`
	P50Ms       float64 `json:"p50_ms"`
	DelayMs     int     `json:"delay_ms"`
	Config      struct {
		Events []string `json:"events"`
		Runs   int      `json:"runs"`
		Warmup int      `json:"warmup"`
		Agent  string   `json:"agent"`
	} `json:"config"`
	Events []struct {
		Event     string         `json:"event"`
		Runs      int            `json:"runs"`
		P50Ms     float64        `json:"p50_ms"`
		P95Ms     float64        `json:"p95_ms"`
		ExitCodes map[string]int `json:"exit_codes"`
		Failure   string         `json:"failure"`
	} `json:"events"`
	Corpus struct {
		Path       string         `json:"path"`
		Provenance string         `json:"provenance"`
		Records    int            `json:"records"`
		ByLayer    map[string]int `json:"by_layer"`
	} `json:"corpus"`
	Profile struct {
		OS        string `json:"os"`
		Arch      string `json:"arch"`
		CPU       string `json:"cpu"`
		Go        string `json:"go"`
		BinarySHA string `json:"binary_sha256"`
	} `json:"profile"`
}

func readArtifact(t *testing.T, path string) artifact {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("artifact %s: %v", path, err)
	}
	var art artifact
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatalf("artifact %s is not valid JSON: %v", path, err)
	}
	return art
}

// TestHookLatencyGatePassesG6 runs the gate exactly as CI would and proves the
// evidence carries the five required fields.
func TestHookLatencyGatePassesG6(t *testing.T) {
	out := filepath.Join(t.TempDir(), "g6.json")
	code, stdout, stderr := runScript(t,
		"--corpus", corpusPath,
		"--runs", "15",
		"--warmup", "2",
		"--out", out,
		"--bin", memBin,
	)
	if code != 0 {
		t.Fatalf("gate exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "PASS") {
		t.Fatalf("gate printed no PASS verdict:\n%s", stdout)
	}
	art := readArtifact(t, out)

	// The five artifact fields (AC-P2-19): command, real exit code,
	// version/SHA, date and probe result with a status.
	if art.Gate != "G6" {
		t.Fatalf("gate = %q, want G6", art.Gate)
	}
	if art.Status != "PASS" || art.ExitCode != 0 {
		t.Fatalf("status/exit_code = %q/%d, want PASS/0", art.Status, art.ExitCode)
	}
	if !strings.Contains(art.Command, "--corpus "+corpusRel) {
		t.Fatalf("artifact command does not record the gate invocation: %q", art.Command)
	}
	if _, err := time.Parse(time.RFC3339, art.Date); err != nil {
		t.Fatalf("artifact date %q is not RFC3339: %v", art.Date, err)
	}
	if len(art.Profile.BinarySHA) != 64 {
		t.Fatalf("binary sha256 = %q, want 64 hex chars", art.Profile.BinarySHA)
	}
	if art.Profile.Go == "" || art.Profile.OS == "" || art.Profile.CPU == "" {
		t.Fatalf("machine/runtime profile is incomplete: %+v", art.Profile)
	}

	// The threshold itself.
	if art.ThresholdMs != threshold {
		t.Fatalf("threshold = %v, want %v", art.ThresholdMs, threshold)
	}
	if art.P50Ms > threshold {
		t.Fatalf("p50 = %.1f ms, want <= %.0f ms", art.P50Ms, threshold)
	}
	if art.DelayMs != 0 {
		t.Fatalf("delay_ms = %d on a real run, want 0", art.DelayMs)
	}

	// Synthetic corpus only, with the label on disk.
	if art.Corpus.Path != corpusRel || art.Corpus.Provenance != "synthetic" {
		t.Fatalf("corpus = %+v, want path %q provenance synthetic", art.Corpus, corpusRel)
	}
	if art.Corpus.Records < 400 {
		t.Fatalf("corpus records = %d, want >= 400", art.Corpus.Records)
	}

	// Both injection events measured, all samples exited 0.
	if len(art.Events) != 2 {
		t.Fatalf("measured events = %d, want 2", len(art.Events))
	}
	for _, ev := range art.Events {
		if ev.Runs != 15 {
			t.Fatalf("%s runs = %d, want 15", ev.Event, ev.Runs)
		}
		if ev.P50Ms > threshold {
			t.Fatalf("%s p50 = %.1f ms, want <= %.0f ms", ev.Event, ev.P50Ms, threshold)
		}
		if ev.ExitCodes["0"] != 15 || ev.Failure != "" {
			t.Fatalf("%s exit codes = %v failure = %q, want 15×0 and no failure", ev.Event, ev.ExitCodes, ev.Failure)
		}
	}
}

// TestHookLatencyGateFailsOnInjectedDelay is the discriminating control: the
// same corpus and binary with a blocking delay must turn the gate red.
func TestHookLatencyGateFailsOnInjectedDelay(t *testing.T) {
	out := filepath.Join(t.TempDir(), "g6-delay.json")
	code, stdout, stderr := runScript(t,
		"--corpus", corpusPath,
		"--runs", "7",
		"--warmup", "1",
		"--delay-ms", "200",
		"--out", out,
		"--bin", memBin,
	)
	if code == 0 {
		t.Fatalf("gate exit = 0 with an injected 200 ms delay; want non-zero\nstdout:\n%s", stdout)
	}
	if code != 1 {
		t.Fatalf("gate exit = %d, want 1 (threshold failure)\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "FAIL") {
		t.Fatalf("gate printed no FAIL verdict:\n%s", stdout)
	}
	art := readArtifact(t, out)
	if art.Status != "FAIL" || art.ExitCode != 1 {
		t.Fatalf("status/exit_code = %q/%d, want FAIL/1", art.Status, art.ExitCode)
	}
	if art.DelayMs != 200 {
		t.Fatalf("delay_ms = %d, want the injected 200", art.DelayMs)
	}
	if art.P50Ms <= threshold {
		t.Fatalf("p50 = %.1f ms with a 200 ms delay; want > %.0f ms", art.P50Ms, threshold)
	}
	for _, ev := range art.Events {
		if ev.P50Ms <= threshold {
			t.Fatalf("%s p50 = %.1f ms with the control delay; want > %.0f ms", ev.Event, ev.P50Ms, threshold)
		}
	}
}

// TestHookLatencyGateRefusesUnprovenancedCorpus proves the synthetic-only
// boundary: an unlabeled corpus, or one too small to stress the read path, is
// refused before anything is measured.
func TestHookLatencyGateRefusesUnprovenancedCorpus(t *testing.T) {
	unlabeled := filepath.Join(t.TempDir(), "corpus")
	if err := os.MkdirAll(filepath.Join(unlabeled, "home"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	code, _, stderr := runScript(t, "--corpus", unlabeled, "--runs", "1", "--warmup", "0", "--out", filepath.Join(t.TempDir(), "x.json"), "--bin", memBin)
	if code != 2 {
		t.Fatalf("unlabeled corpus: exit = %d, want 2 (refused)\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "PROVENANCE.md") {
		t.Fatalf("refusal does not name the missing provenance label:\n%s", stderr)
	}

	// Labeled but below the 400-record stress trigger: also refused.
	small := filepath.Join(t.TempDir(), "corpus")
	if err := os.MkdirAll(filepath.Join(small, "home"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(small, "PROVENANCE.md"), []byte("provenance: synthetic\n"), 0o644); err != nil {
		t.Fatalf("write provenance: %v", err)
	}
	code, _, stderr = runScript(t, "--corpus", small, "--runs", "1", "--warmup", "0", "--out", filepath.Join(t.TempDir(), "x.json"), "--bin", memBin)
	if code != 2 {
		t.Fatalf("small corpus: exit = %d, want 2 (refused)\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, ">= 400") {
		t.Fatalf("refusal does not name the record minimum:\n%s", stderr)
	}
}

// TestReferenceCorpusIsSynthetic is the provenance gate on the committed
// fixtures: every record carries the inline synthetic label, the ids are
// unique, and nothing points at a real home path.
func TestReferenceCorpusIsSynthetic(t *testing.T) {
	const label = "provenance: test-generated, not corpus-derived"
	corpus := filepath.Join(root, corpusPath)
	home := filepath.Join(corpus, "home")

	ids := make(map[string]bool)
	layers := make(map[string]int)
	records := 0
	err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(raw)
		if !strings.Contains(text, label) {
			t.Errorf("%s: missing synthetic provenance label", path)
		}
		if !strings.HasPrefix(text, "---\n") {
			t.Errorf("%s: no front-matter", path)
		}
		for _, want := range []string{"\nid: ", "\nlayer: ", "\nscope: ", "\nsensitivity: internal\n"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: front-matter missing %q", path, strings.TrimSpace(want))
			}
		}
		// A real corpus would leak absolute home paths; these must never appear.
		for _, forbidden := range []string{"/Users/", "/home/"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s: contains a real home path fragment %q", path, forbidden)
			}
		}
		records++
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "id: ") {
				id := strings.TrimSpace(strings.TrimPrefix(line, "id: "))
				if ids[id] {
					t.Errorf("%s: duplicate id %q", path, id)
				}
				ids[id] = true
			}
			if strings.HasPrefix(line, "layer: ") {
				layers[strings.TrimSpace(strings.TrimPrefix(line, "layer: "))]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk corpus: %v", err)
	}
	if records < 400 {
		t.Fatalf("corpus has %d records, want >= 400 (design incremental-index trigger)", records)
	}
	if len(layers) != 6 {
		t.Fatalf("corpus layers = %v, want all six", layers)
	}

	prov, err := os.ReadFile(filepath.Join(corpus, "PROVENANCE.md"))
	if err != nil {
		t.Fatalf("PROVENANCE.md: %v", err)
	}
	if !strings.Contains(string(prov), "provenance: synthetic") {
		t.Fatalf("PROVENANCE.md does not carry the synthetic label")
	}
	if !strings.Contains(string(prov), "480 synthetic records") {
		t.Fatalf("PROVENANCE.md does not document the corpus size")
	}

	// Derived state must never live under the committed corpus.
	for _, forbidden := range []string{".memory", "recall-log.jsonl"} {
		path := filepath.Join(corpus, forbidden)
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("derived state %s is tracked inside the corpus", path)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "schema", "taxonomy.json")); err != nil {
		t.Fatalf("corpus home has no schema: %v", err)
	}
}
