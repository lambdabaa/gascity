package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/shellquote"
)

const (
	perfAttachReadyPromptToken = "GC_PERF_READY>"
	perfAttachReadyShellEntry  = `PS1="GC_PERF_READY> "; export PS1; PROMPT_COMMAND=""; export PROMPT_COMMAND; exec bash --noprofile --norc -i`

	perfMilestoneWaitingForStart = "cli.waiting_for_start"
	perfMilestoneAttaching       = "cli.attaching"
	perfMilestonePromptReady     = "tty.prompt_ready"

	perfInteractiveTimeout        = 15 * time.Second
	perfManagedInteractiveTimeout = 45 * time.Second
	perfControllerReadyTimeout    = 15 * time.Second
	perfControllerStopTimeout     = 5 * time.Second
	perfTmuxPollInterval          = 50 * time.Millisecond
)

type perfRunOutcome struct {
	Result     perfCommandResult
	Milestones map[string]float64
}

func prepareSessionAttachReadyPerfScenario(rootDir string, iteration int, managed bool) (*perfPreparedScenario, error) {
	iterRoot := filepath.Join(rootDir, fmt.Sprintf("iter-%03d", iteration))
	homeDir := filepath.Join(iterRoot, "home")
	runtimeDir := filepath.Join(iterRoot, "runtime")
	tmuxTmpDir := filepath.Join(runtimeDir, "tmux")
	cityPath := filepath.Join(iterRoot, "city")
	for _, dir := range []string{homeDir, runtimeDir, tmuxTmpDir, cityPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := ensureCityScaffold(cityPath); err != nil {
		return nil, err
	}
	cityConfig := strings.TrimSpace(fmt.Sprintf(`
[workspace]
name = "perf-city"

[[agent]]
name = "helper"
provider = "perfsh"
max_active_sessions = 1

[providers.perfsh]
command = "sh"
args = ["-lc", %q, "gc-perf-shell"]
ready_prompt_prefix = %q

[providers.perfsh.env]
TERM = "xterm-256color"
`, perfAttachReadyShellEntry, perfAttachReadyPromptToken)) + "\n"
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(cityConfig), 0o644); err != nil {
		return nil, err
	}
	env := envMapFromSlice(os.Environ())
	env["HOME"] = homeDir
	env["GC_HOME"] = homeDir
	env["XDG_RUNTIME_DIR"] = runtimeDir
	env["TMUX_TMPDIR"] = tmuxTmpDir
	env["GC_SESSION"] = "tmux"
	env["GC_TMUX_SOCKET"] = "perf-city"
	env["GC_BEADS"] = "file"
	env["GC_DOLT"] = "skip"
	env["GC_BOOTSTRAP"] = "skip"

	name := perfScenarioSessionAttachReady
	if managed {
		name = perfScenarioSessionAttachReadyManaged
	}
	return &perfPreparedScenario{
		Name:              name,
		RootDir:           iterRoot,
		WorkDir:           cityPath,
		Env:               envSliceFromMap(env),
		DefaultArgs:       []string{"session", "new", "helper"},
		StopCity:          true,
		PromptReadyToken:  perfAttachReadyPromptToken,
		ManagedController: managed,
	}, nil
}

func runPreparedPerfCommand(prepared *perfPreparedScenario, req perfCommandRequest) (perfRunOutcome, error) {
	if prepared != nil && strings.TrimSpace(prepared.WorkflowSourceBeadID) != "" {
		return runPerfCommandWithWorkflowWait(prepared, req)
	}
	if prepared != nil && strings.TrimSpace(prepared.PromptReadyToken) != "" {
		timeout := perfInteractiveTimeout
		if prepared.ManagedController {
			timeout = perfManagedInteractiveTimeout
		}
		return perfInteractiveCommandRunner(req, prepared.PromptReadyToken, timeout)
	}
	result, err := perfCommandRunner(req)
	return perfRunOutcome{Result: result}, err
}

