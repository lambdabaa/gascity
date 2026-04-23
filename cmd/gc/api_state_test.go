package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestControllerStateReadAccess(t *testing.T) {
	sp := runtime.NewFake()
	ep := events.NewFake()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs: []config.Rig{
			{Name: "rig1", Path: t.TempDir()},
		},
	}

	cs := newControllerState(context.Background(), cfg, sp, ep, "test-city", t.TempDir())

	if got := cs.CityName(); got != "test-city" {
		t.Errorf("CityName() = %q, want %q", got, "test-city")
	}
	if cs.Config() != cfg {
		t.Error("Config() returned wrong config")
	}
	if cs.SessionProvider() != sp {
		t.Error("SessionProvider() returned wrong provider")
	}
	if cs.EventProvider() != ep {
		t.Error("EventProvider() returned wrong provider")
	}

	stores := cs.BeadStores()
	if len(stores) != 2 {
		t.Errorf("BeadStores() len = %d, want 2 (city + rig)", len(stores))
	}
	if stores[cs.CityName()] == nil {
		t.Errorf("BeadStores()[%q] = nil", cs.CityName())
	}
	if cs.BeadStore("rig1") == nil {
		t.Error("BeadStore(rig1) = nil")
	}
	if cs.BeadStore("nonexistent") != nil {
		t.Error("BeadStore(nonexistent) should be nil")
	}

	provs := cs.MailProviders()
	if len(provs) != 1 {
		t.Errorf("MailProviders() len = %d, want 1", len(provs))
	}
	if cs.MailProvider("rig1") == nil {
		t.Error("MailProvider(rig1) = nil")
	}
}

func TestControllerStateConcurrentAccess(t *testing.T) {
	sp := runtime.NewFake()
	ep := events.NewFake()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs: []config.Rig{
			{Name: "rig1", Path: t.TempDir()},
		},
	}

	cs := newControllerState(context.Background(), cfg, sp, ep, "test-city", t.TempDir())

	// Concurrent readers should not race.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cs.Config()
			_ = cs.SessionProvider()
			_ = cs.BeadStores()
			_ = cs.MailProviders()
			_ = cs.EventProvider()
			_ = cs.CityName()
			_ = cs.CityPath()
		}()
	}
	wg.Wait()
}

func TestControllerStateUpdate(t *testing.T) {
	sp := runtime.NewFake()
	ep := events.NewFake()
	cfg1 := &config.City{
		Workspace: config.Workspace{Name: "city1"},
		Rigs: []config.Rig{
			{Name: "rig1", Path: t.TempDir()},
		},
	}

	cs := newControllerState(context.Background(), cfg1, sp, ep, "city1", t.TempDir())

	if len(cs.BeadStores()) != 2 {
		t.Fatalf("initial stores = %d, want 2 (city + rig)", len(cs.BeadStores()))
	}

	// Update with new config adding a rig.
	cfg2 := &config.City{
		Workspace: config.Workspace{Name: "city1"},
		Rigs: []config.Rig{
			{Name: "rig1", Path: t.TempDir()},
			{Name: "rig2", Path: t.TempDir()},
		},
	}

	sp2 := runtime.NewFake()
	cs.update(cfg2, sp2)

	if len(cs.BeadStores()) != 3 {
		t.Errorf("updated stores = %d, want 3 (city + 2 rigs)", len(cs.BeadStores()))
	}
	if cs.SessionProvider() != sp2 {
		t.Error("SessionProvider() not updated")
	}
	if cs.Config() != cfg2 {
		t.Error("Config() not updated")
	}
}

func TestControllerStateNilEventProvider(t *testing.T) {
	sp := runtime.NewFake()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
	}

	cs := newControllerState(context.Background(), cfg, sp, nil, "test-city", t.TempDir())

	if cs.EventProvider() != nil {
		t.Error("EventProvider() should be nil when events disabled")
	}
}

