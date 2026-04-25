package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/shellquote"
)

const (
	perfScenarioFormulaScopedWork = "formula-scoped-work"

	perfMilestoneCommandReturned  = "cli.command_returned"
	perfMilestoneWorkflowAssigned = "workflow.workflow_id_assigned"
	perfMilestoneWorkflowClosed   = "workflow.root_closed"

	perfWorkflowPatrolInterval = "5s"
	perfWorkflowSeedBeadCount  = 8
	perfWorkflowPollInterval   = 100 * time.Millisecond
	perfWorkflowTimeout        = 2 * time.Minute
)

const perfWorkflowWorkerScript = `#!/bin/sh
set -eu

cd "$GC_CITY"
ASSIGNEE="${GC_SESSION_NAME:-${GC_AGENT:-worker}}"
LOG_FILE="$GC_CITY/perf-workflow-steps.log"

while true; do
    ready=$(bd ready --assignee="$ASSIGNEE" --json --limit=1 2>/dev/null || printf '[]')
    bead_id=$(printf '%s\n' "$ready" | jq -r 'if type == "array" then (.[0].id // "") else (.id // "") end' 2>/dev/null || true)
    if [ -z "$bead_id" ]; then
        sleep 0.1
        continue
    fi

    printf '%s\n' "$bead_id" >> "$LOG_FILE"
    bd update "$bead_id" --set-metadata gc.outcome=pass --status closed >/dev/null 2>&1 || true
done
`

type perfBeadSnapshot struct {
	ID       string            `json:"id"`
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
}

var (
	perfAuxCommandRunner   = runPerfCommandSubprocess
	perfBeadSnapshotReader = readPerfBeadSnapshot
)

func prepareFormulaScopedWorkPerfScenario(rootDir string, iteration int) (*perfPreparedScenario, error) {
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
	if err := ensureInitFormulas(cityPath); err != nil {
		return nil, err
	}
	if _, err := MaterializeSystemFormulas(systemFormulasFS, "system_formulas", cityPath); err != nil {
		return nil, err
	}
	if err := ResolveFormulas(cityPath, []string{filepath.Join(cityPath, citylayout.FormulasRoot)}); err != nil {
		return nil, err
	}
	if err := writePerfWorkflowDoltConfig(homeDir); err != nil {
		return nil, err
	}

	workerScriptPath := filepath.Join(iterRoot, "perf-workflow-worker.sh")
	if err := os.WriteFile(workerScriptPath, []byte(perfWorkflowWorkerScript), 0o755); err != nil {
		return nil, err
	}

	cityConfig := strings.TrimSpace(fmt.Sprintf(`
[workspace]
name = "perf-city"

[session]
provider = "subprocess"

[daemon]
formula_v2 = true
patrol_interval = %q

[[agent]]
name = "worker"
min_active_sessions = 1
max_active_sessions = 1
start_command = %q
`, perfWorkflowPatrolInterval, "sh "+shellquote.Quote(workerScriptPath))) + "\n"
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(cityConfig), 0o644); err != nil {
		return nil, err
	}

	env := envMapFromSlice(os.Environ())
	for _, key := range []string{
		"GC_BEADS",
		"GC_BOOTSTRAP",
		"GC_DOLT",
		"GC_DOLT_HOST",
		"GC_DOLT_PASSWORD",
		"GC_DOLT_PORT",
		"GC_DOLT_USER",
		"GC_SESSION",
		"GC_TMUX_SOCKET",
		"BEADS_DIR",
		"BEADS_DOLT_AUTO_START",
	} {
		delete(env, key)
	}
	for key := range env {
		if strings.HasPrefix(key, "BEADS_DOLT_") {
			delete(env, key)
		}
	}
	env["HOME"] = homeDir
	env["GC_HOME"] = homeDir
	env["XDG_RUNTIME_DIR"] = runtimeDir
	env["DOLT_ROOT_PATH"] = homeDir
	env["GC_SESSION"] = "subprocess"
	env["GC_BOOTSTRAP"] = "skip"

	return &perfPreparedScenario{
		Name:                      perfScenarioFormulaScopedWork,
		RootDir:                   iterRoot,
		WorkDir:                   cityPath,
		Env:                       envSliceFromMap(env),
		StopCity:                  true,
		ManagedController:         true,
		WorkflowCompletionTimeout: perfWorkflowTimeout,
		WorkflowPollInterval:      perfWorkflowPollInterval,
	}, nil
}

