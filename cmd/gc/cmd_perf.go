package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	perfScenarioNone                      = "none"
	perfScenarioSessionCreate             = "session-create"
	perfScenarioSessionAttachReady        = "session-attach-ready"
	perfScenarioSessionAttachReadyManaged = "session-attach-ready-managed"

	perfCleanupTimeout      = 5 * time.Second
	perfCleanupPollInterval = 100 * time.Millisecond
)

type perfPreparedScenario struct {
	Name                      string
	RootDir                   string
	WorkDir                   string
	Env                       []string
	DefaultArgs               []string
	StopCity                  bool
	PromptReadyToken          string
	ManagedController         bool
	WorkflowSourceBeadID      string
	WorkflowCompletionTimeout time.Duration
	WorkflowPollInterval      time.Duration
}

type perfCommandRequest struct {
	Executable string
	Args       []string
	Dir        string
	Env        []string
}

type perfCommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type perfMetricSummary struct {
	Count int     `json:"count"`
	MinMs float64 `json:"min_ms"`
	AvgMs float64 `json:"avg_ms"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	MaxMs float64 `json:"max_ms"`
}

type perfRunSummary struct {
	Total      perfMetricSummary            `json:"total_duration_ms"`
	Steps      map[string]perfMetricSummary `json:"steps,omitempty"`
	Milestones map[string]perfMetricSummary `json:"milestones,omitempty"`
}

type perfSample struct {
	Iteration  int                `json:"iteration"`
	DurationMs float64            `json:"duration_ms"`
	ExitCode   int                `json:"exit_code"`
	Steps      []perfTraceStep    `json:"steps,omitempty"`
	Milestones map[string]float64 `json:"milestones,omitempty"`
}

type perfRunReport struct {
	Scenario     string         `json:"scenario"`
	Command      []string       `json:"command"`
	Iterations   int            `json:"iterations"`
	Warmups      int            `json:"warmups"`
	ArtifactsDir string         `json:"artifacts_dir,omitempty"`
	Samples      []perfSample   `json:"samples"`
	Summary      perfRunSummary `json:"summary"`
}

var (
	perfCommandRunner            = runPerfCommandSubprocess
	perfInteractiveCommandRunner = runPerfCommandWithPTY
	perfExecutablePath           = os.Executable
	perfProcessListCommand       = runPerfProcessListCommand
)

func newPerfCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "perf",
		Short:  "Run hidden performance harnesses",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newPerfRunCmd(stdout, stderr))
	return cmd
}

func newPerfRunCmd(stdout, stderr io.Writer) *cobra.Command {
	var scenario string
	var iterations int
	var warmups int
	var jsonOut bool
	var keep bool
	cmd := &cobra.Command{
		Use:   "run [flags] [-- <gc args...>]",
		Short: "Benchmark gc command lines in a controlled scenario",
		Long: `Run a gc command line repeatedly under a scenario-specific harness.

Scenarios:
  none                         Run the command in the current environment
  session-create               Create a fresh city with a helper template for each iteration
  session-attach-ready         Attach to a fresh tmux-backed shell session and wait for a prompt token
  session-attach-ready-managed Same as session-attach-ready, but with a foreground controller handling session start
  formula-scoped-work          Start a managed formula_v2 city, seed beads, sling mol-scoped-work, and wait for workflow completion

Command args may be provided either as "session new helper --no-attach" or
with a leading "gc". When omitted, the scenario's default command is used.`,
		Example: `  gc perf run --scenario session-create --iterations 5 -- session new helper --no-attach
  gc perf run --scenario none --iterations 10 -- gc status
  gc perf run --scenario session-attach-ready-managed --iterations 5
  gc perf run --scenario formula-scoped-work --iterations 3 --warmups 0 --json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdPerfRun(scenario, iterations, warmups, jsonOut, keep, args, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scenario, "scenario", perfScenarioSessionCreate, "measurement scenario")
	cmd.Flags().IntVar(&iterations, "iterations", 5, "measured iterations")
	cmd.Flags().IntVar(&warmups, "warmups", 1, "warmup iterations excluded from the report")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON report")
	cmd.Flags().BoolVar(&keep, "keep", false, "keep harness artifacts instead of removing them")
	return cmd
}

func cmdPerfRun(scenarioName string, iterations, warmups int, jsonOut, keep bool, args []string, stdout, stderr io.Writer) int {
	if iterations <= 0 {
		fmt.Fprintf(stderr, "gc perf run: --iterations must be > 0 (got %d)\n", iterations) //nolint:errcheck
		return 1
	}
	if warmups < 0 {
		fmt.Fprintf(stderr, "gc perf run: --warmups must be >= 0 (got %d)\n", warmups) //nolint:errcheck
		return 1
	}
	executable, err := perfExecutablePath()
	if err != nil {
		fmt.Fprintf(stderr, "gc perf run: resolving gc executable: %v\n", err) //nolint:errcheck
		return 1
	}
	rootDir, err := os.MkdirTemp("", "gc-perf-*")
	if err != nil {
		fmt.Fprintf(stderr, "gc perf run: creating temp root: %v\n", err) //nolint:errcheck
		return 1
	}
	if !keep {
		defer os.RemoveAll(rootDir)
	}

	commandArgs := normalizePerfCommandArgs(args)
	report := perfRunReport{
		Scenario:   scenarioName,
		Iterations: iterations,
		Warmups:    warmups,
	}
	totalRuns := iterations + warmups
	samples := make([]perfSample, 0, iterations)
	for runIndex := 1; runIndex <= totalRuns; runIndex++ {
		prepared, err := preparePerfScenario(scenarioName, rootDir, runIndex)
		if err != nil {
			fmt.Fprintf(stderr, "gc perf run: scenario %q: %v\n", scenarioName, err) //nolint:errcheck
			return 1
		}
		traceFile := filepath.Join(prepared.RootDir, fmt.Sprintf("trace-%03d.json", runIndex))
		env := upsertEnv(prepared.Env, perfTraceFileEnv, traceFile)
		controllerCleanup, activationErr := activatePerfScenario(executable, prepared, env)
		if activationErr != nil {
			fmt.Fprintf(stderr, "gc perf run: activating scenario %q: %v\n", scenarioName, activationErr) //nolint:errcheck
			return 1
		}
		runArgs := commandArgs
		if len(runArgs) == 0 {
			runArgs = append([]string(nil), prepared.DefaultArgs...)
		}
		if len(runArgs) == 0 {
			controllerCleanup()
			bestEffortPerfCleanup(executable, prepared)
			fmt.Fprintf(stderr, "gc perf run: scenario %q requires command args after --\n", scenarioName) //nolint:errcheck
			return 1
		}
		if len(report.Command) == 0 {
			report.Command = append([]string(nil), runArgs...)
		}
		req := perfCommandRequest{
			Executable: executable,
			Args:       runArgs,
			Dir:        prepared.WorkDir,
			Env:        env,
		}
		started := time.Now()
		outcome, runErr := runPreparedPerfCommand(prepared, req)
		elapsed := time.Since(started)
		trace, traceErr := readPerfTraceReport(traceFile)
		if traceErr == nil {
			traceErr = enrichPerfWorkflowControllerMetrics(prepared, req, started, &trace, outcome.Milestones)
		}
		controllerCleanup()
		bestEffortPerfCleanup(executable, prepared)
		if !keep && prepared.RootDir != "" {
			_ = os.RemoveAll(prepared.RootDir)
		}
		if runErr != nil {
			fmt.Fprintf(stderr, "gc perf run: iteration %d: %v\n", runIndex, runErr) //nolint:errcheck
			if strings.TrimSpace(outcome.Result.Stdout) != "" {
				fmt.Fprintf(stderr, "stdout:\n%s\n", strings.TrimSpace(outcome.Result.Stdout)) //nolint:errcheck
			}
			if strings.TrimSpace(outcome.Result.Stderr) != "" {
				fmt.Fprintf(stderr, "stderr:\n%s\n", strings.TrimSpace(outcome.Result.Stderr)) //nolint:errcheck
			}
			return 1
		}
		if outcome.Result.ExitCode != 0 {
			fmt.Fprintf(stderr, "gc perf run: iteration %d exited with code %d\n", runIndex, outcome.Result.ExitCode) //nolint:errcheck
			if strings.TrimSpace(outcome.Result.Stdout) != "" {
				fmt.Fprintf(stderr, "stdout:\n%s\n", strings.TrimSpace(outcome.Result.Stdout)) //nolint:errcheck
			}
			if strings.TrimSpace(outcome.Result.Stderr) != "" {
				fmt.Fprintf(stderr, "stderr:\n%s\n", strings.TrimSpace(outcome.Result.Stderr)) //nolint:errcheck
			}
			return 1
		}
		if traceErr != nil {
			fmt.Fprintf(stderr, "gc perf run: reading trace for iteration %d: %v\n", runIndex, traceErr) //nolint:errcheck
			return 1
		}
		if runIndex <= warmups {
			continue
		}
		samples = append(samples, perfSample{
			Iteration:  runIndex - warmups,
			DurationMs: durationMs(elapsed),
			ExitCode:   outcome.Result.ExitCode,
			Steps:      trace.Steps,
			Milestones: outcome.Milestones,
		})
	}

	report.Samples = samples
	report.Summary = summarizePerfSamples(samples)
	if keep {
		report.ArtifactsDir = rootDir
	}

	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "gc perf run: encoding report: %v\n", err) //nolint:errcheck
			return 1
		}
		return 0
	}
	writePerfReport(stdout, report)
	return 0
}

func preparePerfScenario(name, rootDir string, iteration int) (*perfPreparedScenario, error) {
	switch strings.TrimSpace(name) {
	case "", perfScenarioSessionCreate:
		return prepareSessionCreatePerfScenario(rootDir, iteration)
	case perfScenarioSessionAttachReady:
		return prepareSessionAttachReadyPerfScenario(rootDir, iteration, false)
	case perfScenarioSessionAttachReadyManaged:
		return prepareSessionAttachReadyPerfScenario(rootDir, iteration, true)
	case perfScenarioFormulaScopedWork:
		return prepareFormulaScopedWorkPerfScenario(rootDir, iteration)
	case perfScenarioNone:
		return prepareNoopPerfScenario(rootDir, iteration)
	default:
		return nil, fmt.Errorf("unknown scenario %q", name)
	}
}

func prepareNoopPerfScenario(rootDir string, iteration int) (*perfPreparedScenario, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return &perfPreparedScenario{
		Name:    perfScenarioNone,
		RootDir: filepath.Join(rootDir, fmt.Sprintf("iter-%03d", iteration)),
		WorkDir: workDir,
		Env:     append([]string(nil), os.Environ()...),
	}, nil
}

func prepareSessionCreatePerfScenario(rootDir string, iteration int) (*perfPreparedScenario, error) {
	iterRoot := filepath.Join(rootDir, fmt.Sprintf("iter-%03d", iteration))
	homeDir := filepath.Join(iterRoot, "home")
	runtimeDir := filepath.Join(iterRoot, "runtime")
	cityPath := filepath.Join(iterRoot, "city")
	for _, dir := range []string{homeDir, runtimeDir, cityPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := ensureCityScaffold(cityPath); err != nil {
		return nil, err
	}
	cityConfig := strings.TrimSpace(`
[workspace]
name = "perf-city"
start_command = "sleep 60"

[[agent]]
name = "helper"
max_active_sessions = 1
`) + "\n"
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(cityConfig), 0o644); err != nil {
		return nil, err
	}
	env := envMapFromSlice(os.Environ())
	env["HOME"] = homeDir
	env["GC_HOME"] = homeDir
	env["XDG_RUNTIME_DIR"] = runtimeDir
	env["GC_SESSION"] = "subprocess"
	env["GC_BEADS"] = "file"
	env["GC_DOLT"] = "skip"
	env["GC_BOOTSTRAP"] = "skip"
	return &perfPreparedScenario{
		Name:    perfScenarioSessionCreate,
		RootDir: iterRoot,
		WorkDir: cityPath,
		Env:     envSliceFromMap(env),
		DefaultArgs: []string{
			"session", "new", "helper", "--alias", "bench", "--no-attach",
		},
		StopCity: true,
	}, nil
}

func bestEffortPerfCleanup(executable string, prepared *perfPreparedScenario) {
	if prepared == nil || !prepared.StopCity || prepared.WorkDir == "" {
		return
	}
	env := withoutEnvKey(prepared.Env, perfTraceFileEnv)
	_, err := runPerfCleanupCommand(perfCommandRequest{
		Executable: executable,
		Args:       []string{"stop", prepared.WorkDir},
		Dir:        prepared.WorkDir,
		Env:        env,
	}, perfCleanupTimeout)
	if err == nil {
		return
	}
	forceKillPerfScenarioProcesses(prepared.RootDir, prepared.WorkDir)
}

func runPerfCommandSubprocess(req perfCommandRequest) (perfCommandResult, error) {
	cmd := exec.Command(req.Executable, req.Args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	result := perfCommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	err := cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

func runPerfCleanupCommand(req perfCommandRequest, timeout time.Duration) (perfCommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Executable, req.Args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	result := perfCommandResult{}
	err := cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if err == nil {
		return result, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

func runPerfProcessListCommand() (string, error) {
	out, err := exec.Command("ps", "-eo", "pid=,args=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func forceKillPerfScenarioProcesses(rootDir, workDir string) {
	pids := collectPerfScenarioPIDs(rootDir, workDir)
	if len(pids) == 0 {
		return
	}
	killPerfScenarioPIDs(pids, syscall.SIGTERM)
	waitForPerfScenarioExit(pids, 750*time.Millisecond)
	remaining := filterAlivePerfScenarioPIDs(pids)
	if len(remaining) == 0 {
		return
	}
	killPerfScenarioPIDs(remaining, syscall.SIGKILL)
	_ = waitForPerfScenarioExit(remaining, 750*time.Millisecond)
}

func collectPerfScenarioPIDs(rootDir, workDir string) []int {
	snapshot, err := perfProcessListCommand()
	if err != nil {
		return nil
	}
	var markers []string
	for _, path := range []string{rootDir, workDir} {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		markers = append(markers, path)
	}
	if len(markers) == 0 {
		return nil
	}
	seen := make(map[int]struct{})
	pids := make([]int, 0)
	for _, line := range strings.Split(snapshot, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, args, ok := parsePerfProcessSnapshotLine(line)
		if !ok {
			continue
		}
		matched := false
		for _, marker := range markers {
			if strings.Contains(args, marker) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

func parsePerfProcessSnapshotLine(line string) (int, string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	argsStart := strings.Index(line, fields[1])
	if argsStart < 0 {
		return 0, "", false
	}
	return pid, line[argsStart:], true
}

func killPerfScenarioPIDs(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		_ = syscall.Kill(pid, sig)
	}
}

func filterAlivePerfScenarioPIDs(pids []int) []int {
	alive := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pidAlive(pid) {
			alive = append(alive, pid)
		}
	}
	return alive
}

func waitForPerfScenarioExit(pids []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if len(filterAlivePerfScenarioPIDs(pids)) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(perfCleanupPollInterval)
	}
}

func readPerfTraceReport(path string) (perfTraceReport, error) {
	var report perfTraceReport
	data, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	err = json.Unmarshal(data, &report)
	return report, err
}

func summarizePerfSamples(samples []perfSample) perfRunSummary {
	totalDurations := make([]float64, 0, len(samples))
	stepDurations := make(map[string][]float64)
	milestoneDurations := make(map[string][]float64)
	for _, sample := range samples {
		totalDurations = append(totalDurations, sample.DurationMs)
		for _, step := range sample.Steps {
			stepDurations[step.Name] = append(stepDurations[step.Name], step.DurationMs)
		}
		for name, duration := range sample.Milestones {
			milestoneDurations[name] = append(milestoneDurations[name], duration)
		}
	}
	summary := perfRunSummary{
		Total: summarizeMetric(totalDurations),
	}
	if len(stepDurations) > 0 {
		summary.Steps = make(map[string]perfMetricSummary, len(stepDurations))
		for name, durations := range stepDurations {
			summary.Steps[name] = summarizeMetric(durations)
		}
	}
	if len(milestoneDurations) > 0 {
		summary.Milestones = make(map[string]perfMetricSummary, len(milestoneDurations))
		for name, durations := range milestoneDurations {
			summary.Milestones[name] = summarizeMetric(durations)
		}
	}
	return summary
}

func summarizeMetric(values []float64) perfMetricSummary {
	if len(values) == 0 {
		return perfMetricSummary{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	total := 0.0
	for _, value := range sorted {
		total += value
	}
	return perfMetricSummary{
		Count: len(sorted),
		MinMs: sorted[0],
		AvgMs: total / float64(len(sorted)),
		P50Ms: percentile(sorted, 0.50),
		P95Ms: percentile(sorted, 0.95),
		MaxMs: sorted[len(sorted)-1],
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	position := p * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	fraction := position - float64(lower)
	return sorted[lower] + ((sorted[upper] - sorted[lower]) * fraction)
}

func writePerfReport(w io.Writer, report perfRunReport) {
	fmt.Fprintf(w, "scenario: %s\n", report.Scenario)                     //nolint:errcheck
	fmt.Fprintf(w, "command: gc %s\n", strings.Join(report.Command, " ")) //nolint:errcheck
	fmt.Fprintf(w, "iterations: %d\n", report.Iterations)                 //nolint:errcheck
	fmt.Fprintf(w, "warmups: %d\n", report.Warmups)                       //nolint:errcheck
	if report.ArtifactsDir != "" {
		fmt.Fprintf(w, "artifacts: %s\n", report.ArtifactsDir) //nolint:errcheck
	}
	writePerfSummaryLine(w, "total", report.Summary.Total)
	if len(report.Summary.Milestones) > 0 {
		fmt.Fprintln(w, "milestones:") //nolint:errcheck
		names := make([]string, 0, len(report.Summary.Milestones))
		for name := range report.Summary.Milestones {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			writePerfSummaryLine(w, "  "+name, report.Summary.Milestones[name])
		}
	}
	if len(report.Summary.Steps) > 0 {
		fmt.Fprintln(w, "steps:") //nolint:errcheck
		names := make([]string, 0, len(report.Summary.Steps))
		for name := range report.Summary.Steps {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			writePerfSummaryLine(w, "  "+name, report.Summary.Steps[name])
		}
	}
}

func writePerfSummaryLine(w io.Writer, label string, summary perfMetricSummary) {
	fmt.Fprintf(w, "%s: min=%.2fms avg=%.2fms p50=%.2fms p95=%.2fms max=%.2fms\n",
		label, summary.MinMs, summary.AvgMs, summary.P50Ms, summary.P95Ms, summary.MaxMs) //nolint:errcheck
}

func normalizePerfCommandArgs(args []string) []string {
	args = append([]string(nil), args...)
	if len(args) > 0 && strings.TrimSpace(args[0]) == "gc" {
		return args[1:]
	}
	return args
}

func durationMs(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func envMapFromSlice(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}

func envSliceFromMap(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

func upsertEnv(env []string, key, value string) []string {
	updated := withoutEnvKey(env, key)
	return append(updated, key+"="+value)
}

func withoutEnvKey(env []string, key string) []string {
	filtered := make([]string, 0, len(env))
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