func TestControllerStateOrdersIncludeVisibleCityRoot(t *testing.T) {
	cityDir := t.TempDir()
	autoDir := filepath.Join(cityDir, "orders", "digest")
	if err := os.MkdirAll(autoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "order.toml"), []byte(`
[order]
formula = "mol-digest"
gate = "cooldown"
interval = "24h"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cs := newControllerState(context.Background(), &config.City{
		Workspace: config.Workspace{Name: "test-city"},
	}, runtime.NewFake(), events.NewFake(), "test-city", cityDir)

	aa := cs.Orders()
	if len(aa) != 1 {
		t.Fatalf("Orders() returned %d entries, want 1", len(aa))
	}
	if aa[0].Name != "digest" {
		t.Fatalf("order name = %q, want digest", aa[0].Name)
	}
}

// Verify controllerState satisfies the api.State interface at compile time.
// This uses a blank import check, not an explicit runtime assertion.
var _ interface {
	Config() *config.City
	SessionProvider() runtime.Provider
	BeadStore(string) beads.Store
	BeadStores() map[string]beads.Store
	EventProvider() events.Provider
	CityName() string
	CityPath() string
} = (*controllerState)(nil)

// Verify controllerState satisfies StateMutator at compile time.
var _ interface {
	SuspendAgent(string) error
	ResumeAgent(string) error
	SuspendRig(string) error
	ResumeRig(string) error
} = (*controllerState)(nil)

// TestBuildStoresExecProviderSetsRigPrefix verifies that when the bead
// provider is "exec:<script>", each rig's store receives the rig's own
// GC_BEADS_PREFIX via SetEnv. This defends the fix in PR #421
// (api_state.go: env["GC_BEADS_PREFIX"] = prefix) against silent regression.
//
// Regression target: if api_state.go's openRigStore stopped setting
// GC_BEADS_PREFIX before calling SetEnv, this test would fail.
func TestBuildStoresExecProviderSetsRigPrefix(t *testing.T) {
	captureDir := t.TempDir()

	// Spy script: record GC_BEADS_PREFIX to a unique file on each invocation,
	// then return a valid empty list so the caller succeeds.
	spyScript := `#!/bin/sh
op="$1"
prefix="$GC_BEADS_PREFIX"
outfile="` + captureDir + `/prefix-$$.env"
echo "$prefix" > "$outfile"
case "$op" in
  list)  echo '[]' ;;
  ready) echo '[]' ;;
  *)     exit 2 ;;
esac
`
	spyDir := t.TempDir()
	spyPath := filepath.Join(spyDir, "spy-provider")
	if err := os.WriteFile(spyPath, []byte(spyScript), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GC_BEADS", "exec:"+spyPath)

	rigADir := t.TempDir()
	rigBDir := t.TempDir()

	cfg := &config.City{
		Workspace: config.Workspace{Name: "prefix-test"},
		Rigs: []config.Rig{
			{Name: "rig-a", Path: rigADir, Prefix: "rig-a-"},
			{Name: "rig-b", Path: rigBDir, Prefix: "rig-b-"},
		},
	}

	cityDir := t.TempDir()
	cs := newControllerState(context.Background(), cfg, runtime.NewFake(), events.NewFake(), "prefix-test", cityDir)

	// Trigger each rig's store to dispatch to the spy script.
	for _, name := range []string{"rig-a", "rig-b"} {
		store := cs.BeadStore(name)
		if store == nil {
			t.Fatalf("BeadStore(%q) = nil", name)
		}
		_, err := store.ListOpen()
		if err != nil {
			t.Fatalf("ListOpen on %q: %v", name, err)
		}
	}

	// Read captured prefixes from spy output files.
	entries, err := os.ReadDir(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	var captured []string
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(captureDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		captured = append(captured, strings.TrimSpace(string(data)))
	}

	if len(captured) < 2 {
		t.Fatalf("expected at least 2 spy invocations, got %d", len(captured))
	}

	sort.Strings(captured)
	// With two rigs we expect exactly "rig-a-" and "rig-b-".
	found := map[string]bool{}
	for _, p := range captured {
		found[p] = true
		if p == "" {
			t.Error("spy recorded an empty GC_BEADS_PREFIX — the prefix was not set")
		}
	}
	if !found["rig-a-"] {
		t.Errorf("no invocation received GC_BEADS_PREFIX=%q; captured: %v", "rig-a-", captured)
	}
	if !found["rig-b-"] {
		t.Errorf("no invocation received GC_BEADS_PREFIX=%q; captured: %v", "rig-b-", captured)
	}
}