func writePerfWorkflowDoltConfig(homeDir string) error {
	doltDir := filepath.Join(homeDir, ".dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(doltDir, "config_global.json"),
		[]byte(`{"user.name":"gc-perf","user.email":"gc-perf@test.local"}`), 0o644)
}

func activatePreparedPerfScenario(executable string, prepared *perfPreparedScenario, env []string) error {
	if prepared == nil {
		return nil
	}
	switch prepared.Name {
	case perfScenarioFormulaScopedWork:
		return seedFormulaScopedWorkPerfScenario(executable, prepared, env)
	default:
		return nil
	}
}

func seedFormulaScopedWorkPerfScenario(_ string, prepared *perfPreparedScenario, _ []string) error {
	store, err := openStoreAtForCity(prepared.WorkDir, prepared.WorkDir)
	if err != nil {
		return fmt.Errorf("opening scenario store: %w", err)
	}
	for i := 0; i < perfWorkflowSeedBeadCount; i++ {
		_, err := store.Create(beads.Bead{
			Title: fmt.Sprintf("Perf seed bead %02d", i+1),
			Type:  "task",
		})
		if err != nil {
			return fmt.Errorf("creating seed bead %d: %w", i+1, err)
		}
	}

	source, err := store.Create(beads.Bead{
		Title: "Perf workflow source",
		Type:  "task",
	})
	if err != nil {
		return fmt.Errorf("creating source bead: %w", err)
	}
	prepared.WorkflowSourceBeadID = source.ID
	prepared.DefaultArgs = []string{
		"sling", "worker", source.ID, "--on=mol-scoped-work", "--var", "issue=" + source.ID,
	}
	return nil
}

