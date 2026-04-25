package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const perfTraceFileEnv = "GC_PERF_TRACE_FILE"

type perfTraceReport struct {
	Command []string        `json:"command"`
	Steps   []perfTraceStep `json:"steps,omitempty"`
}

type perfTraceStep struct {
	Name       string  `json:"name"`
	DurationMs float64 `json:"duration_ms"`
}

type perfTraceCollector struct {
	mu     sync.Mutex
	path   string
	report perfTraceReport
}

var activePerfTrace struct {
	mu        sync.Mutex
	collector *perfTraceCollector
}

func maybeStartPerfTrace(args []string) func() {
	path := strings.TrimSpace(os.Getenv(perfTraceFileEnv))
	if path == "" {
		return func() {}
	}
	collector := &perfTraceCollector{
		path: path,
		report: perfTraceReport{
			Command: append([]string(nil), args...),
		},
	}
	activePerfTrace.mu.Lock()
	activePerfTrace.collector = collector
	activePerfTrace.mu.Unlock()
	return func() {
		activePerfTrace.mu.Lock()
		if activePerfTrace.collector == collector {
			activePerfTrace.collector = nil
		}
		activePerfTrace.mu.Unlock()
		_ = collector.write()
	}
}

func perfTraceStepTimer(name string) func() {
	name = strings.TrimSpace(name)
	if name == "" {
		return func() {}
	}
	activePerfTrace.mu.Lock()
	collector := activePerfTrace.collector
	activePerfTrace.mu.Unlock()
	if collector == nil {
		return func() {}
	}
	start := time.Now()
	return func() {
		collector.addStep(name, time.Since(start))
	}
}

func (c *perfTraceCollector) addStep(name string, duration time.Duration) {
	if c == nil || strings.TrimSpace(name) == "" {
		return
	}
	c.mu.Lock()
	c.report.Steps = append(c.report.Steps, perfTraceStep{
		Name:       name,
		DurationMs: durationMs(duration),
	})
	report := perfTraceReport{
		Command: append([]string(nil), c.report.Command...),
		Steps:   append([]perfTraceStep(nil), c.report.Steps...),
	}
	c.mu.Unlock()
	_ = writePerfTraceReport(c.path, report)
}

func (c *perfTraceCollector) write() error {
	if c == nil || strings.TrimSpace(c.path) == "" {
		return nil
	}
	c.mu.Lock()
	report := perfTraceReport{
		Command: append([]string(nil), c.report.Command...),
		Steps:   append([]perfTraceStep(nil), c.report.Steps...),
	}
	c.mu.Unlock()
	return writePerfTraceReport(c.path, report)
}

func writePerfTraceReport(path string, report perfTraceReport) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