func activatePerfScenario(executable string, prepared *perfPreparedScenario, env []string) (func(), error) {
	cleanup := func() {}
	if prepared != nil && prepared.ManagedController {
		var err error
		cleanup, err = startPerfController(executable, prepared, env)
		if err != nil {
			return nil, err
		}
	}
	if err := activatePreparedPerfScenario(executable, prepared, withoutEnvKey(env, perfTraceFileEnv)); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}

func startPerfController(executable string, prepared *perfPreparedScenario, env []string) (func(), error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(executable, "start", "--controller", prepared.WorkDir)
	cmd.Dir = prepared.WorkDir
	cmd.Env = withoutEnvKey(env, perfTraceFileEnv)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	deadline := time.Now().Add(perfControllerReadyTimeout)
	for time.Now().Before(deadline) {
		if pid := controllerAlive(prepared.WorkDir); pid != 0 {
			return func() {
				stopPerfController(executable, prepared, env, cmd, done)
			}, nil
		}
		select {
		case err := <-done:
			return nil, fmt.Errorf("controller exited before becoming ready: %v\nstdout:\n%s\nstderr:\n%s",
				err, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	<-done
	return nil, fmt.Errorf("timed out waiting for controller socket\nstdout:\n%s\nstderr:\n%s",
		strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
}

func stopPerfController(executable string, prepared *perfPreparedScenario, env []string, cmd *exec.Cmd, done <-chan error) {
	stopEnv := withoutEnvKey(env, perfTraceFileEnv)
	_, err := runPerfCleanupCommand(perfCommandRequest{
		Executable: executable,
		Args:       []string{"stop", prepared.WorkDir},
		Dir:        prepared.WorkDir,
		Env:        stopEnv,
	}, perfCleanupTimeout)
	if err != nil {
		forceKillPerfScenarioProcesses(prepared.RootDir, prepared.WorkDir)
	}
	select {
	case <-done:
		return
	case <-time.After(perfControllerStopTimeout):
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		forceKillPerfScenarioProcesses(prepared.RootDir, prepared.WorkDir)
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func runPerfCommandWithPTY(req perfCommandRequest, promptToken string, timeout time.Duration) (perfRunOutcome, error) {
	if strings.TrimSpace(promptToken) == "" {
		return perfRunOutcome{}, fmt.Errorf("prompt token is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	commandLine := shellquote.Join(append([]string{req.Executable}, req.Args...))
	cmd := exec.CommandContext(ctx, "script", "-q", "-e", "-f", "-c", commandLine, "/dev/null")
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return perfRunOutcome{}, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return perfRunOutcome{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return perfRunOutcome{}, err
	}

	started := time.Now()
	milestones := map[string]float64{}
	var stdout bytes.Buffer
	var mu sync.Mutex
	stopOnce := sync.Once{}
	stopCommand := func() {
		stopOnce.Do(func() {
			_ = stdin.Close()
			cancel()
		})
	}

	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, readErr := stdoutPipe.Read(buf)
			if n > 0 {
				mu.Lock()
				stdout.Write(buf[:n])
				current := stdout.String()
				recordPerfMilestones(milestones, current, started, promptToken)
				_, ready := milestones[perfMilestonePromptReady]
				mu.Unlock()
				if ready {
					stopCommand()
				}
			}
			if readErr != nil {
				readDone <- readErr
				return
			}
		}
	}()
	startPerfTmuxPromptWatcher(ctx, req.Env, promptToken, started, milestones, &mu, stopCommand)

	err = cmd.Wait()
	_ = <-readDone
	stopCommand()

	mu.Lock()
	result := perfCommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	outMilestones := make(map[string]float64, len(milestones))
	for key, value := range milestones {
		outMilestones[key] = value
	}
	mu.Unlock()
	if _, ok := outMilestones[perfMilestonePromptReady]; ok {
		result.ExitCode = 0
		return perfRunOutcome{Result: result, Milestones: outMilestones}, nil
	}

	if err == nil {
		result.ExitCode = 0
		return perfRunOutcome{Result: result, Milestones: outMilestones}, fmt.Errorf("process exited before prompt token %q appeared", promptToken)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		if ctx.Err() == context.DeadlineExceeded {
			return perfRunOutcome{Result: result, Milestones: outMilestones}, fmt.Errorf("timed out waiting for prompt token %q", promptToken)
		}
		return perfRunOutcome{Result: result, Milestones: outMilestones}, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return perfRunOutcome{Result: result, Milestones: outMilestones}, fmt.Errorf("timed out waiting for prompt token %q", promptToken)
	}
	return perfRunOutcome{Result: result, Milestones: outMilestones}, err
}

func recordPerfMilestones(milestones map[string]float64, output string, started time.Time, promptToken string) {
	recordPerfMilestone(milestones, perfMilestoneWaitingForStart, output, "Waiting for session to start...", started)
	recordPerfMilestone(milestones, perfMilestoneAttaching, output, "Attaching...", started)
	recordPerfMilestone(milestones, perfMilestoneAttaching, output, "Attaching to session ", started)
	recordPerfMilestone(milestones, perfMilestonePromptReady, output, promptToken, started)
}

func recordPerfMilestone(milestones map[string]float64, name, output, needle string, started time.Time) {
	if milestones == nil || needle == "" {
		return
	}
	if _, exists := milestones[name]; exists {
		return
	}
	if strings.Contains(output, needle) {
		milestones[name] = durationMs(time.Since(started))
	}
}

func startPerfTmuxPromptWatcher(ctx context.Context, env []string, promptToken string, started time.Time, milestones map[string]float64, mu *sync.Mutex, onReady func()) {
	cfg, ok := perfTmuxWatchConfigFromEnv(env)
	if !ok {
		return
	}
	go func() {
		ticker := time.NewTicker(perfTmuxPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sessionName, err := perfTmuxSessionName(cfg.env, cfg.socketName)
				if err != nil || sessionName == "" {
					continue
				}
				pane, err := perfTmuxCapturePane(cfg.env, cfg.socketName, sessionName)
				if err != nil {
					continue
				}
				mu.Lock()
				recordPerfMilestones(milestones, pane, started, promptToken)
				_, ready := milestones[perfMilestonePromptReady]
				mu.Unlock()
				if !ready {
					continue
				}
				onReady()
				return
			}
		}
	}()
}

type perfTmuxWatchConfig struct {
	env        []string
	socketName string
}

func perfTmuxWatchConfigFromEnv(env []string) (perfTmuxWatchConfig, bool) {
	envMap := envMapFromSlice(env)
	if strings.TrimSpace(envMap["GC_SESSION"]) != "tmux" {
		return perfTmuxWatchConfig{}, false
	}
	socketName := strings.TrimSpace(envMap["GC_TMUX_SOCKET"])
	if socketName == "" {
		return perfTmuxWatchConfig{}, false
	}
	return perfTmuxWatchConfig{
		env:        append([]string(nil), env...),
		socketName: socketName,
	}, true
}

func perfTmuxSessionName(env []string, socketName string) (string, error) {
	out, err := perfRunTmuxCommand(env, socketName, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sessionName := strings.TrimSpace(line)
		if sessionName == "" {
			continue
		}
		return sessionName, nil
	}
	return "", nil
}

func perfTmuxCapturePane(env []string, socketName, sessionName string) (string, error) {
	return perfRunTmuxCommand(env, socketName, "capture-pane", "-p", "-t", sessionName, "-S", "-100")
}

func perfRunTmuxCommand(env []string, socketName string, args ...string) (string, error) {
	cmdArgs := append([]string{"-L", socketName}, args...)
	cmd := exec.Command("tmux", cmdArgs...)
	if len(env) > 0 {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