func runPerfCommandWithWorkflowWait(prepared *perfPreparedScenario, req perfCommandRequest) (perfRunOutcome, error) {
	started := time.Now()
	result, err := perfCommandRunner(req)
	outcome := perfRunOutcome{
		Result:     result,
		Milestones: map[string]float64{},
	}
	outcome.Milestones[perfMilestoneCommandReturned] = durationMs(time.Since(started))
	if err != nil {
		return outcome, err
	}
	if result.ExitCode != 0 || prepared == nil || strings.TrimSpace(prepared.WorkflowSourceBeadID) == "" {
		return outcome, nil
	}
	if err := waitForPerfWorkflowCompletion(prepared, req, started, outcome.Milestones); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func waitForPerfWorkflowCompletion(prepared *perfPreparedScenario, req perfCommandRequest, started time.Time, milestones map[string]float64) error {
	timeout := prepared.WorkflowCompletionTimeout
	if timeout <= 0 {
		timeout = perfWorkflowTimeout
	}
	pollInterval := prepared.WorkflowPollInterval
	if pollInterval <= 0 {
		pollInterval = perfWorkflowPollInterval
	}
	env := withoutEnvKey(req.Env, perfTraceFileEnv)
	deadline := time.Now().Add(timeout)
	workflowID := ""
	var lastSourceErr error
	var lastWorkflowErr error
	for time.Now().Before(deadline) {
		source, err := perfBeadSnapshotReader(req.Executable, req.Dir, env, prepared.WorkflowSourceBeadID)
		if err != nil {
			lastSourceErr = err
		} else {
			workflowID = strings.TrimSpace(source.Metadata["workflow_id"])
			if workflowID != "" {
				if _, ok := milestones[perfMilestoneWorkflowAssigned]; !ok {
					milestones[perfMilestoneWorkflowAssigned] = durationMs(time.Since(started))
				}
				workflow, workflowErr := perfBeadSnapshotReader(req.Executable, req.Dir, env, workflowID)
				if workflowErr != nil {
					lastWorkflowErr = workflowErr
				} else if strings.EqualFold(strings.TrimSpace(workflow.Status), "closed") {
					milestones[perfMilestoneWorkflowClosed] = durationMs(time.Since(started))
					return nil
				}
			}
		}
		time.Sleep(pollInterval)
	}

	msg := fmt.Sprintf("timed out waiting for workflow completion on source %s", prepared.WorkflowSourceBeadID)
	if workflowID != "" {
		msg += fmt.Sprintf(" (workflow %s)", workflowID)
	}
	if lastWorkflowErr != nil {
		msg += fmt.Sprintf(": %v", lastWorkflowErr)
	} else if lastSourceErr != nil {
		msg += fmt.Sprintf(": %v", lastSourceErr)
	}
	return fmt.Errorf("%s", msg)
}

type perfWorkflowControllerMetrics struct {
	Steps      []perfTraceStep
	Milestones map[string]float64
}

type perfWorkflowControllerEvent struct {
	At       time.Time
	Type     string
	BeadID   string
	Kind     string
	Phase    string
	Duration time.Duration
}

func enrichPerfWorkflowControllerMetrics(
	prepared *perfPreparedScenario,
	req perfCommandRequest,
	started time.Time,
	trace *perfTraceReport,
	milestones map[string]float64,
) error {
	if prepared == nil || strings.TrimSpace(prepared.WorkflowSourceBeadID) == "" {
		return nil
	}
	metrics, err := collectPerfWorkflowControllerMetrics(
		filepath.Join(prepared.WorkDir, "control-dispatcher-trace.log"),
		req.Executable,
		req.Dir,
		withoutEnvKey(req.Env, perfTraceFileEnv),
		started,
	)
	if err != nil {
		return err
	}
	if trace != nil && len(metrics.Steps) > 0 {
		trace.Steps = append(trace.Steps, metrics.Steps...)
	}
	if milestones == nil {
		return nil
	}
	for name, value := range metrics.Milestones {
		milestones[name] = value
	}
	return nil
}

func collectPerfWorkflowControllerMetrics(
	logPath, executable, dir string,
	env []string,
	started time.Time,
) (perfWorkflowControllerMetrics, error) {
	var metrics perfWorkflowControllerMetrics
	data, err := os.ReadFile(logPath)
	if err != nil {
		return metrics, err
	}
	events := parsePerfWorkflowControllerEvents(string(data))
	beads := loadPerfWorkflowTraceBeads(events, executable, dir, env)
	return buildPerfWorkflowControllerMetrics(events, beads, started), nil
}

func parsePerfWorkflowControllerEvents(raw string) []perfWorkflowControllerEvent {
	lines := strings.Split(raw, "\n")
	events := make([]perfWorkflowControllerEvent, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		at, ok := parsePerfWorkflowControllerTimestamp(fields[0])
		if !ok {
			continue
		}
		rest := fields[1:]
		switch {
		case len(rest) >= 4 && rest[0] == "serve" && (rest[1] == "process" || rest[1] == "processed"):
			beadID := strings.TrimSpace(strings.TrimPrefix(rest[2], "bead="))
			kind := strings.TrimSpace(strings.TrimPrefix(rest[3], "kind="))
			if beadID == "" || kind == "" {
				continue
			}
			eventType := "process"
			if rest[1] == "processed" {
				eventType = "processed"
			}
			events = append(events, perfWorkflowControllerEvent{
				At:     at,
				Type:   eventType,
				BeadID: beadID,
				Kind:   kind,
			})
		case len(rest) >= 5 && rest[0] == "scope-check" && strings.HasPrefix(rest[1], "bead=") &&
			strings.HasPrefix(rest[2], "phase=") && rest[3] == "ok" && strings.HasPrefix(rest[4], "dur="):
			beadID := strings.TrimSpace(strings.TrimPrefix(rest[1], "bead="))
			phase := strings.TrimSpace(strings.TrimPrefix(rest[2], "phase="))
			dur, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(rest[4], "dur=")))
			if beadID == "" || phase == "" || err != nil {
				continue
			}
			events = append(events, perfWorkflowControllerEvent{
				At:       at,
				Type:     "phase_ok",
				BeadID:   beadID,
				Kind:     "scope-check",
				Phase:    phase,
				Duration: dur,
			})
		}
	}
	return events
}

func parsePerfWorkflowControllerTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return at, true
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

func loadPerfWorkflowTraceBeads(
	events []perfWorkflowControllerEvent,
	executable, dir string,
	env []string,
) map[string]perfBeadSnapshot {
	seen := make(map[string]struct{})
	beads := make(map[string]perfBeadSnapshot)
	for _, event := range events {
		if strings.TrimSpace(event.BeadID) == "" {
			continue
		}
		if _, ok := seen[event.BeadID]; ok {
			continue
		}
		seen[event.BeadID] = struct{}{}
		bead, err := perfBeadSnapshotReader(executable, dir, env, event.BeadID)
		if err != nil {
			continue
		}
		beads[event.BeadID] = bead
	}
	return beads
}

func buildPerfWorkflowControllerMetrics(
	events []perfWorkflowControllerEvent,
	beads map[string]perfBeadSnapshot,
	started time.Time,
) perfWorkflowControllerMetrics {
	metrics := perfWorkflowControllerMetrics{
		Steps:      make([]perfTraceStep, 0, len(events)),
		Milestones: map[string]float64{},
	}
	processStarts := make(map[string]time.Time)
	for _, event := range events {
		bead := beads[event.BeadID]
		stepSlug := perfWorkflowMetricSlugForBead(bead, event.Kind)
		kindSlug := perfWorkflowMetricSlug(event.Kind)
		switch event.Type {
		case "process":
			processStarts[event.BeadID] = event.At
		case "processed":
			stepName := perfWorkflowControllerStepName(stepSlug, kindSlug)
			if start, ok := processStarts[event.BeadID]; ok && !start.IsZero() && stepName != "" {
				metrics.Steps = append(metrics.Steps, perfTraceStep{
					Name:       stepName,
					DurationMs: perfWorkflowDurationMs(event.At.Sub(start)),
				})
			}
			recordPerfWorkflowMilestone(metrics.Milestones, stepName+"_closed", started, event.At)
		case "phase_ok":
			phaseName := perfWorkflowControllerPhaseName(stepSlug, perfWorkflowMetricSlug(event.Phase))
			if phaseName != "" {
				metrics.Steps = append(metrics.Steps, perfTraceStep{
					Name:       phaseName,
					DurationMs: perfWorkflowDurationMs(event.Duration),
				})
			}
			if perfWorkflowMetricSlug(event.Phase) == "close_body" {
				recordPerfWorkflowMilestone(metrics.Milestones, "workflow.body_closed", started, event.At)
			}
		}
	}
	return metrics
}

func perfWorkflowMetricSlugForBead(bead perfBeadSnapshot, fallbackKind string) string {
	if len(bead.Metadata) > 0 {
		for _, candidate := range []string{
			bead.Metadata["gc.step_id"],
			bead.Metadata["gc.control_for"],
			perfWorkflowMetricSlugFromStepRef(bead.Metadata["gc.step_ref"]),
			bead.Metadata["gc.kind"],
		} {
			if slug := perfWorkflowMetricSlug(candidate); slug != "" {
				return slug
			}
		}
	}
	return perfWorkflowMetricSlug(fallbackKind)
}

func perfWorkflowMetricSlugFromStepRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if dot := strings.Index(ref, "."); dot >= 0 && dot+1 < len(ref) {
		ref = ref[dot+1:]
	}
	if idx := strings.Index(ref, ".attempt."); idx >= 0 {
		ref = ref[:idx]
	}
	if strings.HasSuffix(ref, "-scope-check") {
		ref = strings.TrimSuffix(ref, "-scope-check")
	}
	if strings.HasSuffix(ref, ".spec") {
		ref = strings.TrimSuffix(ref, ".spec")
	}
	return ref
}

