//go:build integration

package exec //nolint:revive // internal package, always imported with alias

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/runtimetest"
)

// TestExternalScriptConformance runs the runtime.Provider conformance suite
// against a real exec-provider script supplied at test time. It is intentionally
// substrate-agnostic: private packs can provide the script path without this
// repository knowing which remote runtime backs it.
//
// Required:
//
//	GC_RUNTIME_CONFORMANCE_SCRIPT=/path/to/exec-provider-script
//
// Optional:
//
//	GC_RUNTIME_CONFORMANCE_COMMAND="sleep 300"
//	GC_RUNTIME_CONFORMANCE_WORKDIR=/path/to/workdir
//	GC_RUNTIME_CONFORMANCE_TIMEOUT=60s
//	GC_RUNTIME_CONFORMANCE_START_TIMEOUT=5m
//
// Example:
//
//	GC_RUNTIME_CONFORMANCE_SCRIPT=/path/to/private/pack/scripts/worker go test -tags integration ./internal/runtime/exec -run TestExternalScriptConformance -timeout 30m
func TestExternalScriptConformance(t *testing.T) {
	script := os.Getenv("GC_RUNTIME_CONFORMANCE_SCRIPT")
	if script == "" {
		t.Skip("GC_RUNTIME_CONFORMANCE_SCRIPT not set")
	}
	if st, err := os.Stat(script); err != nil {
		t.Fatalf("stat GC_RUNTIME_CONFORMANCE_SCRIPT %q: %v", script, err)
	} else if st.IsDir() {
		t.Fatalf("GC_RUNTIME_CONFORMANCE_SCRIPT %q is a directory", script)
	}

	command := getenvDefault("GC_RUNTIME_CONFORMANCE_COMMAND", "sleep 300")
	workDirOverride := os.Getenv("GC_RUNTIME_CONFORMANCE_WORKDIR")

	p := NewProvider(script)
	p.timeout = getenvDuration(t, "GC_RUNTIME_CONFORMANCE_TIMEOUT", 60*time.Second)
	p.startTimeout = getenvDuration(t, "GC_RUNTIME_CONFORMANCE_START_TIMEOUT", 5*time.Minute)
	sessionPrefix := getenvDefault("GC_RUNTIME_CONFORMANCE_SESSION_PREFIX", fmt.Sprintf("gc-exec-script-conform-%d", time.Now().UnixNano()))

	var counter int64
	newSession := func(t *testing.T) (runtime.Provider, runtime.Config, string) {
		t.Helper()
		id := atomic.AddInt64(&counter, 1)
		name := fmt.Sprintf("%s-%d", sessionPrefix, id)
		t.Cleanup(func() { _ = p.Stop(name) })
		return p, runtime.Config{Command: command, WorkDir: conformanceWorkDir(t, workDirOverride)}, name
	}

	runtimetest.RunLifecycleTests(t, newSession)

	t.Run("SharedSession", func(t *testing.T) {
		name := sessionPrefix + "-shared"
		cfg := runtime.Config{Command: command, WorkDir: conformanceWorkDir(t, workDirOverride)}
		if err := p.Start(context.Background(), name, cfg); err != nil {
			t.Fatalf("Start shared session: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop(name) })
		runtimetest.RunSessionTests(t, p, cfg, name)
	})
}

func conformanceWorkDir(t *testing.T, override string) string {
	t.Helper()
	if override != "" {
		return override
	}
	return t.TempDir()
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	dur, err := time.ParseDuration(value)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", key, value, err)
	}
	return dur
}
