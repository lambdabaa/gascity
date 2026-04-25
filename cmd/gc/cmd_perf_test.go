package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrepareSessionCreatePerfScenario(t *testing.T) {
	root := t.TempDir()
	prepared, err := prepareSessionCreatePerfScenario(root, 1)
	if err != nil {
		t.Fatalf("prepareSessionCreatePerfScenario: %v", err)
	}

	if prepared.Name != perfScenarioSessionCreate {
		t.Fatalf("Name = %q, want %q", prepared.Name, perfScenarioSessionCreate)
	}
	if !prepared.StopCity {
		t.Fatal("StopCity = false, want true")
	}

	wantArgs := []string{"session", "new", "helper", "--alias", "bench", "--no-attach"}
	if !reflect.DeepEqual(prepared.DefaultArgs, wantArgs) {
		t.Fatalf("DefaultArgs = %v, want %v", prepared.DefaultArgs, wantArgs)
	}

	if _, err := os.Stat(filepath.Join(prepared.WorkDir, ".gc", "events.jsonl")); err != nil {
		t.Fatalf("expected city scaffold, stat events.jsonl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(prepared.WorkDir, "city.toml"))
	if err != nil {
		t.Fatalf("ReadFile(city.toml): %v", err)
	}
	if !bytes.Contains(data, []byte(`start_command = "sleep 60"`)) {
		t.Fatalf("city.toml missing start_command:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`name = "helper"`)) {
		t.Fatalf("city.toml missing helper agent:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`max_active_sessions = 1`)) {
		t.Fatalf("city.toml missing singleton session cap:\n%s", data)
	}

	env := envMapFromSlice(prepared.Env)
	if env["GC_SESSION"] != "subprocess" {
		t.Fatalf("GC_SESSION = %q, want %q", env["GC_SESSION"], "subprocess")
	}
	if env["GC_BEADS"] != "file" {
		t.Fatalf("GC_BEADS = %q, want %q", env["GC_BEADS"], "file")
	}
	if env["GC_DOLT"] != "skip" {
		t.Fatalf("GC_DOLT = %q, want %q", env["GC_DOLT"], "skip")
	}
	if env["HOME"] == "" || env["GC_HOME"] == "" || env["XDG_RUNTIME_DIR"] == "" {
		t.Fatalf("expected HOME, GC_HOME, and XDG_RUNTIME_DIR in env: %#v", env)
	}
}

func TestPrepareSessionAttachReadyPerfScenario(t *testing.T) {
	root := t.TempDir()
	prepared, err := prepareSessionAttachReadyPerfScenario(root, 1, false)
	if err != nil {
		t.Fatalf("prepareSessionAttachReadyPerfScenario: %v", err)
	}

	if prepared.Name != perfScenarioSessionAttachReady {
		t.Fatalf("Name = %q, want %q", prepared.Name, perfScenarioSessionAttachReady)
	}
	if !prepared.StopCity {
		t.Fatal("StopCity = false, want true")
	}
	if prepared.PromptReadyToken != perfAttachReadyPromptToken {
		t.Fatalf("PromptReadyToken = %q, want %q", prepared.PromptReadyToken, perfAttachReadyPromptToken)
	}
	if prepared.ManagedController {
		t.Fatal("ManagedController = true, want false")
	}

	wantArgs := []string{"session", "new", "helper"}
	if !reflect.DeepEqual(prepared.DefaultArgs, wantArgs) {
		t.Fatalf("DefaultArgs = %v, want %v", prepared.DefaultArgs, wantArgs)
	}

	data, err := os.ReadFile(filepath.Join(prepared.WorkDir, "city.toml"))
	if err != nil {
		t.Fatalf("ReadFile(city.toml): %v", err)
	}
	if !bytes.Contains(data, []byte(`[providers.perfsh]`)) {
		t.Fatalf("city.toml missing perfsh provider:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`max_active_sessions = 1`)) {
		t.Fatalf("city.toml missing singleton session cap:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`ready_prompt_prefix = "GC_PERF_READY>"`)) {
		t.Fatalf("city.toml missing ready prompt prefix:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`command = "sh"`)) {
		t.Fatalf("city.toml missing wrapper shell command:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`args = ["-lc", "PS1=\"GC_PERF_READY> \"; export PS1; PROMPT_COMMAND=\"\"; export PROMPT_COMMAND; exec bash --noprofile --norc -i", "gc-perf-shell"]`)) {
		t.Fatalf("city.toml missing prompt-safe shell wrapper:\n%s", data)
	}
	if bytes.Contains(data, []byte(`PS1 = "GC_PERF_READY> "`)) {
		t.Fatalf("city.toml should not rely on provider env PS1 injection:\n%s", data)
	}

	env := envMapFromSlice(prepared.Env)
	if env["GC_SESSION"] != "tmux" {
		t.Fatalf("GC_SESSION = %q, want %q", env["GC_SESSION"], "tmux")
	}
	if env["GC_TMUX_SOCKET"] != "perf-city" {
		t.Fatalf("GC_TMUX_SOCKET = %q, want %q", env["GC_TMUX_SOCKET"], "perf-city")
	}
}

func TestPrepareFormulaScopedWorkPerfScenario(t *testing.T) {
	root := t.TempDir()
	prepared, err := prepareFormulaScopedWorkPerfScenario(root, 1)
	if err != nil {
		t.Fatalf("prepareFormulaScopedWorkPerfScenario: %v", err)
	}

	if prepared.Name != perfScenarioFormulaScopedWork {
		t.Fatalf("Name = %q, want %q", prepared.Name, perfScenarioFormulaScopedWork)
	}
	if !prepared.StopCity {
		t.Fatal("StopCity = false, want true")
	}
	if !prepared.ManagedController {
		t.Fatal("ManagedController = false, want true")
	}
	if prepared.WorkflowCompletionTimeout != perfWorkflowTimeout {
		t.Fatalf("WorkflowCompletionTimeout = %s, want %s", prepared.WorkflowCompletionTimeout, perfWorkflowTimeout)
	}
	if prepared.WorkflowPollInterval != perfWorkflowPollInterval {
		t.Fatalf("WorkflowPollInterval = %s, want %s", prepared.WorkflowPollInterval, perfWorkflowPollInterval)
	}
	if len(prepared.DefaultArgs) != 0 {
		t.Fatalf("DefaultArgs = %v, want empty before scenario activation", prepared.DefaultArgs)
	}

	data, err := os.ReadFile(filepath.Join(prepared.WorkDir, "city.toml"))
	if err != nil {
		t.Fatalf("ReadFile(city.toml): %v", err)
	}
	if !bytes.Contains(data, []byte(`provider = "subprocess"`)) {
		t.Fatalf("city.toml missing subprocess provider:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`formula_v2 = true`)) {
		t.Fatalf("city.toml missing formula_v2:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`patrol_interval = "5s"`)) {
		t.Fatalf("city.toml missing patrol interval:\n%s", data)
	}
	if !bytes.Contains(data, []byte(`start_command = "sh `)) {
		t.Fatalf("city.toml missing worker start command:\n%s", data)
	}

	if _, err := os.Stat(filepath.Join(prepared.WorkDir, "formulas", "mol-scoped-work.toml")); err != nil {
		t.Fatalf("expected mol-scoped-work formula: %v", err)
	}

	env := envMapFromSlice(prepared.Env)
	if env["HOME"] == "" || env["GC_HOME"] == "" || env["XDG_RUNTIME_DIR"] == "" {
		t.Fatalf("expected HOME, GC_HOME, and XDG_RUNTIME_DIR in env: %#v", env)
	}
	if env["DOLT_ROOT_PATH"] != env["HOME"] {
		t.Fatalf("DOLT_ROOT_PATH = %q, want HOME %q", env["DOLT_ROOT_PATH"], env["HOME"])
	}
	if env["GC_SESSION"] != "subprocess" {
		t.Fatalf("GC_SESSION = %q, want %q", env["GC_SESSION"], "subprocess")
	}
	if env["GC_DOLT"] != "" {
		t.Fatalf("GC_DOLT = %q, want empty", env["GC_DOLT"])
	}
}

func TestCollectPerfScenarioPIDsFiltersByScenarioPaths(t *testing.T) {
	origProcessList := perfProcessListCommand
	t.Cleanup(func() {
		perfProcessListCommand = origProcessList
	})

	perfProcessListCommand = func() (string, error) {
		return strings.Join([]string{
			"101 /tmp/gc-baseline start --controller /tmp/gc-perf-123/iter-001/city",
			"202 dolt sql-server --config /tmp/gc-perf-123/iter-001/city/.gc/runtime/packs/dolt/dolt-config.yaml",
			"303 sh /tmp/gc-perf-123/iter-001/perf-workflow-worker.sh",
			"404 /tmp/gc-baseline status",
			"badline",
		}, "\n"), nil
	}

	got := collectPerfScenarioPIDs("/tmp/gc-perf-123/iter-001", "/tmp/gc-perf-123/iter-001/city")
	want := []int{101, 202, 303}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectPerfScenarioPIDs() = %v, want %v", got, want)
	}
}

func TestCmdPerfRunJSONReportIncludesMeasuredStepSummary(t *testing.T) {
	origRunner := perfCommandRunner
	origExecutablePath := perfExecutablePath
	t.Cleanup(func() {
		perfCommandRunner = origRunner
		perfExecutablePath = origExecutablePath
	})

	perfExecutablePath = func() (string, error) { return "/tmp/fake-gc", nil }
	callCount := 0
	perfCommandRunner = func(req perfCommandRequest) (perfCommandResult, error) {
		if len(req.Args) > 0 && req.Args[0] == "stop" {
			return perfCommandResult{ExitCode: 0}, nil
		}
		callCount++
		tracePath := envMapFromSlice(req.Env)[perfTraceFileEnv]
		stepsByCall := [][]perfTraceStep{
			{
				{Name: "session.new.resolve_city", DurationMs: 1},
				{Name: "session.new.create_session", DurationMs: 5},
			},
			{
				{Name: "session.new.resolve_city", DurationMs: 2},
				{Name: "session.new.create_session", DurationMs: 20},
			},
			{
				{Name: "session.new.resolve_city", DurationMs: 3},
				{Name: "session.new.create_session", DurationMs: 30},
			},
		}
		report := perfTraceReport{
			Command: append([]string(nil), req.Args...),
			Steps:   stepsByCall[callCount-1],
		}
		if err := os.MkdirAll(filepath.Dir(tracePath), 0o755); err != nil {
			t.Fatalf("MkdirAll(trace dir): %v", err)
		}
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("Marshal(trace): %v", err)
		}
		if err := os.WriteFile(tracePath, data, 0o644); err != nil {
			t.Fatalf("WriteFile(trace): %v", err)
		}
		return perfCommandResult{ExitCode: 0}, nil
	}

	var stdout, stderr bytes.Buffer
	code := cmdPerfRun(perfScenarioSessionCreate, 2, 1, true, false, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdPerfRun() = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var report perfRunReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("Unmarshal(report): %v\n%s", err, stdout.String())
	}

	wantCommand := []string{"session", "new", "helper", "--alias", "bench", "--no-attach"}
	if !reflect.DeepEqual(report.Command, wantCommand) {
		t.Fatalf("Command = %v, want %v", report.Command, wantCommand)
	}
	if report.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", report.Iterations)
	}
	if report.Warmups != 1 {
		t.Fatalf("Warmups = %d, want 1", report.Warmups)
	}
	if len(report.Samples) != 2 {
		t.Fatalf("len(Samples) = %d, want 2", len(report.Samples))
	}

	createSummary := report.Summary.Steps["session.new.create_session"]
	if createSummary.Count != 2 {
		t.Fatalf("create_summary.Count = %d, want 2", createSummary.Count)
	}
	if createSummary.MinMs != 20 {
		t.Fatalf("create_summary.MinMs = %.2f, want 20", createSummary.MinMs)
	}
	if createSummary.MaxMs != 30 {
		t.Fatalf("create_summary.MaxMs = %.2f, want 30", createSummary.MaxMs)
	}

	resolveSummary := report.Summary.Steps["session.new.resolve_city"]
	if resolveSummary.MinMs != 2 || resolveSummary.MaxMs != 3 {
		t.Fatalf("resolve_summary = %+v, want min=2 max=3", resolveSummary)
	}
}

func TestSummarizePerfSamplesIncludesMilestones(t *testing.T) {
	report := summarizePerfSamples([]perfSample{
		{
			DurationMs: 10,
			Steps: []perfTraceStep{
				{Name: "session.new.create_session", DurationMs: 3},
			},
			Milestones: map[string]float64{
				"tty.prompt_ready": 6,
			},
		},
		{
			DurationMs: 20,
			Steps: []perfTraceStep{
				{Name: "session.new.create_session", DurationMs: 7},
			},
			Milestones: map[string]float64{
				"tty.prompt_ready": 12,
			},
		},
	})

	if report.Total.Count != 2 {
		t.Fatalf("Total.Count = %d, want 2", report.Total.Count)
	}
	if got := report.Milestones["tty.prompt_ready"]; got.Count != 2 || got.MinMs != 6 || got.MaxMs != 12 {
		t.Fatalf("Milestones[tty.prompt_ready] = %+v, want count=2 min=6 max=12", got)
	}
}

func TestWaitForPerfWorkflowCompletionRecordsMilestones(t *testing.T) {
	origReader := perfBeadSnapshotReader
	t.Cleanup(func() {
		perfBeadSnapshotReader = origReader
	})

	readCount := make(map[string]int)
	perfBeadSnapshotReader = func(_ string, _ string, _ []string, beadID string) (perfBeadSnapshot, error) {
		readCount[beadID]++
		switch beadID {
		case "gc-source":
			if readCount[beadID] == 1 {
				return perfBeadSnapshot{ID: beadID, Status: "open", Metadata: map[string]string{}}, nil
			}
			return perfBeadSnapshot{
				ID:     beadID,
				Status: "open",
				Metadata: map[string]string{
					"workflow_id": "gc-workflow",
				},
			}, nil
		case "gc-workflow":
			if readCount[beadID] == 1 {
				return perfBeadSnapshot{ID: beadID, Status: "open", Metadata: map[string]string{}}, nil
			}
			return perfBeadSnapshot{ID: beadID, Status: "closed", Metadata: map[string]string{}}, nil
		default:
			return perfBeadSnapshot{}, os.ErrNotExist
		}
	}

	prepared := &perfPreparedScenario{
		WorkflowSourceBeadID:      "gc-source",
		WorkflowCompletionTimeout: 250 * time.Millisecond,
		WorkflowPollInterval:      0,
	}
	milestones := map[string]float64{}
	started := time.Now().Add(-25 * time.Millisecond)
	if err := waitForPerfWorkflowCompletion(prepared, perfCommandRequest{}, started, milestones); err != nil {
		t.Fatalf("waitForPerfWorkflowCompletion: %v", err)
	}
	if _, ok := milestones[perfMilestoneWorkflowAssigned]; !ok {
		t.Fatalf("missing %q milestone: %+v", perfMilestoneWorkflowAssigned, milestones)
	}
	if _, ok := milestones[perfMilestoneWorkflowClosed]; !ok {
		t.Fatalf("missing %q milestone: %+v", perfMilestoneWorkflowClosed, milestones)
	}
	if milestones[perfMilestoneWorkflowClosed] < milestones[perfMilestoneWorkflowAssigned] {
		t.Fatalf("workflow closed milestone %.2fms should be >= assignment %.2fms", milestones[perfMilestoneWorkflowClosed], milestones[perfMilestoneWorkflowAssigned])
	}
}

func TestCollectPerfWorkflowControllerMetricsRecordsStepBreakdown(t *testing.T) {
	origReader := perfBeadSnapshotReader
	t.Cleanup(func() {
		perfBeadSnapshotReader = origReader
	})

	perfBeadSnapshotReader = func(_ string, _ string, _ []string, beadID string) (perfBeadSnapshot, error) {
		switch beadID {
		case "retry-load":
			return perfBeadSnapshot{ID: beadID, Metadata: map[string]string{"gc.step_id": "load-context"}}, nil
		case "scope-setup":
			return perfBeadSnapshot{ID: beadID, Metadata: map[string]string{"gc.step_id": "workspace-setup"}}, nil
		case "wf-final":
			return perfBeadSnapshot{ID: beadID, Metadata: map[string]string{"gc.step_ref": "mol-scoped-work.workflow-finalize"}}, nil
		default:
			return perfBeadSnapshot{ID: beadID}, nil
		}
	}

	logPath := filepath.Join(t.TempDir(), "control-dispatcher-trace.log")
	logData := strings.Join([]string{
		"2026-04-23T20:00:01Z serve process bead=retry-load kind=retry",
		"2026-04-23T20:00:04Z serve processed bead=retry-load kind=retry",
		"2026-04-23T20:00:05Z serve process bead=scope-setup kind=scope-check",
		"2026-04-23T20:00:05Z scope-check bead=scope-setup phase=load-snapshot ok dur=950ms",
		"2026-04-23T20:00:06Z scope-check bead=scope-setup phase=close-body ok dur=875ms",
		"2026-04-23T20:00:08Z serve processed bead=scope-setup kind=scope-check",
		"2026-04-23T20:00:09Z serve process bead=wf-final kind=workflow-finalize",
		"2026-04-23T20:00:10Z serve processed bead=wf-final kind=workflow-finalize",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(logData), 0o644); err != nil {
		t.Fatalf("WriteFile(control-dispatcher-trace.log): %v", err)
	}

	started := time.Date(2026, 4, 23, 20, 0, 0, 0, time.UTC)
	metrics, err := collectPerfWorkflowControllerMetrics(logPath, "/tmp/fake-gc", t.TempDir(), nil, started)
	if err != nil {
		t.Fatalf("collectPerfWorkflowControllerMetrics: %v", err)
	}

	gotSteps := make(map[string]float64, len(metrics.Steps))
	for _, step := range metrics.Steps {
		gotSteps[step.Name] = step.DurationMs
	}
	for name, want := range map[string]float64{
		"workflow.load_context.retry":                        3000,
		"workflow.workspace_setup.scope_check":               3000,
		"workflow.workspace_setup.scope_check.load_snapshot": 950,
		"workflow.workspace_setup.scope_check.close_body":    875,
		"workflow.workflow_finalize":                         1000,
	} {
		if got := gotSteps[name]; got != want {
			t.Fatalf("step %q = %.0fms, want %.0fms; all=%v", name, got, want, gotSteps)
		}
	}

	for name, want := range map[string]float64{
		"workflow.load_context.retry_closed":          4000,
		"workflow.workspace_setup.scope_check_closed": 8000,
		"workflow.body_closed":                        6000,
		"workflow.workflow_finalize_closed":           10000,
	} {
		if got := metrics.Milestones[name]; got != want {
			t.Fatalf("milestone %q = %.0fms, want %.0fms; all=%v", name, got, want, metrics.Milestones)
		}
	}
}

func TestRunPerfCommandWithPTYCapturesPromptReadyMilestones(t *testing.T) {
	outcome, err := runPerfCommandWithPTY(perfCommandRequest{
		Executable: "/bin/bash",
		Args: []string{
			"-lc",
			`printf "Waiting for session to start...\nAttaching...\nGC_PERF_READY> "; read line`,
		},
	}, perfAttachReadyPromptToken, 5*time.Second)
	if err != nil {
		t.Fatalf("runPerfCommandWithPTY: %v", err)
	}
	if outcome.Result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; stdout=%q stderr=%q", outcome.Result.ExitCode, outcome.Result.Stdout, outcome.Result.Stderr)
	}
	if _, ok := outcome.Milestones[perfMilestonePromptReady]; !ok {
		t.Fatalf("missing %q milestone: %+v", perfMilestonePromptReady, outcome.Milestones)
	}
	if _, ok := outcome.Milestones[perfMilestoneAttaching]; !ok {
		t.Fatalf("missing %q milestone: %+v", perfMilestoneAttaching, outcome.Milestones)
	}
	if _, ok := outcome.Milestones[perfMilestoneWaitingForStart]; !ok {
		t.Fatalf("missing %q milestone: %+v", perfMilestoneWaitingForStart, outcome.Milestones)
	}
	if !bytes.Contains([]byte(outcome.Result.Stdout), []byte(perfAttachReadyPromptToken)) {
		t.Fatalf("stdout = %q, want prompt token %q", outcome.Result.Stdout, perfAttachReadyPromptToken)
	}
}

func TestSessionNewPerfTraceWritesStepBreakdown(t *testing.T) {
	configureIsolatedRuntimeEnv(t)
	t.Setenv("GC_SESSION", "fake")
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_DOLT", "skip")
	t.Setenv("GC_BOOTSTRAP", "skip")

	cityDir := t.TempDir()
	if err := ensureCityScaffold(cityDir); err != nil {
		t.Fatalf("ensureCityScaffold: %v", err)
	}
	cityConfig := `[workspace]
name = "perf-city"
start_command = "true"

[[agent]]
name = "helper"
`
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityConfig), 0o644); err != nil {
		t.Fatalf("WriteFile(city.toml): %v", err)
	}

	tracePath := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv(perfTraceFileEnv, tracePath)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--city", cityDir, "session", "new", "helper", "--no-attach"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(session new) = %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	trace, err := readPerfTraceReport(tracePath)
	if err != nil {
		t.Fatalf("readPerfTraceReport: %v", err)
	}
	if len(trace.Steps) == 0 {
		t.Fatal("trace.Steps is empty")
	}

	seen := make(map[string]bool, len(trace.Steps))
	for _, step := range trace.Steps {
		seen[step.Name] = true
	}
	for _, want := range []string{
		"session.new.resolve_city",
		"session.new.load_config",
		"session.new.resolve_template",
		"session.new.create_session",
	} {
		if !seen[want] {
			t.Fatalf("missing step %q in trace: %+v", want, trace.Steps)
		}
	}
}