func perfWorkflowMetricSlug(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, ch := range raw {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteRune(ch)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore && b.Len() > 0 {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func perfWorkflowControllerStepName(stepSlug, kindSlug string) string {
	switch {
	case stepSlug == "" && kindSlug == "":
		return ""
	case stepSlug == "":
		return "workflow." + kindSlug
	case kindSlug == "":
		return "workflow." + stepSlug
	case stepSlug == kindSlug:
		return "workflow." + stepSlug
	default:
		return "workflow." + stepSlug + "." + kindSlug
	}
}

func perfWorkflowControllerPhaseName(stepSlug, phaseSlug string) string {
	switch {
	case stepSlug == "" || phaseSlug == "":
		if phaseSlug == "" {
			return ""
		}
		return "workflow.scope_check." + phaseSlug
	default:
		return "workflow." + stepSlug + ".scope_check." + phaseSlug
	}
}

func perfWorkflowDurationMs(d time.Duration) float64 {
	if d < 0 {
		return 0
	}
	return durationMs(d)
}

func recordPerfWorkflowMilestone(milestones map[string]float64, name string, started, at time.Time) {
	if milestones == nil || strings.TrimSpace(name) == "" {
		return
	}
	ms := perfWorkflowDurationMs(at.Sub(started))
	if existing, ok := milestones[name]; !ok || ms > existing {
		milestones[name] = ms
	}
}

func createPerfBead(executable, dir string, env []string, title string) (string, error) {
	result, err := runPerfAuxCommand(perfCommandRequest{
		Executable: executable,
		Args:       []string{"bd", "create", "--json", "--type", "task", title},
		Dir:        dir,
		Env:        env,
	})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("gc bd create exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	created, err := parsePerfBeadSnapshot(result.Stdout)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(created.ID) == "" {
		return "", fmt.Errorf("gc bd create returned empty bead id")
	}
	return created.ID, nil
}

func closePerfBead(executable, dir string, env []string, beadID string) error {
	result, err := runPerfAuxCommand(perfCommandRequest{
		Executable: executable,
		Args:       []string{"bd", "close", beadID},
		Dir:        dir,
		Env:        env,
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("gc bd close %s exited %d: %s", beadID, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func readPerfBeadSnapshot(executable, dir string, env []string, beadID string) (perfBeadSnapshot, error) {
	result, err := runPerfAuxCommand(perfCommandRequest{
		Executable: executable,
		Args:       []string{"bd", "show", beadID, "--json"},
		Dir:        dir,
		Env:        env,
	})
	if err != nil {
		return perfBeadSnapshot{}, err
	}
	if result.ExitCode != 0 {
		return perfBeadSnapshot{}, fmt.Errorf("gc bd show %s exited %d: %s", beadID, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return parsePerfBeadSnapshot(result.Stdout)
}

func runPerfAuxCommand(req perfCommandRequest) (perfCommandResult, error) {
	req.Env = withoutEnvKey(req.Env, perfTraceFileEnv)
	return perfAuxCommandRunner(req)
}

func parsePerfBeadSnapshot(raw string) (perfBeadSnapshot, error) {
	var bead perfBeadSnapshot
	payload := strings.TrimSpace(extractPerfJSONPayload(raw))
	if payload == "" {
		return bead, fmt.Errorf("no JSON payload found")
	}
	if err := json.Unmarshal([]byte(payload), &bead); err == nil {
		return bead, nil
	}
	var list []perfBeadSnapshot
	if err := json.Unmarshal([]byte(payload), &list); err != nil {
		return bead, fmt.Errorf("parsing bead JSON: %w", err)
	}
	if len(list) == 0 {
		return bead, fmt.Errorf("bead JSON payload was empty")
	}
	return list[0], nil
}

func extractPerfJSONPayload(raw string) string {
	data := []byte(raw)
	for i, b := range data {
		if b != '{' && b != '[' {
			continue
		}
		candidate := strings.TrimSpace(string(data[i:]))
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	return raw
}
